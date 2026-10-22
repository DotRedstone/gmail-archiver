package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dot/gmail-archiver/internal/db"
)

type ownSubmission struct {
	Version     int       `json:"version"`
	Filename    string    `json:"filename"`
	SubmittedAt time.Time `json:"submitted_at"`
	IsLatest    bool      `json:"is_latest"`
}

func (s *Server) selfAssertedBinding(w http.ResponseWriter, r *http.Request) (*db.ExternalBinding, bool) {
	var key identityKey
	if !decodeIdentityBody(w, r, &key) {
		return nil, false
	}
	if !key.valid() {
		writeJSONError(w, http.StatusBadRequest, "invalid identity")
		return nil, false
	}
	binding, err := s.database.GetExternalBinding(key.PlatformID, key.BotID, key.ExternalUserID)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusForbidden, "binding required")
		return nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check binding")
		return nil, false
	}
	if binding.Source != "self_asserted" && binding.Source != "staff_verified" {
		writeJSONError(w, http.StatusForbidden, "binding required")
		return nil, false
	}
	return binding, true
}

func (s *Server) handleMyAssignments(w http.ResponseWriter, r *http.Request) {
	binding, ok := s.selfAssertedBinding(w, r)
	if !ok {
		return
	}
	if s.rules == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "assignment rules unavailable")
		return
	}
	type item struct {
		ID             string    `json:"assignment_id"`
		Name           string    `json:"assignment_name"`
		Deadline       time.Time `json:"deadline"`
		Submitted      bool      `json:"submitted"`
		LatestFilename string    `json:"latest_filename,omitempty"`
	}
	result := make([]item, 0)
	for _, assignment := range s.rules.Rules() {
		student, expected := assignment.Roster.FindByID(binding.StudentID)
		if !expected || student.Name != binding.StudentName || student.ClassName != binding.ClassName {
			continue
		}
		latest, err := s.database.GetLatestSubmissionForStudent(assignment.ID, binding.StudentID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusInternalServerError, "failed to read submission")
			return
		}
		row := item{ID: assignment.ID, Name: assignment.Name, Deadline: assignment.Deadline}
		if err == nil && latest != nil {
			row.Submitted, row.LatestFilename = true, latest.TargetFilename
		}
		result = append(result, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"student_id": binding.StudentID, "assignments": result})
}

func (s *Server) handleMyAssignment(w http.ResponseWriter, r *http.Request) {
	binding, ok := s.selfAssertedBinding(w, r)
	if !ok {
		return
	}
	if s.rules == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "assignment rules unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	assignment, exists := s.rules.GetRule(id)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "assignment not found")
		return
	}
	student, expected := assignment.Roster.FindByID(binding.StudentID)
	if !expected || student.Name != binding.StudentName || student.ClassName != binding.ClassName {
		writeJSONError(w, http.StatusForbidden, "student is not expected for assignment")
		return
	}
	history, err := s.database.GetStudentSubmissionHistory(id, binding.StudentID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read submission history")
		return
	}
	versions := make([]ownSubmission, 0, len(history))
	for _, sub := range history {
		versions = append(versions, ownSubmission{Version: sub.Version, Filename: sub.TargetFilename,
			SubmittedAt: sub.SubmittedAt, IsLatest: sub.IsLatest})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"assignment_id": id, "assignment_name": assignment.Name, "student_id": binding.StudentID,
		"submitted": len(versions) > 0, "history": versions,
	})
}
