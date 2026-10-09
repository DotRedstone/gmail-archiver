package verifier

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dot/gmail-archiver/internal/db"
)

// FileCategory represents the functional type of a file inside a homework submission.
type FileCategory string

const (
	CategoryCoreCode FileCategory = "core_code" // .c, .cpp, .cu, .cc, .cxx, .asm
	CategoryCode     FileCategory = "code"      // .h, .hpp, .cuh, .py, .sh, Makefile
	CategoryReport   FileCategory = "report"    // .pdf, .docx, .doc, .md, .txt
	CategoryOther    FileCategory = "other"
)

// ArchivedFileInfo holds metadata and content hash of a file inside a submission.
type ArchivedFileInfo struct {
	Path       string       `json:"path"`
	Filename   string       `json:"filename"`
	Size       int64        `json:"size"`
	SHA256     string       `json:"sha256"`
	Category   FileCategory `json:"category"`
	IsCoreCode bool         `json:"is_core_code"`
}

// PlagiarismCheckResult represents the outcome of duplicate/hash verification.
type PlagiarismCheckResult struct {
	IsDuplicate        bool     `json:"is_duplicate"`
	DuplicateType      string   `json:"duplicate_type"` // "exact_archive" or "code_collision"
	MatchedStudentID   string   `json:"matched_student_id"`
	MatchedStudentName string   `json:"matched_student_name"`
	MatchedAssignment  string   `json:"matched_assignment"`
	IdenticalFiles     []string `json:"identical_files"`
	TotalCoreFiles     int      `json:"total_core_files"`
	CollidedCoreFiles  int      `json:"collided_core_files"`
	Message            string   `json:"message"`
}

// isIgnoredPath checks if a file is an OS artifact, IDE temp file, or directory entry.
func isIgnoredPath(p string) bool {
	clean := filepath.ToSlash(p)
	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if part == "__MACOSX" || part == ".DS_Store" || part == "Thumbs.db" || part == ".git" || part == ".vscode" || part == ".idea" {
			return true
		}
		if strings.HasPrefix(part, "._") {
			return true
		}
	}
	return false
}

// classifyFile identifies file type and determines if it is a core source implementation.
func classifyFile(filename string, size int64) (FileCategory, bool) {
	lower := strings.ToLower(filename)
	ext := filepath.Ext(lower)
	base := filepath.Base(lower)

	// Core implementation source files (excluding templates)
	if (ext == ".c" || ext == ".cpp" || ext == ".cu" || ext == ".cc" || ext == ".cxx" || ext == ".asm" || ext == ".s") && size >= 30 {
		return CategoryCoreCode, true
	}

	// Code, headers, scripts, and build files
	if ext == ".h" || ext == ".hpp" || ext == ".cuh" || ext == ".py" || ext == ".sh" ||
		base == "makefile" || base == "cmakelists.txt" {
		return CategoryCode, false
	}

	// Reports
	if ext == ".pdf" || ext == ".docx" || ext == ".doc" || ext == ".md" || ext == ".txt" {
		return CategoryReport, false
	}

	return CategoryOther, false
}

// ExtractArchiveFiles extracts all files and their SHA-256 hashes from a zip/tar file.
// If the file is not an archive, it treats the file itself as a single item.
func ExtractArchiveFiles(absPath string) ([]*ArchivedFileInfo, error) {
	lower := strings.ToLower(absPath)

	if strings.HasSuffix(lower, ".zip") {
		return extractZip(absPath)
	} else if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
		return extractTarGz(absPath)
	} else if strings.HasSuffix(lower, ".tar") {
		return extractTar(absPath)
	}

	// Fallback for raw files
	return extractSingleFile(absPath)
}

func extractZip(absPath string) ([]*ArchivedFileInfo, error) {
	zr, err := zip.OpenReader(absPath)
	if err != nil {
		// If zip reading fails, fallback to single file
		return extractSingleFile(absPath)
	}
	defer zr.Close()

	var files []*ArchivedFileInfo
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if isIgnoredPath(f.Name) {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}

		hasher := sha256.New()
		written, copyErr := io.Copy(hasher, rc)
		_ = rc.Close()
		if copyErr != nil {
			continue
		}

		hashHex := hex.EncodeToString(hasher.Sum(nil))
		cat, isCore := classifyFile(f.Name, written)

		files = append(files, &ArchivedFileInfo{
			Path:       filepath.ToSlash(f.Name),
			Filename:   filepath.Base(f.Name),
			Size:       written,
			SHA256:     hashHex,
			Category:   cat,
			IsCoreCode: isCore,
		})
	}

	return files, nil
}

func extractTarGz(absPath string) ([]*ArchivedFileInfo, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return extractSingleFile(absPath)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return extractSingleFile(absPath)
	}
	defer gzr.Close()

	return readTarStream(gzr)
}

func extractTar(absPath string) ([]*ArchivedFileInfo, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return extractSingleFile(absPath)
	}
	defer f.Close()

	return readTarStream(f)
}

func readTarStream(r io.Reader) ([]*ArchivedFileInfo, error) {
	tr := tar.NewReader(r)
	var files []*ArchivedFileInfo

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if isIgnoredPath(header.Name) {
			continue
		}

		hasher := sha256.New()
		written, copyErr := io.Copy(hasher, tr)
		if copyErr != nil {
			continue
		}

		hashHex := hex.EncodeToString(hasher.Sum(nil))
		cat, isCore := classifyFile(header.Name, written)

		files = append(files, &ArchivedFileInfo{
			Path:       filepath.ToSlash(header.Name),
			Filename:   filepath.Base(header.Name),
			Size:       written,
			SHA256:     hashHex,
			Category:   cat,
			IsCoreCode: isCore,
		})
	}

	return files, nil
}

func extractSingleFile(absPath string) ([]*ArchivedFileInfo, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	hasher := sha256.New()
	written, err := io.Copy(hasher, f)
	if err != nil {
		return nil, err
	}

	hashHex := hex.EncodeToString(hasher.Sum(nil))
	cat, isCore := classifyFile(fi.Name(), written)

	return []*ArchivedFileInfo{
		{
			Path:       filepath.Base(absPath),
			Filename:   filepath.Base(absPath),
			Size:       written,
			SHA256:     hashHex,
			Category:   cat,
			IsCoreCode: isCore,
		},
	}, nil
}

// ToDBFiles converts a slice of ArchivedFileInfo to db.SubmissionFile for persistence.
func ToDBFiles(files []*ArchivedFileInfo) []*db.SubmissionFile {
	var result []*db.SubmissionFile
	for _, f := range files {
		isCode := (f.Category == CategoryCoreCode || f.Category == CategoryCode)
		result = append(result, &db.SubmissionFile{
			Filename:   f.Filename,
			Filepath:   f.Path,
			FileSize:   f.Size,
			SHA256:     f.SHA256,
			IsCode:     isCode,
			IsCoreCode: f.IsCoreCode,
		})
	}
	return result
}

// CheckSubmissionPlagiarism verifies whether the uploaded submission duplicates with any existing submission.
// It checks two tiers:
// 1. Exact Archive Collision: The entire outer archive SHA256 is identical to another student's submission.
// 2. Code File Collision: One or more core source implementation files (.c, .cpp, .cu, etc.) have identical SHA256 hashes.
func CheckSubmissionPlagiarism(
	database *db.DB,
	assignmentID string,
	studentID string,
	outerSHA256 string,
	extractedFiles []*ArchivedFileInfo,
) (*PlagiarismCheckResult, error) {
	if database == nil {
		return &PlagiarismCheckResult{IsDuplicate: false}, nil
	}

	// 1. Check Exact Archive Collision
	dupSub, err := database.CheckArchiveSHA256Duplicate(assignmentID, studentID, outerSHA256)
	if err != nil {
		return nil, fmt.Errorf("verifier: check archive duplicate: %w", err)
	}
	if dupSub != nil {
		return &PlagiarismCheckResult{
			IsDuplicate:        true,
			DuplicateType:      "exact_archive",
			MatchedStudentID:   dupSub.StudentID,
			MatchedStudentName: dupSub.StudentName,
			MatchedAssignment:  assignmentID,
			IdenticalFiles:     []string{"整包压缩包完全相同 (SHA256 Collision)"},
			Message: fmt.Sprintf(
				"该压缩包哈希指纹（%s...）与同学【%s】（%s）的提交完全一致，严禁直接复制压缩包提交！",
				outerSHA256[:10], dupSub.StudentName, dupSub.StudentID,
			),
		}, nil
	}

	// 2. Collect core implementation code files from the uploaded submission
	var coreHashes []string
	hashMap := make(map[string]string) // sha256 -> filename
	totalCore := 0

	for _, f := range extractedFiles {
		if f.IsCoreCode {
			coreHashes = append(coreHashes, f.SHA256)
			hashMap[f.SHA256] = f.Filename
			totalCore++
		}
	}

	// If no core code found, also consider any code file
	if totalCore == 0 {
		for _, f := range extractedFiles {
			if f.Category == CategoryCode && f.Size >= 50 {
				coreHashes = append(coreHashes, f.SHA256)
				hashMap[f.SHA256] = f.Filename
			}
		}
	}

	if len(coreHashes) == 0 {
		return &PlagiarismCheckResult{IsDuplicate: false}, nil
	}

	// Check core code collisions against other students' latest submissions
	collisions, err := database.FindCoreCodeCollisions(assignmentID, studentID, coreHashes)
	if err != nil {
		return nil, fmt.Errorf("verifier: check core code collision: %w", err)
	}

	if len(collisions) > 0 {
		// Group collisions by matched student
		studentCollisions := make(map[string][]*db.CodeCollisionEntry)
		for _, c := range collisions {
			studentCollisions[c.MatchedStudentID] = append(studentCollisions[c.MatchedStudentID], c)
		}

		// Find the student with the most collided files
		var topStudentID string
		var topStudentName string
		var topFiles []string
		maxCount := 0

		for sid, entries := range studentCollisions {
			if len(entries) > maxCount {
				maxCount = len(entries)
				topStudentID = sid
				topStudentName = entries[0].MatchedStudentName
				topFiles = nil
				for _, e := range entries {
					origName := hashMap[e.SHA256]
					if origName == "" {
						origName = e.OtherFilename
					}
					topFiles = append(topFiles, origName)
				}
			}
		}

		if maxCount >= 1 {
			return &PlagiarismCheckResult{
				IsDuplicate:        true,
				DuplicateType:      "code_collision",
				MatchedStudentID:   topStudentID,
				MatchedStudentName: topStudentName,
				MatchedAssignment:  assignmentID,
				IdenticalFiles:     topFiles,
				TotalCoreFiles:     totalCore,
				CollidedCoreFiles:  maxCount,
				Message: fmt.Sprintf(
					"核心源代码文件 %v 的 SHA256 哈希与同学【%s】完全一致。严禁仅修改报告或文件名抄袭他人作业，请独立完成实验！",
					topFiles, topStudentName,
				),
			}, nil
		}
	}

	return &PlagiarismCheckResult{IsDuplicate: false}, nil
}

