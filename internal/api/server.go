package api

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/imap"
	"github.com/dot/gmail-archiver/internal/roster"
	"github.com/dot/gmail-archiver/internal/rule"
	"github.com/dot/gmail-archiver/internal/storage"
	"github.com/dot/gmail-archiver/internal/verifier"
)

// [Server]
type Server struct {
	cfg           *config.Config
	database      *db.DB
	storage       *storage.Engine
	watcher       *imap.Watcher
	rules         *rule.Engine
	startTime     time.Time
	publicationMu sync.Mutex
	plagiarismMu  sync.Mutex
}

func NewServer(cfg *config.Config, database *db.DB, storageEngine *storage.Engine, watcher *imap.Watcher, ruleEngine *rule.Engine) *Server {
	return &Server{
		cfg:       cfg,
		database:  database,
		storage:   storageEngine,
		watcher:   watcher,
		rules:     ruleEngine,
		startTime: time.Now().UTC(),
	}
}

// [Router]
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	// 请求 URI 含身份标识和旧式查询令牌，不写入通用访问日志。
	r.Use(middleware.Recoverer)

	// Health check and documentation (public)
	r.Get("/", s.handleDocs)
	r.Get("/docs", s.handleDocs)
	r.Get("/health", s.handleHealth)

	// Protected routes
	r.Group(func(api chi.Router) {
		api.Use(s.apiKeyMiddleware)

		api.Route("/api/attachments", func(att chi.Router) {
			att.Get("/", s.handleListAttachments)
			att.Get("/{id}/download", s.handleDownloadAttachment)
		})

		api.Route("/api/bindings", func(b chi.Router) {
			b.Get("/", s.handleListBindings)
			b.Get("/{qq_id}", s.handleGetBinding)
			b.Post("/", s.handleCreateBinding)
			b.Delete("/{qq_id}", s.handleDeleteBinding)
		})

		api.Route("/api/v3", func(v3 chi.Router) {
			v3.Get("/bindings", s.handleListExternalBindings)
			v3.Post("/my-assignments/query", s.handleMyAssignments)
			v3.Post("/assignments/{id}/mine/query", s.handleMyAssignment)
			v3.Delete("/bindings/{student_id}", s.handleDeleteExternalBinding)
			v3.Post("/bindings/{student_id}/verify", s.handleVerifyExternalBinding)
			v3.Get("/binding-intents", s.handleGetBindingIntent)
			v3.Post("/binding-intents", s.handleCreateBindingIntent)
			v3.Post("/binding-intents/confirm", s.handleConfirmBindingIntent)
			v3.Post("/binding-intents/cancel", s.handleCancelBindingIntent)
			v3.Post("/assignments/{id}/upload", s.handleExternalAssignmentUpload)
			v3.Post("/lab-cards", s.handleLabCardUpload)
			v3.Get("/lab-cards/{number}", s.handleGetLabCard)
			v3.Get("/lab-cards/{number}/download", s.handleDownloadLabCard)
			v3.Post("/lab-cards/{number}/confirm", s.handleConfirmLabCard)
			v3.Get("/publication-drafts/{draft_id}", s.handleGetPublicationDraft)
			v3.Patch("/publication-drafts/{draft_id}", s.handleUpdatePublicationDraft)
			v3.Delete("/publication-drafts/{draft_id}", s.handleCancelPublicationDraft)
			v3.Post("/publication-drafts/{draft_id}/confirm", s.handleConfirmPublicationDraft)
			v3.Get("/publication-jobs", s.handleDuePublicationJobs)
			v3.Post("/publication-jobs/{job_id}/claim", s.handleClaimPublicationJob)
			v3.Post("/publication-jobs/{job_id}/result", s.handleRecordPublicationJobResult)
		})

		api.Route("/api/roster", func(ros chi.Router) {
			ros.Get("/", s.handleGetRoster)
		})

		api.Route("/api/assignments", func(as chi.Router) {
			as.Get("/", s.handleListAssignments)
			as.Post("/", s.handleCreateAssignment)
			as.Get("/export/all", s.handleSemesterExportZip)
			as.Get("/{id}/status", s.handleAssignmentStatus)
			as.Get("/{id}/missing", s.handleAssignmentMissing)
			as.Get("/{id}/submissions", s.handleAssignmentSubmissions)
			as.Get("/{id}/submissions/{student_id}/download", s.handleStudentSubmissionDownload)
			as.Get("/{id}/submissions/{student_id}/history", s.handleStudentSubmissionHistory)
			as.Get("/{id}/export", s.handleAssignmentExportZip)
			as.Get("/{id}/plagiarism", s.handleAssignmentPlagiarism)
			as.Post("/{id}/plagiarism/reindex", s.handleReindexAssignmentPlagiarism)
			as.Post("/{id}/upload", s.handleAssignmentUpload)
		})

		api.Route("/api/students", func(st chi.Router) {
			st.Get("/{student_id}/export", s.handleStudentAllAssignmentsExportZip)
		})
	})

	return r
}

// [AuthMiddleware]
func (s *Server) apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey == "" {
			writeJSONError(w, http.StatusServiceUnavailable, "API authentication is not configured")
			return
		}

		token := r.Header.Get("X-API-Key")
		if token == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				token = strings.TrimSpace(authHeader[7:])
			}
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.APIKey)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized: invalid or missing API key")
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		next.ServeHTTP(w, r)
	})
}

// [Health]
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var imapStatus any = "not running"
	if s.watcher != nil {
		imapStatus = s.watcher.GetStatus()
	}

	resp := map[string]any{
		"status": "ok",
		"uptime": time.Since(s.startTime).Truncate(time.Second).String(),
		"imap":   imapStatus,
	}

	writeJSON(w, http.StatusOK, resp)
}

// [ListAttachments]
func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	filter := db.AttachmentFilter{
		Keyword: q.Get("keyword"),
		Sender:  q.Get("sender"),
		Page:    page,
		Limit:   limit,
	}

	if fromStr := q.Get("from_date"); fromStr != "" {
		if t, err := parseDateParam(fromStr); err == nil {
			filter.FromDate = &t
		}
	}

	if toStr := q.Get("to_date"); toStr != "" {
		if t, err := parseDateParam(toStr); err == nil {
			filter.ToDate = &t
		}
	}

	list, total, err := s.database.ListAttachments(filter)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query attachments: "+err.Error())
		return
	}

	if list == nil {
		list = []*db.Attachment{}
	}

	totalPages := (total + int64(limit) - 1) / int64(limit)
	if totalPages == 0 {
		totalPages = 1
	}

	resp := map[string]any{
		"data": list,
		"pagination": map[string]any{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"total_pages": totalPages,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}

// [DownloadAttachment]
func (s *Server) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}

	att, err := s.database.GetAttachmentByID(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "attachment not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to get attachment: "+err.Error())
		return
	}

	absPath, err := s.storage.ResolveAbsolutePath(att.StoragePath)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, "access denied: invalid storage path")
		return
	}

	file, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "file not found on disk")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to stat file")
		return
	}

	// Content-Disposition headers (RFC 6266 + RFC 5987 for full unicode filename support)
	encodedFilename := url.PathEscape(att.Filename)
	disposition := fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", att.Filename, encodedFilename)
	w.Header().Set("Content-Disposition", disposition)
	if att.MIMEType != "" {
		w.Header().Set("Content-Type", att.MIMEType)
	}

	// http.ServeContent natively handles HTTP Range requests for partial downloads
	http.ServeContent(w, r, att.Filename, fi.ModTime(), file)
}

// [Helpers]
func parseDateParam(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date format: %s", s)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// [AssignmentHandlers]
func (s *Server) handleListAssignments(w http.ResponseWriter, r *http.Request) {
	if s.rules == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	rules := s.rules.Rules()
	type assignmentItem struct {
		ID             string    `json:"id"`
		Name           string    `json:"name"`
		Deadline       time.Time `json:"deadline"`
		TotalExpected  int       `json:"total_expected"`
		TargetFilename string    `json:"target_filename"`
	}

	list := make([]assignmentItem, 0, len(rules))
	for _, rule := range rules {
		list = append(list, assignmentItem{
			ID:             rule.ID,
			Name:           rule.Name,
			Deadline:       rule.Deadline,
			TotalExpected:  rule.Roster.Count(),
			TargetFilename: rule.TargetFilename,
		})
	}

	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAssignmentStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.rules == nil {
		writeJSONError(w, http.StatusNotFound, "rule engine not initialized")
		return
	}

	rule, ok := s.rules.GetRule(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "assignment rule not found")
		return
	}

	latestSubs, err := s.database.GetLatestSubmissions(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query submissions: "+err.Error())
		return
	}

	totalExpected := rule.Roster.Count()
	submittedCount := len(latestSubs)
	lateCount := 0
	for _, sub := range latestSubs {
		if sub.IsLate {
			lateCount++
		}
	}

	missingCount := totalExpected - submittedCount
	if missingCount < 0 {
		missingCount = 0
	}

	rate := "0.0%"
	if totalExpected > 0 {
		rate = fmt.Sprintf("%.1f%%", float64(submittedCount)/float64(totalExpected)*100)
	}

	resp := map[string]any{
		"assignment_id":   rule.ID,
		"assignment_name": rule.Name,
		"deadline":        rule.Deadline,
		"total_expected":  totalExpected,
		"submitted_count": submittedCount,
		"missing_count":   missingCount,
		"late_count":      lateCount,
		"submission_rate": rate,
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAssignmentMissing(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.rules == nil {
		writeJSONError(w, http.StatusNotFound, "rule engine not initialized")
		return
	}

	rule, ok := s.rules.GetRule(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "assignment rule not found")
		return
	}

	latestSubs, err := s.database.GetLatestSubmissions(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query submissions: "+err.Error())
		return
	}

	submittedMap := make(map[string]bool)
	for _, sub := range latestSubs {
		submittedMap[sub.StudentID] = true
	}

	allStudents := rule.Roster.All()
	var missing []any
	for _, st := range allStudents {
		if !submittedMap[st.StudentID] {
			missing = append(missing, map[string]string{
				"student_id": st.StudentID,
				"name":       st.Name,
				"class_name": st.ClassName,
				"gender":     st.Gender,
			})
		}
	}

	resp := map[string]any{
		"assignment_id":   rule.ID,
		"assignment_name": rule.Name,
		"missing_count":   len(missing),
		"missing_list":    missing,
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAssignmentSubmissions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	latestSubs, err := s.database.GetLatestSubmissions(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query submissions: "+err.Error())
		return
	}

	if latestSubs == nil {
		latestSubs = []*db.Submission{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"assignment_id": id,
		"count":         len(latestSubs),
		"submissions":   latestSubs,
	})
}

func (s *Server) handleStudentSubmissionHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	studentID := chi.URLParam(r, "student_id")

	history, err := s.database.GetStudentSubmissionHistory(id, studentID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query history: "+err.Error())
		return
	}

	if history == nil {
		history = []*db.Submission{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"assignment_id": id,
		"student_id":    studentID,
		"total_version": len(history),
		"history":       history,
	})
}

func (s *Server) handleAssignmentExportZip(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	latestSubs, err := s.database.GetLatestSubmissions(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query submissions: "+err.Error())
		return
	}

	zipFilename := fmt.Sprintf("%s-submissions.zip", id)
	if s.rules != nil {
		if rule, ok := s.rules.GetRule(id); ok && rule.Name != "" {
			zipFilename = fmt.Sprintf("%s_全员作业.zip", rule.Name)
		}
	}
	encodedFilename := url.PathEscape(zipFilename)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", zipFilename, encodedFilename))

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, sub := range latestSubs {
		absPath, err := s.storage.ResolveAbsolutePath(sub.StoragePath)
		if err != nil {
			continue
		}

		className := roster.NormalizeClassName(sub.ClassName)

		studentFolder := fmt.Sprintf("%s-%s", sub.StudentID, sub.StudentName)
		var targetDir string
		if className != "" {
			targetDir = fmt.Sprintf("%s/%s", className, studentFolder)
		} else {
			targetDir = studentFolder
		}

		fallbackName := sub.TargetFilename
		if fallbackName == "" {
			fallbackName = fmt.Sprintf("%s-%s.zip", sub.StudentID, sub.StudentName)
		}

		_ = writeSubmissionFilesToZip(zw, absPath, targetDir, fallbackName)
	}
}

// [StudentSubmissionDownload]
func (s *Server) handleStudentSubmissionDownload(w http.ResponseWriter, r *http.Request) {
	assignmentID := chi.URLParam(r, "id")
	studentID := chi.URLParam(r, "student_id")

	sub, err := s.database.GetLatestSubmissionForStudent(assignmentID, studentID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "submission not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to query submission: "+err.Error())
		return
	}

	absPath, err := s.storage.ResolveAbsolutePath(sub.StoragePath)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, "access denied: invalid storage path")
		return
	}

	file, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "file not found on disk")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to stat file")
		return
	}

	filename := sub.TargetFilename
	if filename == "" {
		filename = fmt.Sprintf("%s-%s.zip", sub.StudentID, sub.StudentName)
	}

	encodedFilename := url.PathEscape(filename)
	disposition := fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", filename, encodedFilename)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Type", "application/octet-stream")

	http.ServeContent(w, r, filename, fi.ModTime(), file)
}

// [SemesterExportZip]
func (s *Server) handleSemesterExportZip(w http.ResponseWriter, r *http.Request) {
	allSubs, err := s.database.GetAllLatestSubmissions()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query submissions: "+err.Error())
		return
	}

	zipFilename := fmt.Sprintf("整学期全量作业归档_%s.zip", time.Now().Format("20060102"))
	encodedFilename := url.PathEscape(zipFilename)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", zipFilename, encodedFilename))

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, sub := range allSubs {
		absPath, err := s.storage.ResolveAbsolutePath(sub.StoragePath)
		if err != nil {
			continue
		}

		dirName := sub.AssignmentID
		if s.rules != nil {
			if rule, ok := s.rules.GetRule(sub.AssignmentID); ok && rule.Name != "" {
				dirName = rule.Name
			}
		}
		dirName = strings.ReplaceAll(dirName, "/", "_")

		className := roster.NormalizeClassName(sub.ClassName)

		studentFolder := fmt.Sprintf("%s-%s", sub.StudentID, sub.StudentName)
		var targetDir string
		if className != "" {
			targetDir = fmt.Sprintf("%s/%s/%s", dirName, className, studentFolder)
		} else {
			targetDir = fmt.Sprintf("%s/%s", dirName, studentFolder)
		}

		fallbackName := sub.TargetFilename
		if fallbackName == "" {
			fallbackName = fmt.Sprintf("%s-%s.zip", sub.StudentID, sub.StudentName)
		}

		_ = writeSubmissionFilesToZip(zw, absPath, targetDir, fallbackName)
	}
}

// [StudentAllAssignmentsExportZip]
func (s *Server) handleStudentAllAssignmentsExportZip(w http.ResponseWriter, r *http.Request) {
	studentID := chi.URLParam(r, "student_id")
	subs, err := s.database.GetAllLatestSubmissionsForStudent(studentID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query submissions: "+err.Error())
		return
	}

	if len(subs) == 0 {
		writeJSONError(w, http.StatusNotFound, "no submissions found for student")
		return
	}

	studentName := subs[0].StudentName
	if studentName == "" {
		studentName = studentID
	}

	zipFilename := fmt.Sprintf("%s_%s_全部作业.zip", studentID, studentName)
	encodedFilename := url.PathEscape(zipFilename)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", zipFilename, encodedFilename))

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, sub := range subs {
		absPath, err := s.storage.ResolveAbsolutePath(sub.StoragePath)
		if err != nil {
			continue
		}

		dirName := sub.AssignmentID
		if s.rules != nil {
			if rule, ok := s.rules.GetRule(sub.AssignmentID); ok && rule.Name != "" {
				dirName = rule.Name
			}
		}
		dirName = strings.ReplaceAll(dirName, "/", "_")
		targetDir := dirName

		fallbackName := sub.TargetFilename
		if fallbackName == "" {
			fallbackName = fmt.Sprintf("%s_%s_%s.zip", sub.AssignmentID, sub.StudentID, sub.StudentName)
		}

		_ = writeSubmissionFilesToZip(zw, absPath, targetDir, fallbackName)
	}
}

// [ExportZipHelpers]
// writeSubmissionFilesToZip extracts inner files if the submission is a zip/tar archive,
// placing each file under studentTargetFolder/<inner_file>.
// If it is not an archive or extraction fails, it places the raw file under studentTargetFolder/<fallbackFilename>.
func writeSubmissionFilesToZip(zw *zip.Writer, absPath string, studentTargetFolder string, fallbackFilename string) error {
	fi, err := os.Stat(absPath)
	if err != nil {
		return err
	}

	lower := strings.ToLower(absPath)

	// 1. Try extracting zip
	if strings.HasSuffix(lower, ".zip") {
		zr, err := zip.OpenReader(absPath)
		if err == nil {
			defer zr.Close()
			commonPrefix := detectCommonDirPrefix(zr.File)
			extractedCount := 0

			for _, f := range zr.File {
				if f.FileInfo().IsDir() {
					continue
				}
				cleanName := filepath.ToSlash(decodeZipFilename(f))
				if verifier.IsIgnoredPath(cleanName) {
					continue
				}
				if commonPrefix != "" && strings.HasPrefix(cleanName, commonPrefix) {
					cleanName = strings.TrimPrefix(cleanName, commonPrefix)
				}
				cleanName = strings.TrimLeft(cleanName, "/")
				if cleanName == "" {
					continue
				}

				cleanName = filepath.Clean(cleanName)
				if strings.HasPrefix(cleanName, "..") {
					continue
				}

				rc, err := f.Open()
				if err != nil {
					continue
				}

				entryPath := fmt.Sprintf("%s/%s", studentTargetFolder, cleanName)
				header := &zip.FileHeader{
					Name:   entryPath,
					Method: zip.Deflate,
				}
				header.Flags |= 0x800
				header.SetModTime(f.Modified)

				fw, err := zw.CreateHeader(header)
				if err == nil {
					_, _ = io.Copy(fw, rc)
					extractedCount++
				}
				rc.Close()
			}

			if extractedCount > 0 {
				return nil
			}
		}
	}

	// 2. Try extracting tar.gz / tgz / tar
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar") {
		extractedCount, err := extractTarToZip(zw, absPath, studentTargetFolder)
		if err == nil && extractedCount > 0 {
			return nil
		}
	}

	// 3. Fallback: write the raw file into the student folder
	file, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer file.Close()

	if fallbackFilename == "" {
		fallbackFilename = filepath.Base(absPath)
	}

	entryPath := fmt.Sprintf("%s/%s", studentTargetFolder, fallbackFilename)
	header := &zip.FileHeader{
		Name:   entryPath,
		Method: zip.Deflate,
	}
	header.Flags |= 0x800
	header.SetModTime(fi.ModTime())

	fw, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(fw, file)
	return err
}

func decodeZipFilename(f *zip.File) string {
	name := f.Name
	if (f.Flags&0x800 != 0) && utf8.ValidString(name) {
		return name
	}
	if utf8.ValidString(name) {
		return name
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().String(name)
	if err == nil && utf8.ValidString(decoded) {
		return decoded
	}
	return name
}

func detectCommonDirPrefix(files []*zip.File) string {
	var prefix string
	first := true
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(decodeZipFilename(f))
		if verifier.IsIgnoredPath(name) {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) <= 1 {
			return ""
		}
		topDir := parts[0] + "/"
		if first {
			prefix = topDir
			first = false
		} else if prefix != topDir {
			return ""
		}
	}
	return prefix
}

func extractTarToZip(zw *zip.Writer, absPath string, studentTargetFolder string) (int, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var r io.Reader = file
	lower := strings.ToLower(absPath)
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
		gzr, err := gzip.NewReader(file)
		if err != nil {
			return 0, err
		}
		defer gzr.Close()
		r = gzr
	}

	tr := tar.NewReader(r)
	extractedCount := 0

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}

		cleanName := filepath.ToSlash(hdr.Name)
		if verifier.IsIgnoredPath(cleanName) {
			continue
		}
		cleanName = strings.TrimLeft(cleanName, "/")
		cleanName = filepath.Clean(cleanName)
		if strings.HasPrefix(cleanName, "..") || cleanName == "." || cleanName == "" {
			continue
		}

		entryPath := fmt.Sprintf("%s/%s", studentTargetFolder, cleanName)
		header := &zip.FileHeader{
			Name:   entryPath,
			Method: zip.Deflate,
		}
		header.Flags |= 0x800
		header.SetModTime(hdr.ModTime)

		fw, err := zw.CreateHeader(header)
		if err != nil {
			continue
		}
		_, _ = io.Copy(fw, tr)
		extractedCount++
	}

	return extractedCount, nil
}

// [StudentBindingsHandlers]
func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	list, err := s.database.ListStudentBindings()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list bindings: "+err.Error())
		return
	}
	if list == nil {
		list = []*db.StudentBinding{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bindings": list,
		"total":    len(list),
	})
}

func (s *Server) handleGetRoster(w http.ResponseWriter, r *http.Request) {
	students, err := s.courseStudents()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "course roster unavailable")
		return
	}
	classes := make(map[string]int)
	for _, student := range students {
		classes[student.ClassName]++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"students": students,
		"total":    len(students),
		"classes":  classes,
	})
}

func (s *Server) handleGetBinding(w http.ResponseWriter, r *http.Request) {
	qqID := chi.URLParam(r, "qq_id")
	b, err := s.database.GetStudentBindingByQQ(qqID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "binding not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to get binding: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b)
}

type createBindingRequest struct {
	QQID        string `json:"qq_id"`
	StudentID   string `json:"student_id"`
	StudentName string `json:"student_name"`
	ClassName   string `json:"class_name"`
	Force       bool   `json:"force"`
}

func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req createBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	req.QQID = strings.TrimSpace(req.QQID)
	req.StudentID = strings.TrimSpace(req.StudentID)
	req.StudentName = strings.TrimSpace(req.StudentName)
	req.ClassName = strings.TrimSpace(req.ClassName)

	if req.QQID == "" || req.StudentID == "" || req.StudentName == "" {
		writeJSONError(w, http.StatusBadRequest, "qq_id, student_id and student_name are required")
		return
	}

	// 姓名和学号不是身份凭据。共享 API key 的持有者负责线下核验，force 表示人工批准。
	if !req.Force {
		writeJSONError(w, http.StatusForbidden, "binding requires staff approval")
		return
	}
	if s.rules == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "roster unavailable")
		return
	}

	// Cross-check with roster from all loaded assignment rules
	var foundName, foundClass string
	var rosterFound bool

	for _, rule := range s.rules.Rules() {
		if student, ok := rule.Roster.FindByID(req.StudentID); ok {
			foundName = student.Name
			foundClass = student.ClassName
			rosterFound = true
			break
		}
	}

	if !rosterFound {
		writeJSONError(w, http.StatusBadRequest, "花名册中未检索到该学号，无法完成身份绑定")
		return
	}
	if req.StudentName != foundName {
		writeJSONError(w, http.StatusBadRequest, "学号或姓名不匹配")
		return
	}
	req.StudentName = foundName
	req.ClassName = roster.NormalizeClassName(foundClass)

	binding := &db.StudentBinding{
		QQID:        req.QQID,
		StudentID:   req.StudentID,
		StudentName: req.StudentName,
		ClassName:   req.ClassName,
	}

	if err := s.database.UpsertStudentBinding(binding); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save binding: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"binding": binding,
	})
}

func (s *Server) handleDeleteBinding(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "qq_id")
	err := s.database.DeleteStudentBinding(idParam)
	if err != nil && errors.Is(err, db.ErrNotFound) {
		if existing, err2 := s.database.GetStudentBindingByStudentID(idParam); err2 == nil && existing != nil {
			err = s.database.DeleteStudentBinding(existing.QQID)
		}
	}
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "binding not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to delete binding: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// [AssignmentUploadHandler]
func (s *Server) handleAssignmentUpload(w http.ResponseWriter, r *http.Request) {
	// Limit request body to 100MB
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to parse multipart form (max 100MB): "+err.Error())
		return
	}

	assignmentID := chi.URLParam(r, "id")
	var targetRule *rule.AssignmentRule
	if assignmentID == "latest" || assignmentID == "current" || assignmentID == "" {
		targetRule = s.rules.LatestRule()
	} else {
		targetRule, _ = s.rules.GetRule(assignmentID)
	}

	if targetRule == nil {
		writeJSONError(w, http.StatusNotFound, "assignment not found or no active assignments")
		return
	}

	studentID := strings.TrimSpace(r.FormValue("student_id"))
	studentName := strings.TrimSpace(r.FormValue("student_name"))
	className := strings.TrimSpace(r.FormValue("class_name"))
	qqID := strings.TrimSpace(r.FormValue("qq_id"))
	uploader := strings.TrimSpace(r.FormValue("uploader"))

	if uploader == "" && qqID != "" {
		uploader = "qq:" + qqID
	}

	// Auto fill from binding if qq_id is provided and student_id is omitted
	if studentID == "" && qqID != "" {
		if b, err := s.database.GetStudentBindingByQQ(qqID); err == nil {
			studentID = b.StudentID
			if studentName == "" {
				studentName = b.StudentName
			}
			if className == "" {
				className = b.ClassName
			}
		}
	}

	if studentID == "" && studentName == "" {
		writeJSONError(w, http.StatusBadRequest, "student_id or bound qq_id is required")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing or invalid file field in form")
		return
	}
	defer file.Close()

	now := time.Now().UTC()
	relPath, hashHex, fileSize, err := s.storage.Save(file, header.Filename, now)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save file: "+err.Error())
		return
	}

	// Normalize student identity and formatted target filename
	finalID, finalName, finalClass, targetFilename, isLate, normErr := targetRule.NormalizeSubmission(
		studentID, studentName, className, header.Filename, now,
	)
	if normErr != nil {
		writeJSONError(w, http.StatusBadRequest, "student validation error: "+normErr.Error())
		return
	}

	absPath, err := s.storage.ResolveAbsolutePath(relPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to resolve file path: "+err.Error())
		return
	}

	// Extract internal files for the plagiarism index. Similarity is reviewed by
	// staff and must not prevent a valid student submission from being received.
	extractedFiles, extractErr := verifier.ExtractArchiveFiles(absPath)

	// Record attachment
	att := &db.Attachment{
		MessageID:   fmt.Sprintf("direct-upload-%d-%s", now.UnixNano(), hashHex[:8]),
		Sender:      uploader,
		Subject:     targetRule.Name,
		ReceivedAt:  now,
		Filename:    header.Filename,
		FileSize:    fileSize,
		SHA256:      hashHex,
		MIMEType:    header.Header.Get("Content-Type"),
		StoragePath: relPath,
	}

	attID, err := s.database.InsertAttachment(att)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to record attachment: "+err.Error())
		return
	}

	sub := &db.Submission{
		AssignmentID:   targetRule.ID,
		StudentID:      finalID,
		StudentName:    finalName,
		ClassName:      finalClass,
		AttachmentID:   attID,
		SubmittedAt:    now,
		IsLate:         isLate,
		TargetFilename: targetFilename,
	}

	version, isUpdate, err := s.database.RecordSubmission(sub)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to record submission: "+err.Error())
		return
	}

	// Persist extracted file hashes, then warm the materialized report. The
	// report can also be rebuilt lazily after mail imports or a failed warmup.
	indexReady := extractErr == nil
	category := "extraction_failed"
	if indexReady {
		if err := s.database.InsertSubmissionFiles(sub.ID, targetRule.ID, finalID, verifier.ToDBFiles(extractedFiles)); err != nil {
			indexReady = false
			category = "index_write_failed"
			log.Print("[api] plagiarism file index update failed after submission was saved")
		}
	}
	if indexReady {
		if err := s.database.SetPlagiarismIndexProgress(sub.ID, "complete", ""); err != nil {
			indexReady = false
			category = "progress_write_failed"
		}
	} else {
		log.Print("[api] plagiarism extraction failed after submission was saved")
	}
	if !indexReady && category != "progress_write_failed" {
		_ = s.database.SetPlagiarismIndexProgress(sub.ID, "failed", category)
	}
	if indexReady {
		if _, err := s.database.GetMaterializedPlagiarismReport(targetRule.ID); err != nil {
			indexReady = false
			log.Print("[api] plagiarism report update failed after submission was saved")
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":            true,
		"assignment_id":      targetRule.ID,
		"assignment_name":    targetRule.Name,
		"student_id":         finalID,
		"student_name":       finalName,
		"class_name":         finalClass,
		"target_filename":    targetFilename,
		"version":            version,
		"is_update":          isUpdate,
		"is_late":            isLate,
		"file_size":          fileSize,
		"sha256":             hashHex,
		"plagiarism_indexed": indexReady,
		"submitted_at":       now.Format(time.RFC3339),
	})
}

// [AssignmentPlagiarismHandler]
func (s *Server) handleAssignmentPlagiarism(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var targetRule *rule.AssignmentRule
	if id == "latest" || id == "current" || id == "" {
		targetRule = s.rules.LatestRule()
	} else {
		targetRule, _ = s.rules.GetRule(id)
	}

	if targetRule == nil {
		writeJSONError(w, http.StatusNotFound, "assignment rule not found")
		return
	}
	progress, err := s.database.AssignmentPlagiarismProgress(targetRule.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query plagiarism progress")
		return
	}
	if progress.Processing > 0 || progress.Failed > 0 {
		status := http.StatusAccepted
		state := "processing"
		if progress.Failed > 0 {
			status = http.StatusServiceUnavailable
			state = "failed"
		}
		writeJSON(w, status, map[string]any{"assignment_id": targetRule.ID, "index_status": state, "progress": progress})
		return
	}

	report, err := s.database.GetMaterializedPlagiarismReport(targetRule.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query plagiarism report: "+err.Error())
		return
	}

	if report == nil {
		report = []*db.PlagiarismPair{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"assignment_id":   targetRule.ID,
		"assignment_name": targetRule.Name,
		"index_status":    "complete",
		"progress":        progress,
		"total_pairs":     len(report),
		"pairs":           report,
	})
}

// [CreateAssignmentHandler]
func (s *Server) handleCreateAssignment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var raw rule.RuleFileConfig
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if raw.ID == "" || raw.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "id and name are required")
		return
	}

	if len(raw.Rosters) == 0 {
		writeJSONError(w, http.StatusBadRequest, "rosters must be explicitly configured")
		return
	}
	if raw.TargetFilename == "" {
		raw.TargetFilename = "作业-{class}-{student_id}-{name}.{ext}"
	}

	rulesDir := s.cfg.RulesDir
	if rulesDir == "" {
		rulesDir = filepath.Join(s.cfg.DataDir, "rules")
	}

	createdRule, err := s.rules.SaveAndLoadRule(raw, rulesDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save and load rule: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"id":           createdRule.ID,
		"name":         createdRule.Name,
		"deadline":     createdRule.Deadline.Format(time.RFC3339),
		"roster_count": createdRule.Roster.Count(),
	})
}
