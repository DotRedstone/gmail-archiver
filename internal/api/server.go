package api

import (
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/dot/gmail-archiver/internal/storage"
)

// [Server]
type Server struct {
	cfg       *config.Config
	database  *db.DB
	storage   *storage.Engine
	watcher   *imap.Watcher
	startTime time.Time
}

func NewServer(cfg *config.Config, database *db.DB, storageEngine *storage.Engine, watcher *imap.Watcher) *Server {
	return &Server{
		cfg:       cfg,
		database:  database,
		storage:   storageEngine,
		watcher:   watcher,
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

	// Health check (public)
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
	})

	return r
}

// [AuthMiddleware]
func (s *Server) apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-API-Key")
		if token == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				token = strings.TrimSpace(authHeader[7:])
			}
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
