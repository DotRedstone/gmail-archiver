package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dot/gmail-archiver/internal/verifier"
)

// Reindex is an explicit administrative repair operation. It processes a
// bounded batch so migration of existing submissions cannot hold one request
// indefinitely; a second call continues with the remaining versions.
func (s *Server) handleReindexAssignmentPlagiarism(w http.ResponseWriter, r *http.Request) {
	s.plagiarismMu.Lock()
	defer s.plagiarismMu.Unlock()
	assignmentID := chi.URLParam(r, "id")
	if s.rules == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "assignment rules unavailable")
		return
	}
	if _, ok := s.rules.GetRule(assignmentID); !ok {
		writeJSONError(w, http.StatusNotFound, "assignment not found")
		return
	}
	work, err := s.database.ListPlagiarismIndexWork(assignmentID, 10)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list plagiarism index work")
		return
	}
	processed := 0
	for _, item := range work {
		if r.Context().Err() != nil {
			break
		}
		if err := s.database.BeginPlagiarismReindex(item.SubmissionID, assignmentID); err != nil {
			continue
		}
		category := "storage_unavailable"
		path, err := s.storage.ResolveAbsolutePath(item.StoragePath)
		if err == nil {
			var files []*verifier.ArchivedFileInfo
			files, err = verifier.ExtractArchiveFiles(path)
			category = "extraction_failed"
			if err == nil {
				err = s.database.InsertSubmissionFiles(item.SubmissionID, assignmentID, item.StudentID, verifier.ToDBFiles(files))
				category = "index_write_failed"
			}
		}
		if err == nil {
			err = s.database.SetPlagiarismIndexProgress(item.SubmissionID, "complete", "")
		} else {
			_ = s.database.SetPlagiarismIndexProgress(item.SubmissionID, "failed", category)
		}
		if err == nil {
			processed++
		}
	}
	progress, err := s.database.AssignmentPlagiarismProgress(assignmentID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read plagiarism progress")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignment_id": assignmentID, "reindexed": processed, "progress": progress})
}
