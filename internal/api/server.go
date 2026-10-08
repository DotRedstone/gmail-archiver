package api

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/imap"
	"github.com/dot/gmail-archiver/internal/rule"
	"github.com/dot/gmail-archiver/internal/storage"
)

// [Server]
type Server struct {
	cfg       *config.Config
	database  *db.DB
	storage   *storage.Engine
	watcher   *imap.Watcher
	rules     *rule.Engine
	startTime time.Time
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
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Health check and documentation (public)
	r.Get("/", s.handleDocs)
	r.Get("/docs", s.handleDocs)
	r.Get("/health", s.handleHealth)

	// Protected routes
	r.Group(func(api chi.Router) {
		if s.cfg.APIKey != "" {
			api.Use(s.apiKeyMiddleware)
		}

		api.Route("/api/attachments", func(att chi.Router) {
			att.Get("/", s.handleListAttachments)
			att.Get("/{id}/download", s.handleDownloadAttachment)
		})

		api.Route("/api/assignments", func(as chi.Router) {
			as.Get("/", s.handleListAssignments)
			as.Get("/export/all", s.handleSemesterExportZip)
			as.Get("/{id}/status", s.handleAssignmentStatus)
			as.Get("/{id}/missing", s.handleAssignmentMissing)
			as.Get("/{id}/submissions", s.handleAssignmentSubmissions)
			as.Get("/{id}/submissions/{student_id}/download", s.handleStudentSubmissionDownload)
			as.Get("/{id}/submissions/{student_id}/history", s.handleStudentSubmissionHistory)
			as.Get("/{id}/export", s.handleAssignmentExportZip)
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
			next.ServeHTTP(w, r)
			return
		}

		token := r.Header.Get("X-API-Key")
		if token == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				token = strings.TrimSpace(authHeader[7:])
			}
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			token = r.URL.Query().Get("api_key")
		}

		if token == "" || token != s.cfg.APIKey {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized: invalid or missing API key")
			return
		}

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

		file, err := os.Open(absPath)
		if err != nil {
			continue
		}

		entryName := sub.TargetFilename
		if entryName == "" {
			entryName = fmt.Sprintf("%s-%s.zip", sub.StudentID, sub.StudentName)
		}

		fi, statErr := file.Stat()
		header := &zip.FileHeader{
			Name:   entryName,
			Method: zip.Deflate,
		}
		header.Flags |= 0x800 // UTF-8 filename flag
		if statErr == nil {
			header.SetModTime(fi.ModTime())
		}

		fw, err := zw.CreateHeader(header)
		if err != nil {
			file.Close()
			continue
		}

		_, _ = io.Copy(fw, file)
		file.Close()
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

		file, err := os.Open(absPath)
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

		entryName := sub.TargetFilename
		if entryName == "" {
			entryName = fmt.Sprintf("%s-%s.zip", sub.StudentID, sub.StudentName)
		}

		fullEntryPath := fmt.Sprintf("%s/%s", dirName, entryName)

		fi, statErr := file.Stat()
		header := &zip.FileHeader{
			Name:   fullEntryPath,
			Method: zip.Deflate,
		}
		header.Flags |= 0x800 // UTF-8 filename flag
		if statErr == nil {
			header.SetModTime(fi.ModTime())
		}

		fw, err := zw.CreateHeader(header)
		if err != nil {
			file.Close()
			continue
		}

		_, _ = io.Copy(fw, file)
		file.Close()
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

	seenNames := make(map[string]int)
	for _, sub := range subs {
		absPath, err := s.storage.ResolveAbsolutePath(sub.StoragePath)
		if err != nil {
			continue
		}

		file, err := os.Open(absPath)
		if err != nil {
			continue
		}

		entryName := sub.TargetFilename
		if entryName == "" {
			entryName = fmt.Sprintf("%s_%s_%s.zip", sub.AssignmentID, sub.StudentID, sub.StudentName)
		}

		if seenNames[entryName] > 0 {
			entryName = fmt.Sprintf("%s_%s", sub.AssignmentID, entryName)
		}
		seenNames[entryName]++

		fi, statErr := file.Stat()
		header := &zip.FileHeader{
			Name:   entryName,
			Method: zip.Deflate,
		}
		header.Flags |= 0x800 // UTF-8 filename flag
		if statErr == nil {
			header.SetModTime(fi.ModTime())
		}

		fw, err := zw.CreateHeader(header)
		if err != nil {
			file.Close()
			continue
		}

		_, _ = io.Copy(fw, file)
		file.Close()
	}
}
