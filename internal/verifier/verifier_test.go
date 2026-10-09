package verifier

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dot/gmail-archiver/internal/db"
)

func TestExtractArchiveFiles_Zip(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test_homework.zip")

	// Create test zip file
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create test zip: %v", err)
	}

	zw := zip.NewWriter(f)

	// 1. Core code file
	w1, _ := zw.Create("src/gemm.cu")
	_, _ = w1.Write([]byte(`__global__ void gemm_kernel(float* a, float* b, float* c, int n) { int idx = blockDim.x * blockIdx.x + threadIdx.x; }`))

	// 2. Header file
	w2, _ := zw.Create("include/gemm.h")
	_, _ = w2.Write([]byte(`#pragma once`))

	// 3. Report file
	w3, _ := zw.Create("report.pdf")
	_, _ = w3.Write([]byte(`%PDF-1.4 report content here`))

	// 4. Ignored system file
	w4, _ := zw.Create("__MACOSX/._gemm.cu")
	_, _ = w4.Write([]byte(`junk`))

	_ = zw.Close()
	_ = f.Close()

	files, err := ExtractArchiveFiles(zipPath)
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("expected 3 files (excluding __MACOSX), got %d", len(files))
	}

	var foundCore bool
	for _, fi := range files {
		if fi.Filename == "gemm.cu" {
			if !fi.IsCoreCode || fi.Category != CategoryCoreCode {
				t.Errorf("expected gemm.cu to be core code, got %+v", fi)
			}
			foundCore = true
		}
		if fi.Filename == "report.pdf" && fi.Category != CategoryReport {
			t.Errorf("expected report.pdf to be report category, got %+v", fi)
		}
	}

	if !foundCore {
		t.Errorf("did not find gemm.cu in extracted files")
	}
}

func TestCheckSubmissionPlagiarism(t *testing.T) {
	tmpDir := t.TempDir()
	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// 1. Student A submits original homework
	now := time.Now().UTC()
	attA := &db.Attachment{
		MessageID:   "msg-001",
		Sender:      "qq:111",
		Subject:     "并行计算实验2",
		ReceivedAt:  now,
		Filename:    "实验2-241-240809010001-张三.zip",
		FileSize:    1024,
		SHA256:      "hash-archive-student-a",
		MIMEType:    "application/zip",
		StoragePath: "attachments/2026/10/a.zip",
	}
	attAID, err := database.InsertAttachment(attA)
	if err != nil {
		t.Fatalf("insert attA: %v", err)
	}

	subA := &db.Submission{
		AssignmentID:   "parallel_computing_lab2",
		StudentID:      "240809010001",
		StudentName:    "张三",
		ClassName:      "241",
		AttachmentID:   attAID,
		SubmittedAt:    now,
		TargetFilename: "实验2-241-240809010001-张三.zip",
	}
	subAID, _, err := database.RecordSubmission(subA)
	if err != nil {
		t.Fatalf("record subA: %v", err)
	}

	filesA := []*ArchivedFileInfo{
		{
			Filename:   "gemm.cu",
			Path:       "src/gemm.cu",
			Size:       500,
			SHA256:     "sha-gemm-cu-unique-impl-zhangsan",
			Category:   CategoryCoreCode,
			IsCoreCode: true,
		},
		{
			Filename:   "report.pdf",
			Path:       "report.pdf",
			Size:       2000,
			SHA256:     "sha-report-pdf-zhangsan",
			Category:   CategoryReport,
			IsCoreCode: false,
		},
	}
	if err := database.InsertSubmissionFiles(subA.ID, "parallel_computing_lab2", "240809010001", ToDBFiles(filesA)); err != nil {
		t.Fatalf("insert filesA: %v", err)
	}

	// 2. Student B tries exact archive duplication (copying zip directly)
	resB, err := CheckSubmissionPlagiarism(database, "parallel_computing_lab2", "240809010002", "hash-archive-student-a", filesA)
	if err != nil {
		t.Fatalf("check B: %v", err)
	}
	if !resB.IsDuplicate || resB.DuplicateType != "exact_archive" {
		t.Errorf("expected exact_archive collision for Student B, got %+v", resB)
	}
	if resB.MatchedStudentID != "240809010001" {
		t.Errorf("expected matched student 240809010001, got %s", resB.MatchedStudentID)
	}

	// 3. Student C modifies the report, but copies Student A's code file verbatim
	filesC := []*ArchivedFileInfo{
		{
			Filename:   "gemm.cu",
			Path:       "src/gemm.cu",
			Size:       500,
			SHA256:     "sha-gemm-cu-unique-impl-zhangsan", // same code!
			Category:   CategoryCoreCode,
			IsCoreCode: true,
		},
		{
			Filename:   "report.pdf",
			Path:       "report.pdf",
			Size:       2100,
			SHA256:     "sha-report-pdf-wangwu-modified", // modified report!
			Category:   CategoryReport,
			IsCoreCode: false,
		},
	}
	resC, err := CheckSubmissionPlagiarism(database, "parallel_computing_lab2", "240809010003", "hash-archive-student-c-diff", filesC)
	if err != nil {
		t.Fatalf("check C: %v", err)
	}
	if !resC.IsDuplicate || resC.DuplicateType != "code_collision" {
		t.Errorf("expected code_collision for Student C, got %+v", resC)
	}
	if resC.MatchedStudentID != "240809010001" {
		t.Errorf("expected matched student 240809010001, got %s", resC.MatchedStudentID)
	}

	// 4. Student A updates their own homework (self-update should NEVER be blocked)
	resSelf, err := CheckSubmissionPlagiarism(database, "parallel_computing_lab2", "240809010001", "hash-archive-student-a", filesA)
	if err != nil {
		t.Fatalf("check self: %v", err)
	}
	if resSelf.IsDuplicate {
		t.Errorf("expected self update to not be flagged as duplicate, got %+v", resSelf)
	}

	// 5. Student D submits their own independent work
	filesD := []*ArchivedFileInfo{
		{
			Filename:   "gemm.cu",
			Path:       "src/gemm.cu",
			Size:       600,
			SHA256:     "sha-gemm-cu-independent-zhaoliu",
			Category:   CategoryCoreCode,
			IsCoreCode: true,
		},
	}
	resD, err := CheckSubmissionPlagiarism(database, "parallel_computing_lab2", "240809010004", "hash-archive-student-d", filesD)
	if err != nil {
		t.Fatalf("check D: %v", err)
	}
	if resD.IsDuplicate {
		t.Errorf("expected independent submission to pass, got %+v", resD)
	}

	_ = subAID
}

