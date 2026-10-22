package api

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/roster"
)

var externalIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type identityKey struct {
	PlatformID     string `json:"platform_id"`
	BotID          string `json:"bot_id"`
	ExternalUserID string `json:"external_user_id"`
}

type bindingIntentRequest struct {
	identityKey
	StudentID   string `json:"student_id"`
	StudentName string `json:"student_name"`
}

func (k identityKey) valid() bool {
	return externalIDPattern.MatchString(k.PlatformID) &&
		externalIDPattern.MatchString(k.BotID) && externalIDPattern.MatchString(k.ExternalUserID)
}

func decodeIdentityBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func (s *Server) rosterStudent(studentID, name string) (*roster.Student, bool, error) {
	students, err := s.courseStudents()
	if err != nil {
		return nil, false, err
	}
	for _, student := range students {
		if student.StudentID == studentID {
			return &student, student.Name == name, nil
		}
	}
	return nil, false, nil
}

func (s *Server) handleCreateBindingIntent(w http.ResponseWriter, r *http.Request) {
	var req bindingIntentRequest
	if !decodeIdentityBody(w, r, &req) {
		return
	}
	req.StudentID = strings.TrimSpace(req.StudentID)
	req.StudentName = strings.TrimSpace(req.StudentName)
	if !req.valid() || len(req.StudentID) != 12 || !isASCIIDigits(req.StudentID) || req.StudentName == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid identity or student fields")
		return
	}
	student, ok, rosterErr := s.rosterStudent(req.StudentID, req.StudentName)
	if rosterErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "course roster unavailable")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "student identity does not match roster")
		return
	}
	intent := db.BindingIntent{
		ExternalBinding: db.ExternalBinding{
			PlatformID: req.PlatformID, BotID: req.BotID, ExternalUserID: req.ExternalUserID,
			StudentID: student.StudentID, StudentName: student.Name, ClassName: student.ClassName,
		},
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}
	if err := s.database.CreateBindingIntent(intent); err != nil {
		if errors.Is(err, db.ErrBindingConflict) {
			writeJSONError(w, http.StatusConflict, "identity already bound")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to create binding intent")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"student_id": intent.StudentID, "student_name": intent.StudentName,
		"class_name": intent.ClassName, "expires_at": intent.ExpiresAt,
	})
}

func (s *Server) handleGetBindingIntent(w http.ResponseWriter, r *http.Request) {
	key := identityKey{r.URL.Query().Get("platform_id"), r.URL.Query().Get("bot_id"), r.URL.Query().Get("external_user_id")}
	if !key.valid() {
		writeJSONError(w, http.StatusBadRequest, "invalid identity")
		return
	}
	intent, err := s.database.GetBindingIntent(key.PlatformID, key.BotID, key.ExternalUserID)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no pending binding")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to get binding intent")
		return
	}
	if !time.Now().Before(intent.ExpiresAt) {
		_ = s.database.CancelBindingIntentIfMatches(*intent)
		writeJSONError(w, http.StatusGone, "binding intent expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"student_id": intent.StudentID, "student_name": intent.StudentName, "expires_at": intent.ExpiresAt})
}

func (s *Server) handleConfirmBindingIntent(w http.ResponseWriter, r *http.Request) {
	var key identityKey
	if !decodeIdentityBody(w, r, &key) {
		return
	}
	if !key.valid() {
		writeJSONError(w, http.StatusBadRequest, "invalid identity")
		return
	}
	intent, err := s.database.GetBindingIntent(key.PlatformID, key.BotID, key.ExternalUserID)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no pending binding")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to get binding intent")
		return
	}
	student, ok, rosterErr := s.rosterStudent(intent.StudentID, intent.StudentName)
	if rosterErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "course roster unavailable")
		return
	}
	if !ok || student.ClassName != intent.ClassName {
		_ = s.database.CancelBindingIntentIfMatches(*intent)
		writeJSONError(w, http.StatusConflict, "student identity no longer matches roster")
		return
	}
	binding, err := s.database.ConfirmMatchingBindingIntent(*intent, time.Now().UTC())
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "no pending binding")
	case errors.Is(err, db.ErrIntentExpired):
		writeJSONError(w, http.StatusGone, "binding intent expired")
	case errors.Is(err, db.ErrBindingConflict):
		writeJSONError(w, http.StatusConflict, "identity already bound")
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, "failed to confirm binding")
	default:
		writeJSON(w, http.StatusOK, binding)
	}
}

func (s *Server) handleCancelBindingIntent(w http.ResponseWriter, r *http.Request) {
	var key identityKey
	if !decodeIdentityBody(w, r, &key) {
		return
	}
	if !key.valid() {
		writeJSONError(w, http.StatusBadRequest, "invalid identity")
		return
	}
	if err := s.database.CancelBindingIntent(key.PlatformID, key.BotID, key.ExternalUserID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel binding")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) handleListExternalBindings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	studentID := q.Get("student_id")
	platformID, botID, userID := q.Get("platform_id"), q.Get("bot_id"), q.Get("external_user_id")
	var binding *db.ExternalBinding
	var err error
	switch {
	case studentID == "" && externalIDPattern.MatchString(platformID) && externalIDPattern.MatchString(botID) && userID == "":
		list, err := s.database.ListExternalBindingsForBot(platformID, botID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list bindings")
			return
		}
		writeJSON(w, http.StatusOK, list)
		return
	case studentID != "" && platformID == "" && botID == "" && userID == "":
		binding, err = s.database.GetExternalBindingByStudent(studentID)
	case studentID == "" && (identityKey{platformID, botID, userID}).valid():
		binding, err = s.database.GetExternalBinding(platformID, botID, userID)
	case studentID == "" && platformID == "" && botID == "" && userID == "":
		list, err := s.database.ListExternalBindings()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list bindings")
			return
		}
		writeJSON(w, http.StatusOK, list)
		return
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid lookup")
		return
	}
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "binding not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to get binding")
		return
	}
	writeJSON(w, http.StatusOK, binding)
}

func (s *Server) handleDeleteExternalBinding(w http.ResponseWriter, r *http.Request) {
	studentID := chi.URLParam(r, "student_id")
	if len(studentID) != 12 || !isASCIIDigits(studentID) {
		writeJSONError(w, http.StatusBadRequest, "invalid student id")
		return
	}
	if err := s.database.DeleteExternalBinding(studentID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "binding not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to delete binding")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) handleVerifyExternalBinding(w http.ResponseWriter, r *http.Request) {
	studentID := chi.URLParam(r, "student_id")
	var req struct {
		identityKey
		StudentName string `json:"student_name"`
		VerifiedBy  string `json:"verified_by"`
	}
	if !decodeIdentityBody(w, r, &req) {
		return
	}
	if len(studentID) != 12 || !isASCIIDigits(studentID) || !req.valid() || !externalIDPattern.MatchString(req.VerifiedBy) {
		writeJSONError(w, http.StatusBadRequest, "invalid verification fields")
		return
	}
	student, ok, rosterErr := s.rosterStudent(studentID, strings.TrimSpace(req.StudentName))
	if rosterErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "course roster unavailable")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "student identity does not match roster")
		return
	}
	bound, err := s.database.GetExternalBindingByStudent(student.StudentID)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "binding not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to get binding")
		return
	}
	if bound.PlatformID != req.PlatformID || bound.BotID != req.BotID || bound.ExternalUserID != req.ExternalUserID {
		writeJSONError(w, http.StatusConflict, "binding identity changed")
		return
	}
	verified, err := s.database.VerifyExternalBinding(studentID, req.PlatformID, req.BotID, req.ExternalUserID, req.VerifiedBy, time.Now().UTC())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to verify binding")
		return
	}
	writeJSON(w, http.StatusOK, verified)
}

func isASCIIDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func (s *Server) handleExternalAssignmentUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid upload form")
		return
	}
	defer r.MultipartForm.RemoveAll()
	key := identityKey{
		PlatformID: r.FormValue("platform_id"), BotID: r.FormValue("bot_id"),
		ExternalUserID: r.FormValue("external_user_id"),
	}
	if !key.valid() {
		writeJSONError(w, http.StatusBadRequest, "invalid identity")
		return
	}
	binding, err := s.database.GetExternalBinding(key.PlatformID, key.BotID, key.ExternalUserID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && binding.Source != "self_asserted" && binding.Source != "staff_verified") {
		writeJSONError(w, http.StatusForbidden, "binding required")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check binding")
		return
	}
	assignmentID := chi.URLParam(r, "id")
	if s.rules == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "assignment rules unavailable")
		return
	}
	assignment, ok := s.rules.GetRule(assignmentID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "assignment not found")
		return
	}
	student, ok := assignment.Roster.FindByID(binding.StudentID)
	if !ok || student.Name != binding.StudentName || student.ClassName != binding.ClassName {
		writeJSONError(w, http.StatusConflict, "binding no longer matches assignment roster")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing file")
		return
	}
	defer file.Close()
	if strings.ContainsAny(header.Filename, "/\\\r\n") || !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		writeJSONError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	_, _, _, expected, _, err := assignment.NormalizeSubmission(binding.StudentID, binding.StudentName, binding.ClassName, header.Filename, time.Now().UTC())
	if err != nil || header.Filename != expected {
		writeJSONError(w, http.StatusBadRequest, "filename does not match assignment rule")
		return
	}
	if _, err := zip.NewReader(file, header.Size); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid ZIP archive")
		return
	}
	trusted := map[string]string{
		"student_id": binding.StudentID, "student_name": binding.StudentName,
		"class_name": binding.ClassName, "qq_id": "",
		"uploader": "external:" + key.PlatformID + ":" + key.BotID + ":" + key.ExternalUserID,
	}
	for name, value := range trusted {
		r.Form[name] = []string{value}
		r.PostForm[name] = []string{value}
		r.MultipartForm.Value[name] = []string{value}
	}
	s.handleAssignmentUpload(w, r)
}
