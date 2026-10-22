package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dot/gmail-archiver/internal/db"
)

func (s *Server) handleDuePublicationJobs(w http.ResponseWriter, r *http.Request) {
	platform, bot := r.URL.Query().Get("platform_id"), r.URL.Query().Get("bot_id")
	if !externalIDPattern.MatchString(platform) || !externalIDPattern.MatchString(bot) {
		writeJSONError(w, http.StatusBadRequest, "invalid platform or bot identity")
		return
	}
	// Coordinate with confirmation/projection repair in this process.
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	jobs, err := s.database.DuePublicationJobs(platform, bot, time.Now().UTC())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read publication jobs")
		return
	}
	ready := make([]*db.PublicationJob, 0, len(jobs))
	for _, job := range jobs {
		card, err := s.database.GetLabCardDraftByID(job.DraftID)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "publication state unavailable")
			return
		}
		if s.publicationProjectionReady(card) {
			ready = append(ready, job)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": ready})
}

func (s *Server) handleClaimPublicationJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlatformID string `json:"platform_id"`
		BotID      string `json:"bot_id"`
	}
	if !decodeIdentityBody(w, r, &req) {
		return
	}
	if !externalIDPattern.MatchString(req.PlatformID) || !externalIDPattern.MatchString(req.BotID) {
		writeJSONError(w, http.StatusBadRequest, "invalid platform or bot identity")
		return
	}
	id := chi.URLParam(r, "job_id")
	if !externalIDPattern.MatchString(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	// Recheck the durable projection at claim time; a job listed before
	// a restart or a lost rule file must never become externally sendable.
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	current, err := s.database.GetPublicationJob(id)
	if err == nil && current.PlatformID == req.PlatformID && current.BotID == req.BotID {
		card, cardErr := s.database.GetLabCardDraftByID(current.DraftID)
		if cardErr != nil || !s.publicationProjectionReady(card) {
			writeJSONError(w, http.StatusServiceUnavailable, "assignment projection unavailable")
			return
		}
	}
	job, err := s.database.ClaimPublicationJob(id, req.PlatformID, req.BotID, time.Now().UTC())
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusConflict, "job is not pending for this bot")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to claim job")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleRecordPublicationJobResult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlatformID      string `json:"platform_id"`
		BotID           string `json:"bot_id"`
		Status          string `json:"status"`
		MessageID       string `json:"message_id"`
		FailureCategory string `json:"failure_category"`
	}
	if !decodeIdentityBody(w, r, &req) {
		return
	}
	validStatus := req.Status == "sent" || req.Status == "failed" || req.Status == "unknown" ||
		req.Status == "skipped" || req.Status == "expired" || req.Status == "manual_pending"
	validCategory := false
	switch req.FailureCategory {
	case "platform_rejected", "quota", "timeout", "interrupted", "missing_receipt", "group_disabled",
		"already_complete", "admins_only_missing", "roster_unavailable", "dependency_unknown", "deadline_passed", "other":
		validCategory = true
	}
	if !externalIDPattern.MatchString(req.PlatformID) || !externalIDPattern.MatchString(req.BotID) || !validStatus ||
		(req.Status == "sent" && (req.MessageID == "" || len(req.MessageID) > 256)) ||
		(req.Status != "sent" && req.MessageID != "") ||
		(req.Status != "sent" && !validCategory) ||
		(req.Status == "sent" && req.FailureCategory != "") {
		writeJSONError(w, http.StatusBadRequest, "invalid delivery result")
		return
	}
	id := chi.URLParam(r, "job_id")
	if !externalIDPattern.MatchString(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := s.database.RecordPublicationResult(id, req.PlatformID, req.BotID, req.Status, req.MessageID, req.FailureCategory, time.Now().UTC())
	if errors.Is(err, db.ErrNotFound) {
		prior, priorErr := s.database.GetPublicationJob(id)
		if priorErr == nil && prior.PlatformID == req.PlatformID && prior.BotID == req.BotID && prior.Status == req.Status &&
			prior.MessageID == req.MessageID && prior.FailureCategory == req.FailureCategory {
			writeJSON(w, http.StatusOK, prior)
			return
		}
		writeJSONError(w, http.StatusConflict, "job result conflicts with recorded state")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save job result")
		return
	}
	writeJSON(w, http.StatusOK, job)
}
