package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/roster"
	"github.com/dot/gmail-archiver/internal/rule"
)

var labCardName = regexp.MustCompile(`^实验卡([1-9][0-9]*)\.(pdf|docx|doc)$`)

const maxLabCardBytes = 25 << 20

func suggestedLabDeadline(now time.Time) time.Time {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := now.In(zone)
	days := (int(time.Wednesday) - int(local.Weekday()) + 7) % 7
	if days == 0 {
		days = 7
	}
	date := local.AddDate(0, 0, days)
	return time.Date(date.Year(), date.Month(), date.Day(), 18, 0, 0, 0, zone).UTC()
}

func validLabCard(filename string, content []byte) bool {
	switch filepath.Ext(filename) {
	case ".pdf":
		return bytes.HasPrefix(content, []byte("%PDF-"))
	case ".doc":
		return bytes.HasPrefix(content, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	case ".docx":
		archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if err != nil {
			return false
		}
		var types, document bool
		for _, item := range archive.File {
			types = types || item.Name == "[Content_Types].xml"
			document = document || item.Name == "word/document.xml"
		}
		return types && document
	}
	return false
}

func labCardNumber(raw string) (int, bool) {
	number, err := strconv.Atoi(raw)
	return number, err == nil && number > 0 && number <= 9999
}

func (s *Server) handleLabCardUpload(w http.ResponseWriter, r *http.Request) {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, maxLabCardBytes+(64<<10))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid lab card upload")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing lab card file")
		return
	}
	defer file.Close()
	match := labCardName.FindStringSubmatch(header.Filename)
	if len(match) != 3 {
		writeJSONError(w, http.StatusBadRequest, "expected 实验卡N.pdf, .docx or .doc")
		return
	}
	number, ok := labCardNumber(match[1])
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid experiment number")
		return
	}
	if _, err := s.database.GetLabCardDraft(number); err == nil {
		writeJSONError(w, http.StatusConflict, "experiment number already has a draft or assignment")
		return
	} else if err != nil && !errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusInternalServerError, "failed to check lab card draft")
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, maxLabCardBytes+1))
	if err != nil || len(content) == 0 || len(content) > maxLabCardBytes || !validLabCard(header.Filename, content) {
		writeJSONError(w, http.StatusBadRequest, "invalid lab card format or size")
		return
	}
	now := time.Now().UTC()
	draftID, err := newPublicationDraftID(number)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create draft id")
		return
	}
	deadline := suggestedLabDeadline(now)
	if raw := r.FormValue("deadline"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil || !parsed.After(now) {
			writeJSONError(w, http.StatusBadRequest, "invalid deadline")
			return
		}
		deadline = parsed.UTC()
	}
	var targets []db.PublicationTarget
	var snapshot []db.SnapshotStudent
	if raw := r.FormValue("targets"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &targets); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid publication targets")
			return
		}
		targets, snapshot, err = s.snapshotPublicationTargets(targets)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid publication targets or roster")
			return
		}
	}
	path, hash, size, err := s.storage.Save(bytes.NewReader(content), header.Filename, now)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to store lab card")
		return
	}
	card := db.LabCardDraft{
		Number: number, DraftID: draftID, Filename: header.Filename, StoragePath: path,
		SHA256: hash, FileSize: size, SuggestedDeadline: suggestedLabDeadline(now), Deadline: deadline,
		Targets: targets, RosterSnapshot: snapshot, ExpectedCount: len(snapshot),
		Status: "draft", DeliveryStatus: "not_started", UploadedAt: now,
	}
	if err := s.database.SaveLabCardDraft(card); err != nil {
		if errors.Is(err, db.ErrLabCardExists) {
			writeJSONError(w, http.StatusConflict, "experiment number already has a draft or assignment")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to save lab card draft")
		return
	}
	writeJSON(w, http.StatusCreated, card)
}

func (s *Server) getLabCard(w http.ResponseWriter, r *http.Request) (*db.LabCardDraft, bool) {
	number, ok := labCardNumber(chi.URLParam(r, "number"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid experiment number")
		return nil, false
	}
	card, err := s.database.GetLabCardDraft(number)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "lab card not found")
		return nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read lab card")
		return nil, false
	}
	return card, true
}

func (s *Server) handleGetLabCard(w http.ResponseWriter, r *http.Request) {
	card, ok := s.getLabCard(w, r)
	if ok {
		writeJSON(w, http.StatusOK, card)
	}
}

func (s *Server) handleDownloadLabCard(w http.ResponseWriter, r *http.Request) {
	card, ok := s.getLabCard(w, r)
	if !ok {
		return
	}
	path, err := s.storage.ResolveAbsolutePath(card.StoragePath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "lab card path unavailable")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "lab card file unavailable")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": card.Filename}))
	http.ServeContent(w, r, card.Filename, card.UploadedAt, file)
}

func (s *Server) rosterPathsForLabCard() ([]string, error) {
	if s.cfg == nil || s.cfg.DataDir == "" {
		return nil, errRosterUnavailable
	}
	entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, "rosters"))
	if err != nil {
		return nil, errRosterUnavailable
	}
	var paths []string
	seen := make(map[string]roster.Student)
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			list := roster.New()
			loaded, err := list.LoadCSV(filepath.Join(s.cfg.DataDir, "rosters", entry.Name()))
			if err != nil {
				return nil, errRosterUnavailable
			}
			for _, student := range loaded {
				if prior, exists := seen[student.StudentID]; exists &&
					(prior.Name != student.Name || prior.ClassName != student.ClassName) {
					return nil, errRosterUnavailable
				}
				seen[student.StudentID] = student
			}
			paths = append(paths, filepath.ToSlash(filepath.Join("rosters", entry.Name())))
		}
	}
	if len(paths) == 0 {
		return nil, errRosterUnavailable
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *Server) handleConfirmLabCard(w http.ResponseWriter, r *http.Request) {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	if s.rules == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "assignment rules unavailable")
		return
	}
	card, ok := s.getLabCard(w, r)
	if !ok {
		return
	}
	if card.DraftID != "" && len(card.Targets) > 0 {
		writeJSONError(w, http.StatusConflict, "confirm scoped draft by draft id")
		return
	}
	if card.Status == "confirmed" {
		if _, exists := s.rules.GetRule(fmt.Sprintf("experiment_%d", card.Number)); !exists {
			writeJSONError(w, http.StatusServiceUnavailable, "confirmed assignment rule unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"card": card, "delivery_status": card.DeliveryStatus})
		return
	}
	paths, err := s.rosterPathsForLabCard()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "course roster unavailable")
		return
	}
	if _, err := s.courseStudents(); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "course roster unavailable")
		return
	}
	assignmentID := fmt.Sprintf("experiment_%d", card.Number)
	name := fmt.Sprintf("实验%d", card.Number)
	filename := name + "-{class}-{student_id}-{name}.zip"
	existing, exists := s.rules.GetRule(assignmentID)
	if exists && (!existing.Deadline.Equal(card.SuggestedDeadline) || existing.TargetFilename != filename || !slices.Equal(existing.RosterPaths, paths)) {
		writeJSONError(w, http.StatusConflict, "experiment number already used by another rule")
		return
	}
	if !exists {
		cfg := rule.RuleFileConfig{
			ID: assignmentID, Name: name, Deadline: card.SuggestedDeadline.Format(time.RFC3339),
			Rosters: paths, TargetFilename: filename,
		}
		cfg.Patterns.AttachmentRegex = fmt.Sprintf(`^实验%d-(?P<class>[^-]+)-(?P<student_id>[0-9]{12})-(?P<name>[^.]+)\.zip$`, card.Number)
		rulesDir := s.cfg.RulesDir
		if rulesDir == "" {
			rulesDir = filepath.Join(s.cfg.DataDir, "rules")
		}
		if _, err := s.rules.SaveAndLoadRule(cfg, rulesDir); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to create assignment rule")
			return
		}
	}
	if err := s.database.ConfirmLabCard(card.Number, time.Now().UTC()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "assignment created but card confirmation failed")
		return
	}
	card, err = s.database.GetLabCardDraft(card.Number)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "assignment created but card status unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"card": card, "assignment_id": assignmentID, "delivery_status": card.DeliveryStatus})
}
