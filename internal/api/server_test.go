package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/rule"
	"github.com/dot/gmail-archiver/internal/storage"
)

// [Test]
func TestAPIServer(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "api_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer database.Close()

	storageEngine, err := storage.New(tmpDir)
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}

	cfg := &config.Config{
		DataDir:  tmpDir,
		HTTPPort: 8080,
		APIKey:   "secret-token-123",
	}

	// Setup roster and rule for assignment tests
	csvContent := `学号,性别,姓名,班级
240809010501,男,支全振,2024级计算机科学与技术5班
240809010502,男,马祥宇,2024级计算机科学与技术5班
`
	_ = os.WriteFile(filepath.Join(tmpDir, "roster.csv"), []byte(csvContent), 0o644)
	yamlContent := `id: "parallel_computing_lab1"
name: "并行计算实验1"
deadline: "2026-09-23T18:00:00+08:00"
rosters:
  - "roster.csv"
patterns:
  subject_regex: "^并行计算-实验1-(?P<name>[\\p{Han}\\w]+)$"
  attachment_regex: "^实验1-(?P<class>[\\w\\p{Han}]+)-(?P<student_id>\\d{12})-(?P<name>[\\p{Han}\\w]+)\\.(?P<ext>zip|rar|7z|tar\\.gz)$"
target_filename: "实验1-{class}-{student_id}-{name}.{ext}"
`
	_ = os.WriteFile(filepath.Join(tmpDir, "rule.yaml"), []byte(yamlContent), 0o644)
	ruleEngine := rule.NewEngine(tmpDir)
	_, _ = ruleEngine.LoadRuleFile("rule.yaml")

	srv := NewServer(cfg, database, storageEngine, nil, ruleEngine)
	handler := srv.Routes()

	// 1. Health check should be accessible without auth
	reqHealth := httptest.NewRequest("GET", "/health", nil)
	recHealth := httptest.NewRecorder()
	handler.ServeHTTP(recHealth, reqHealth)

	if recHealth.Code != http.StatusOK {
		t.Fatalf("health endpoint returned status %d", recHealth.Code)
	}

	// 2. Attachments without API key should return 401
	reqListNoAuth := httptest.NewRequest("GET", "/api/attachments", nil)
	recListNoAuth := httptest.NewRecorder()
	handler.ServeHTTP(recListNoAuth, reqListNoAuth)

	if recListNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", recListNoAuth.Code)
	}

	// 3. Save a test attachment
	fileContent := []byte("confidential document bytes")
	now := time.Now().UTC()
	relPath, hash, size, err := storageEngine.Save(bytes.NewReader(fileContent), "财务报表.pdf", now)
	if err != nil {
		t.Fatalf("storage.Save error = %v", err)
	}

	att := &db.Attachment{
		MessageID:   "<finance-001@example.com>",
		Sender:      "CFO <cfo@example.com>",
		Subject:     "Q3 Financials",
		ReceivedAt:  now,
		Filename:    "财务报表.pdf",
		FileSize:    size,
		SHA256:      hash,
		MIMEType:    "application/pdf",
		StoragePath: relPath,
		CreatedAt:   now,
	}
	id, err := database.InsertAttachment(att)
	if err != nil {
		t.Fatalf("InsertAttachment error = %v", err)
	}

	// 4. Attachments with X-API-Key should return 200 and data
	reqListAuth := httptest.NewRequest("GET", "/api/attachments?keyword=财务", nil)
	reqListAuth.Header.Set("X-API-Key", "secret-token-123")
	recListAuth := httptest.NewRecorder()
	handler.ServeHTTP(recListAuth, reqListAuth)

	if recListAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", recListAuth.Code)
	}

	var listResp struct {
		Data       []db.Attachment `json:"data"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(recListAuth.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}
	if listResp.Pagination.Total != 1 || len(listResp.Data) != 1 {
		t.Fatalf("expected 1 record, got %d", listResp.Pagination.Total)
	}

	// 5. Download attachment with Bearer token
	reqDownload := httptest.NewRequest("GET", fmt.Sprintf("/api/attachments/%d/download", id), nil)
	reqDownload.Header.Set("Authorization", "Bearer secret-token-123")
	recDownload := httptest.NewRecorder()
	handler.ServeHTTP(recDownload, reqDownload)

	if recDownload.Code != http.StatusOK {
		t.Fatalf("download expected 200 OK, got %d", recDownload.Code)
	}
	if !bytes.Equal(recDownload.Body.Bytes(), fileContent) {
		t.Fatalf("downloaded content mismatch")
	}

	// Verify Content-Disposition header
	dispHeader := recDownload.Header().Get("Content-Disposition")
	if dispHeader == "" {
		t.Errorf("missing Content-Disposition header")
	}

	// 6. Test Assignment submission recording with overwrite mechanism
	sub1 := &db.Submission{
		AssignmentID:   "parallel_computing_lab1",
		StudentID:      "240809010501",
		StudentName:    "支全振",
		ClassName:      "2024级计算机科学与技术5班",
		AttachmentID:   id,
		SubmittedAt:    now.Add(-2 * time.Hour),
		TargetFilename: "实验1-241-240809010501-支全振.zip",
	}
	_, _, err = database.RecordSubmission(sub1)
	if err != nil {
		t.Fatalf("RecordSubmission 1: %v", err)
	}

	// Student corrects/resubmits assignment
	sub2 := &db.Submission{
		AssignmentID:   "parallel_computing_lab1",
		StudentID:      "240809010501",
		StudentName:    "支全振",
		ClassName:      "2024级计算机科学与技术5班",
		AttachmentID:   id,
		SubmittedAt:    now.Add(-1 * time.Hour),
		TargetFilename: "实验1-241-240809010501-支全振.zip",
	}
	v2, isUp2, err := database.RecordSubmission(sub2)
	if err != nil || v2 != 2 || !isUp2 {
		t.Fatalf("RecordSubmission 2: v=%d, isUp=%v, err=%v", v2, isUp2, err)
	}

	// 7. Verify /api/assignments/{id}/status
	reqStatus := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/status", nil)
	reqStatus.Header.Set("X-API-Key", "secret-token-123")
	recStatus := httptest.NewRecorder()
	handler.ServeHTTP(recStatus, reqStatus)
	if recStatus.Code != http.StatusOK {
		t.Fatalf("status API returned %d", recStatus.Code)
	}
	var statusResp struct {
		TotalExpected  int    `json:"total_expected"`
		SubmittedCount int    `json:"submitted_count"`
		MissingCount   int    `json:"missing_count"`
		SubmissionRate string `json:"submission_rate"`
	}
	_ = json.Unmarshal(recStatus.Body.Bytes(), &statusResp)
	if statusResp.TotalExpected != 2 || statusResp.SubmittedCount != 1 || statusResp.MissingCount != 1 {
		t.Errorf("unexpected status stats: %+v", statusResp)
	}

	// 8. Verify /api/assignments/{id}/missing
	reqMissing := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/missing", nil)
	reqMissing.Header.Set("X-API-Key", "secret-token-123")
	recMissing := httptest.NewRecorder()
	handler.ServeHTTP(recMissing, reqMissing)
	if recMissing.Code != http.StatusOK {
		t.Fatalf("missing API returned %d", recMissing.Code)
	}
	var missingResp struct {
		MissingCount int `json:"missing_count"`
		MissingList  []struct {
			StudentID string `json:"student_id"`
			Name      string `json:"name"`
		} `json:"missing_list"`
	}
	_ = json.Unmarshal(recMissing.Body.Bytes(), &missingResp)
	if missingResp.MissingCount != 1 || len(missingResp.MissingList) != 1 || missingResp.MissingList[0].StudentID != "240809010502" {
		t.Errorf("expected missing student 240809010502, got: %+v", missingResp)
	}

	// 9. Verify /api/assignments/{id}/submissions/{student_id}/history
	reqHist := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/submissions/240809010501/history", nil)
	reqHist.Header.Set("X-API-Key", "secret-token-123")
	recHist := httptest.NewRecorder()
	handler.ServeHTTP(recHist, reqHist)
	if recHist.Code != http.StatusOK {
		t.Fatalf("history API returned %d", recHist.Code)
	}
	var histResp struct {
		TotalVersion int             `json:"total_version"`
		History      []db.Submission `json:"history"`
	}
	_ = json.Unmarshal(recHist.Body.Bytes(), &histResp)
	if histResp.TotalVersion != 2 || len(histResp.History) != 2 || !histResp.History[0].IsLatest {
		t.Errorf("expected 2 versions with first being latest, got: %+v", histResp)
	}

	// 10. Verify /api/assignments/{id}/export (Zip download)
	reqExport := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/export", nil)
	reqExport.Header.Set("X-API-Key", "secret-token-123")
	recExport := httptest.NewRecorder()
	handler.ServeHTTP(recExport, reqExport)
	if recExport.Code != http.StatusOK {
		t.Fatalf("export API returned %d", recExport.Code)
	}
	if recExport.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("expected application/zip, got %s", recExport.Header().Get("Content-Type"))
	}

	// 11. Verify Web Docs endpoint / and /docs (public access)
	for _, docPath := range []string{"/", "/docs"} {
		reqDoc := httptest.NewRequest("GET", docPath, nil)
		recDoc := httptest.NewRecorder()
		handler.ServeHTTP(recDoc, reqDoc)
		if recDoc.Code != http.StatusOK {
			t.Fatalf("doc endpoint %s returned %d", docPath, recDoc.Code)
		}
		if !strings.Contains(recDoc.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("expected text/html for %s, got %s", docPath, recDoc.Header().Get("Content-Type"))
		}
		if !strings.Contains(recDoc.Body.String(), "gmail-archiver") {
			t.Fatalf("doc body missing title content")
		}
	}

	// 12. Verify Scenario 2: Single student submission download via URL token
	reqSingleStudent := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/submissions/240809010501/download?token=secret-token-123", nil)
	recSingleStudent := httptest.NewRecorder()
	handler.ServeHTTP(recSingleStudent, reqSingleStudent)
	if recSingleStudent.Code != http.StatusOK {
		t.Fatalf("single student download returned %d: %s", recSingleStudent.Code, recSingleStudent.Body.String())
	}
	if !bytes.Equal(recSingleStudent.Body.Bytes(), fileContent) {
		t.Fatalf("single student downloaded content mismatch")
	}

	// 13. Verify Scenario 3: Semester all assignments export ZIP via ?api_key= query
	reqAllExport := httptest.NewRequest("GET", "/api/assignments/export/all?api_key=secret-token-123", nil)
	recAllExport := httptest.NewRecorder()
	handler.ServeHTTP(recAllExport, reqAllExport)
	if recAllExport.Code != http.StatusOK {
		t.Fatalf("semester export all returned %d: %s", recAllExport.Code, recAllExport.Body.String())
	}
	if recAllExport.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("expected application/zip, got %s", recAllExport.Header().Get("Content-Type"))
	}

	// Check zip entries in semester export
	zipReader, err := zip.NewReader(bytes.NewReader(recAllExport.Body.Bytes()), int64(recAllExport.Body.Len()))
	if err != nil {
		t.Fatalf("failed to read semester zip: %v", err)
	}
	if len(zipReader.File) != 1 {
		t.Fatalf("expected 1 file in semester zip, got %d", len(zipReader.File))
	}
	if !strings.Contains(zipReader.File[0].Name, "240809010501") {
		t.Fatalf("expected entry with student ID, got %s", zipReader.File[0].Name)
	}

	// 14. Verify Scenario 4: Single student all assignments export ZIP
	reqStudentAll := httptest.NewRequest("GET", "/api/students/240809010501/export?token=secret-token-123", nil)
	recStudentAll := httptest.NewRecorder()
	handler.ServeHTTP(recStudentAll, reqStudentAll)
	if recStudentAll.Code != http.StatusOK {
		t.Fatalf("student all assignments export returned %d: %s", recStudentAll.Code, recStudentAll.Body.String())
	}
	if recStudentAll.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("expected application/zip, got %s", recStudentAll.Header().Get("Content-Type"))
	}

	studentZipReader, err := zip.NewReader(bytes.NewReader(recStudentAll.Body.Bytes()), int64(recStudentAll.Body.Len()))
	if err != nil {
		t.Fatalf("failed to read student zip: %v", err)
	}
	if len(studentZipReader.File) != 1 {
		t.Fatalf("expected 1 file in student zip, got %d", len(studentZipReader.File))
	}

	// 15. Verify invalid token returns 401
	reqInvalidToken := httptest.NewRequest("GET", "/api/students/240809010501/export?token=wrong-token", nil)
	recInvalidToken := httptest.NewRecorder()
	handler.ServeHTTP(recInvalidToken, reqInvalidToken)
	if recInvalidToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", recInvalidToken.Code)
	}

	// 16. Test Student Binding API
	// 16.1 Create binding with roster match
	bindBody := []byte(`{"qq_id":"1689491386","student_id":"240809010501","student_name":"支全振"}`)
	reqBind := httptest.NewRequest("POST", "/api/bindings?token=secret-token-123", bytes.NewReader(bindBody))
	reqBind.Header.Set("Content-Type", "application/json")
	recBind := httptest.NewRecorder()
	handler.ServeHTTP(recBind, reqBind)
	if recBind.Code != http.StatusOK {
		t.Fatalf("expected 200 for binding, got %d: %s", recBind.Code, recBind.Body.String())
	}

	// 16.2 Get binding
	reqGetBind := httptest.NewRequest("GET", "/api/bindings/1689491386?token=secret-token-123", nil)
	recGetBind := httptest.NewRecorder()
	handler.ServeHTTP(recGetBind, reqGetBind)
	if recGetBind.Code != http.StatusOK {
		t.Fatalf("expected 200 for get binding, got %d: %s", recGetBind.Code, recGetBind.Body.String())
	}
	var bindResp db.StudentBinding
	_ = json.Unmarshal(recGetBind.Body.Bytes(), &bindResp)
	if bindResp.StudentID != "240809010501" || bindResp.StudentName != "支全振" || bindResp.ClassName != "245班" {
		t.Errorf("unexpected binding response: %+v", bindResp)
	}

	// 17. Test Direct Upload API (Direct upload assignment submission)
	bodyBuf := &bytes.Buffer{}
	mpWriter := multipart.NewWriter(bodyBuf)
	_ = mpWriter.WriteField("qq_id", "1689491386") // Should auto fill student_id 240809010501
	fileWriter, _ := mpWriter.CreateFormFile("file", "my_code_submission.zip")
	_, _ = fileWriter.Write([]byte("fake zip archive content"))
	_ = mpWriter.Close()

	reqUpload := httptest.NewRequest("POST", "/api/assignments/latest/upload?token=secret-token-123", bodyBuf)
	reqUpload.Header.Set("Content-Type", mpWriter.FormDataContentType())
	recUpload := httptest.NewRecorder()
	handler.ServeHTTP(recUpload, reqUpload)
	if recUpload.Code != http.StatusOK {
		t.Fatalf("expected 200 for upload, got %d: %s", recUpload.Code, recUpload.Body.String())
	}

	var uploadResp map[string]any
	_ = json.Unmarshal(recUpload.Body.Bytes(), &uploadResp)
	if uploadResp["success"] != true {
		t.Fatalf("upload failed: %+v", uploadResp)
	}
	if uploadResp["student_id"] != "240809010501" {
		t.Errorf("expected student_id 240809010501, got %v", uploadResp["student_id"])
	}
	expectedFilename := "实验1-245班-240809010501-支全振.zip"
	if uploadResp["target_filename"] != expectedFilename {
		t.Errorf("expected target_filename %s, got %v", expectedFilename, uploadResp["target_filename"])
	}

	// 18. Test Plagiarism Collision (Duplicate upload should be rejected with 409)
	dupBodyBuf := &bytes.Buffer{}
	dupMpWriter := multipart.NewWriter(dupBodyBuf)
	_ = dupMpWriter.WriteField("student_id", "240809010502") // Different student
	_ = dupMpWriter.WriteField("student_name", "田小雨")
	dupFileWriter, _ := dupMpWriter.CreateFormFile("file", "copied_submission.zip")
	_, _ = dupFileWriter.Write([]byte("fake zip archive content")) // Exact same content as 1689491386/240809010501!
	_ = dupMpWriter.Close()

	reqDupUpload := httptest.NewRequest("POST", "/api/assignments/latest/upload?token=secret-token-123", dupBodyBuf)
	reqDupUpload.Header.Set("Content-Type", dupMpWriter.FormDataContentType())
	recDupUpload := httptest.NewRecorder()
	handler.ServeHTTP(recDupUpload, reqDupUpload)

	if recDupUpload.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for duplicate upload, got %d: %s", recDupUpload.Code, recDupUpload.Body.String())
	}

	// 19. Test Plagiarism Report API
	reqPlag := httptest.NewRequest("GET", "/api/assignments/latest/plagiarism?token=secret-token-123", nil)
	recPlag := httptest.NewRecorder()
	handler.ServeHTTP(recPlag, reqPlag)
	if recPlag.Code != http.StatusOK {
		t.Fatalf("expected 200 for plagiarism report, got %d: %s", recPlag.Code, recPlag.Body.String())
	}

	// 20. Test Roster API
	reqRoster := httptest.NewRequest("GET", "/api/roster?token=secret-token-123", nil)
	recRoster := httptest.NewRecorder()
	handler.ServeHTTP(recRoster, reqRoster)
	if recRoster.Code != http.StatusOK {
		t.Fatalf("expected 200 for roster api, got %d: %s", recRoster.Code, recRoster.Body.String())
	}
	var rosterResp map[string]any
	_ = json.Unmarshal(recRoster.Body.Bytes(), &rosterResp)
	if int(rosterResp["total"].(float64)) != 2 {
		t.Errorf("expected 2 students in roster, got %v", rosterResp["total"])
	}

	// 21. Test Anti-impersonation and Force Binding Overwrite
	// 21.1 Attempt to bind already bound student 240809010501 to another QQ 999888 without force -> 409
	bindDupBody := []byte(`{"qq_id":"999888","student_id":"240809010501","student_name":"支全振"}`)
	reqDupBind := httptest.NewRequest("POST", "/api/bindings?token=secret-token-123", bytes.NewReader(bindDupBody))
	reqDupBind.Header.Set("Content-Type", "application/json")
	recDupBind := httptest.NewRecorder()
	handler.ServeHTTP(recDupBind, reqDupBind)
	if recDupBind.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate binding without force, got %d", recDupBind.Code)
	}

	// 21.2 Force overwrite by TA/Admin -> 200
	bindForceBody := []byte(`{"qq_id":"999888","student_id":"240809010501","student_name":"支全振","force":true}`)
	reqForceBind := httptest.NewRequest("POST", "/api/bindings?token=secret-token-123", bytes.NewReader(bindForceBody))
	reqForceBind.Header.Set("Content-Type", "application/json")
	recForceBind := httptest.NewRecorder()
	handler.ServeHTTP(recForceBind, reqForceBind)
	if recForceBind.Code != http.StatusOK {
		t.Fatalf("expected 200 for force binding, got %d: %s", recForceBind.Code, recForceBind.Body.String())
	}

	// 21.3 Unbind by student_id
	reqUnbind := httptest.NewRequest("DELETE", "/api/bindings/240809010501?token=secret-token-123", nil)
	recUnbind := httptest.NewRecorder()
	handler.ServeHTTP(recUnbind, reqUnbind)
	if recUnbind.Code != http.StatusOK {
		t.Fatalf("expected 200 for unbind by student_id, got %d: %s", recUnbind.Code, recUnbind.Body.String())
	}
}



