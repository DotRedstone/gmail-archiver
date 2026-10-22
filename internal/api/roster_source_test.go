package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/rule"
	"github.com/dot/gmail-archiver/internal/storage"
)

func TestStandaloneRosterAllowsBindingBeforeFirstAssignment(t *testing.T) {
	root := t.TempDir()
	rosters := filepath.Join(root, "rosters")
	if err := os.Mkdir(rosters, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rosters, "course.csv"), []byte("学号,姓名,班级\n990000000001,示例甲,示例班A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	files, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(&config.Config{APIKey: "synthetic-secret", DataDir: root}, database, files, nil, rule.NewEngine(root)).Routes()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", "synthetic-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(http.MethodGet, "/api/roster", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "990000000001") {
		t.Fatalf("standalone roster lookup: %d", rec.Code)
	}
	claim := `{"platform_id":"course-qq","bot_id":"bot-example","external_user_id":"user-one","student_id":"990000000001","student_name":"示例甲"}`
	if rec := request(http.MethodPost, "/api/v3/binding-intents", claim); rec.Code != http.StatusCreated {
		t.Fatalf("binding before first assignment: %d: %s", rec.Code, rec.Body.String())
	}
	if err := os.WriteFile(filepath.Join(rosters, "conflict.csv"), []byte("学号,姓名,班级\n990000000001,示例乙,示例班A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if rec := request(http.MethodGet, "/api/roster", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("conflicting rosters returned %d", rec.Code)
	}
}
