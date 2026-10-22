package api

import (
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/rule"
)

func newPublicationDraftID(number int) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("D%02d-%s", number, hex.EncodeToString(suffix[:])), nil
}

func (s *Server) snapshotPublicationTargets(targets []db.PublicationTarget) ([]db.PublicationTarget, []db.SnapshotStudent, error) {
	if len(targets) == 0 || len(targets) > 20 {
		return nil, nil, errors.New("invalid targets")
	}
	students, err := s.courseStudents()
	if err != nil {
		return nil, nil, errRosterUnavailable
	}
	byClass := make(map[string][]db.SnapshotStudent)
	for _, student := range students {
		byClass[student.ClassName] = append(byClass[student.ClassName], db.SnapshotStudent{
			StudentID: student.StudentID, StudentName: student.Name, ClassName: student.ClassName,
		})
	}
	seenGroups := make(map[string]bool)
	selected := make(map[string]db.SnapshotStudent)
	platform, bot := targets[0].PlatformID, targets[0].BotID
	for index := range targets {
		target := &targets[index]
		if !externalIDPattern.MatchString(target.PlatformID) || !externalIDPattern.MatchString(target.BotID) ||
			!externalIDPattern.MatchString(target.GroupOpenID) || target.PlatformID != platform || target.BotID != bot ||
			seenGroups[target.GroupOpenID] || len(target.ClassNames) == 0 {
			return nil, nil, errors.New("invalid target identity")
		}
		seenGroups[target.GroupOpenID] = true
		seenClasses := make(map[string]bool)
		target.ExpectedCount = 0
		for _, className := range target.ClassNames {
			if seenClasses[className] || len(byClass[className]) == 0 {
				return nil, nil, errors.New("invalid target class")
			}
			seenClasses[className] = true
			target.ExpectedCount += len(byClass[className])
			for _, student := range byClass[className] {
				selected[student.StudentID] = student
			}
		}
		sort.Strings(target.ClassNames)
	}
	snapshot := make([]db.SnapshotStudent, 0, len(selected))
	for _, student := range selected {
		snapshot = append(snapshot, student)
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].StudentID < snapshot[j].StudentID })
	return targets, snapshot, nil
}

func (s *Server) publicationDraftFromPath(w http.ResponseWriter, r *http.Request) (*db.LabCardDraft, bool) {
	id := chi.URLParam(r, "draft_id")
	if len(id) < 6 || len(id) > 40 || !strings.HasPrefix(id, "D") || strings.ContainsAny(id, "/\\") {
		writeJSONError(w, http.StatusBadRequest, "invalid draft id")
		return nil, false
	}
	card, err := s.database.GetLabCardDraftByID(id)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "draft not found")
		return nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read draft")
		return nil, false
	}
	return card, true
}

func (s *Server) handleGetPublicationDraft(w http.ResponseWriter, r *http.Request) {
	card, ok := s.publicationDraftFromPath(w, r)
	if !ok {
		return
	}
	jobs, err := s.database.ListPublicationJobs(card.DraftID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read publication jobs")
		return
	}
	assignmentID := fmt.Sprintf("experiment_%d", card.Number)
	status := "draft"
	if card.Status == "confirmed" {
		status, err = s.database.PublishedAssignmentStatus(assignmentID, card.DraftID)
		if errors.Is(err, db.ErrNotFound) && len(card.Targets) == 0 {
			status, err = "manual_pending", nil
		}
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "publication state unavailable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"card": card, "assignment_id": assignmentID, "publication_status": status, "jobs": jobs})
}

func (s *Server) handleUpdatePublicationDraft(w http.ResponseWriter, r *http.Request) {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	card, ok := s.publicationDraftFromPath(w, r)
	if !ok {
		return
	}
	if card.Status != "draft" {
		writeJSONError(w, http.StatusConflict, "draft already confirmed")
		return
	}
	var req struct {
		Deadline *string                 `json:"deadline"`
		Targets  *[]db.PublicationTarget `json:"targets"`
	}
	if !decodeIdentityBody(w, r, &req) {
		return
	}
	if req.Deadline == nil && req.Targets == nil {
		writeJSONError(w, http.StatusBadRequest, "no changes supplied")
		return
	}
	if req.Deadline != nil {
		deadline, err := time.Parse(time.RFC3339, *req.Deadline)
		if err != nil || !deadline.After(time.Now().UTC()) {
			writeJSONError(w, http.StatusBadRequest, "invalid deadline")
			return
		}
		card.Deadline = deadline.UTC()
	}
	if req.Targets != nil {
		if len(*req.Targets) == 0 {
			card.Targets, card.RosterSnapshot, card.ExpectedCount = nil, nil, 0
		} else {
			targets, snapshot, err := s.snapshotPublicationTargets(*req.Targets)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid publication targets or roster")
				return
			}
			card.Targets, card.RosterSnapshot, card.ExpectedCount = targets, snapshot, len(snapshot)
		}
	}
	if err := s.database.UpdateLabCardScope(*card); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusConflict, "draft changed")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to update draft")
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (s *Server) handleCancelPublicationDraft(w http.ResponseWriter, r *http.Request) {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	card, ok := s.publicationDraftFromPath(w, r)
	if !ok {
		return
	}
	if err := s.database.CancelLabCardDraft(card.Number, card.DraftID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusConflict, "draft already confirmed")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel draft")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft_id": card.DraftID, "status": "cancelled"})
}

func (s *Server) writePublicationRoster(card *db.LabCardDraft) (string, error) {
	if s.cfg == nil || s.cfg.DataDir == "" {
		return "", errRosterUnavailable
	}
	dir := filepath.Join(s.cfg.DataDir, "rosters", "snapshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".draft-*.csv")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return "", err
	}
	writer := csv.NewWriter(file)
	if err := writer.Write([]string{"学号", "姓名", "班级"}); err != nil {
		file.Close()
		return "", err
	}
	for _, student := range card.RosterSnapshot {
		if err := writer.Write([]string{student.StudentID, student.StudentName, student.ClassName}); err != nil {
			file.Close()
			return "", err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	name := card.DraftID + ".csv"
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("rosters", "snapshots", name)), nil
}

func (s *Server) handleConfirmPublicationDraft(w http.ResponseWriter, r *http.Request) {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	card, ok := s.publicationDraftFromPath(w, r)
	if !ok {
		return
	}
	assignmentID := fmt.Sprintf("experiment_%d", card.Number)
	if card.Status == "confirmed" && len(card.Targets) == 0 {
		writeJSONError(w, http.StatusConflict, "legacy card requires manual publication")
		return
	}
	if card.Status == "draft" && (len(card.Targets) == 0 || card.ExpectedCount == 0 || !card.Deadline.After(time.Now().UTC().Add(24*time.Hour))) {
		writeJSONError(w, http.StatusConflict, "draft scope or reminder deadline invalid")
		return
	}
	if s.rules == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "assignment rules unavailable")
		return
	}
	rosterPath := filepath.ToSlash(filepath.Join("rosters", "snapshots", card.DraftID+".csv"))
	ruleFilename := fmt.Sprintf("实验%d-{class}-{student_id}-{name}.zip", card.Number)
	if existing, exists := s.rules.GetRule(assignmentID); exists {
		if !existing.Deadline.Equal(card.Deadline) || existing.TargetFilename != ruleFilename ||
			len(existing.RosterPaths) != 1 || existing.RosterPaths[0] != rosterPath {
			writeJSONError(w, http.StatusConflict, "experiment number already used by another draft")
			return
		}
	}
	if card.Status == "draft" {
		if _, err := s.database.ConfirmLabCardWithJobs(*card, assignmentID, time.Now().UTC()); err != nil {
			current, lookupErr := s.database.GetLabCardDraft(card.Number)
			if lookupErr != nil || current.Status != "confirmed" || current.DraftID != card.DraftID {
				writeJSONError(w, http.StatusConflict, "assignment or publication jobs could not be committed")
				return
			}
			card = current
		}
	}
	if _, exists := s.rules.GetRule(assignmentID); !exists {
		if _, err := s.writePublicationRoster(card); err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "publication is preparing; roster snapshot unavailable")
			return
		}
		cfg := rule.RuleFileConfig{
			ID: assignmentID, Name: fmt.Sprintf("实验%d", card.Number), Deadline: card.Deadline.Format(time.RFC3339),
			Rosters: []string{rosterPath}, TargetFilename: ruleFilename,
		}
		cfg.Patterns.AttachmentRegex = fmt.Sprintf(`^实验%d-(?P<class>[^-]+)-(?P<student_id>[0-9]{12})-(?P<name>[^.]+)\.zip$`, card.Number)
		rulesDir := s.cfg.RulesDir
		if rulesDir == "" {
			rulesDir = filepath.Join(s.cfg.DataDir, "rules")
		}
		if _, err := s.rules.SaveAndLoadRule(cfg, rulesDir); err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "publication is preparing; assignment rule unavailable")
			return
		}
	}
	if err := s.database.MarkPublishedAssignmentReady(assignmentID, card.DraftID, time.Now().UTC()); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "publication is preparing; retry confirmation")
		return
	}
	card, err := s.database.GetLabCardDraft(card.Number)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "publication state unavailable")
		return
	}
	jobs, err := s.database.ListPublicationJobs(card.DraftID)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "publication jobs unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"card": card, "assignment_id": assignmentID, "publication_status": "ready", "jobs": jobs})
}

func decodePublicationTargets(raw string) ([]db.PublicationTarget, error) {
	if raw == "" {
		return nil, nil
	}
	var targets []db.PublicationTarget
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, err
	}
	return targets, nil
}
