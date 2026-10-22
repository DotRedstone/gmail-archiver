package api

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/rule"
	"github.com/dot/gmail-archiver/internal/storage"
)

func TestLabCardDraftAndManualPublication(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "rosters"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rosters", "course.csv"), []byte("学号,姓名,班级\n990000000001,示例甲,示例班A\n"), 0600); err != nil {
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
	rules := rule.NewEngine(root)
	handler := NewServer(&config.Config{APIKey: "synthetic-secret", DataDir: root}, database, files, nil, rules).Routes()
	content := []byte("%PDF-1.4\nsynthetic lab card\n")
	upload := func(key string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		file, err := writer.CreateFormFile("file", "实验卡1.pdf")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v3/lab-cards", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-API-Key", "synthetic-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := upload(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upload: %d", rec.Code)
	}
	if rec := upload("synthetic-secret"); rec.Code != http.StatusCreated {
		t.Fatalf("draft upload: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := upload("synthetic-secret"); rec.Code != http.StatusConflict {
		t.Fatalf("second draft overwrote experiment number: %d", rec.Code)
	}
	if rec := request(http.MethodGet, "/api/v3/lab-cards/1/download"); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("card download: %d", rec.Code)
	}
	conflictPath := filepath.Join(root, "rosters", "conflict.csv")
	if err := os.WriteFile(conflictPath, []byte("学号,姓名,班级\n990000000001,示例乙,示例班A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if rec := request(http.MethodPost, "/api/v3/lab-cards/1/confirm"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("conflicting roster allowed publication: %d", rec.Code)
	}
	if err := os.WriteFile(conflictPath, []byte("学号,姓名,班级\n990000000001,示例甲,示例班A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	confirmed := request(http.MethodPost, "/api/v3/lab-cards/1/confirm")
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmation: %d: %s", confirmed.Code, confirmed.Body.String())
	}
	var outcome struct {
		AssignmentID   string `json:"assignment_id"`
		DeliveryStatus string `json:"delivery_status"`
	}
	if err := json.Unmarshal(confirmed.Body.Bytes(), &outcome); err != nil || outcome.AssignmentID != "experiment_1" || outcome.DeliveryStatus != "manual_pending" {
		t.Fatalf("wrong confirmation outcome: %s", confirmed.Body.String())
	}
	if _, ok := rules.GetRule("experiment_1"); !ok {
		t.Fatal("confirmed card did not create assignment")
	}
	if rec := request(http.MethodGet, "/api/v3/lab-cards/1"); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"delivery_status":"manual_pending"`)) {
		t.Fatalf("manual delivery state was not persisted: %d: %s", rec.Code, rec.Body.String())
	}
	manualCard, err := database.GetLabCardDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if rec := request(http.MethodGet, "/api/v3/publication-drafts/"+manualCard.DraftID); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"publication_status":"manual_pending"`)) {
		t.Fatalf("legacy manual draft acquired automatic jobs: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, "/api/v3/lab-cards/1/confirm"); rec.Code != http.StatusOK {
		t.Fatalf("repeat confirmation: %d", rec.Code)
	}
	if rec := upload("synthetic-secret"); rec.Code != http.StatusConflict {
		t.Fatalf("confirmed card was overwritten: %d", rec.Code)
	}
}

func TestScopedPublicationDraftJobs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "rosters"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rosters", "course.csv"), []byte("学号,姓名,班级\n990000000001,示例甲,示例班A\n"), 0o600); err != nil {
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
	rules := rule.NewEngine(root)
	handler := NewServer(&config.Config{APIKey: "synthetic-secret", DataDir: root}, database, files, nil, rules).Routes()
	call := func(method, path, body, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("X-API-Key", "synthetic-secret")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	deadline := time.Now().UTC().Add(6 * 24 * time.Hour).Truncate(time.Second)
	if err := writer.WriteField("deadline", deadline.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	targets := `[ {"platform_id":"course-qq", "bot_id":"bot-example", "group_openid":"group-example", "class_names":["示例班A"]} ]`
	if err := writer.WriteField("targets", targets); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "实验卡2.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("%PDF-1.4\nsynthetic")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v3/lab-cards", &body)
	req.Header.Set("X-API-Key", "synthetic-secret")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d: %s", rec.Code, rec.Body.String())
	}
	var card db.LabCardDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.DraftID == "" || card.ExpectedCount != 1 || len(card.Targets) != 1 || card.Targets[0].ExpectedCount != 1 {
		t.Fatalf("incomplete draft preview: %s", rec.Body.String())
	}
	path := "/api/v3/publication-drafts/" + card.DraftID
	if rec := call(http.MethodPost, "/api/v3/lab-cards/2/confirm", "", ""); rec.Code != http.StatusConflict {
		t.Fatalf("scoped draft accepted by legacy path: %d", rec.Code)
	}
	if rec := call(http.MethodPatch, path, `{"targets":[{"platform_id":"course-qq","bot_id":"bot-example","group_openid":"group-example","class_names":["示例班A"]}]}`, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d: %s", rec.Code, rec.Body.String())
	}
	inject, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer inject.Close()
	if _, err := inject.Exec(`CREATE TRIGGER reject_publication_job BEFORE INSERT ON publication_delivery_jobs
		BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if rec := call(http.MethodPost, path+"/confirm", "", ""); rec.Code != http.StatusConflict {
		t.Fatalf("injected job failure: %d: %s", rec.Code, rec.Body.String())
	}
	if current, err := database.GetLabCardDraft(2); err != nil || current.Status != "draft" {
		t.Fatalf("job failure left confirmed card: %v, %v", current, err)
	}
	var publishedCount, jobCount int
	if err := inject.QueryRow(`SELECT COUNT(*) FROM published_assignments WHERE assignment_id='experiment_2'`).Scan(&publishedCount); err != nil {
		t.Fatal(err)
	}
	if err := inject.QueryRow(`SELECT COUNT(*) FROM publication_delivery_jobs WHERE assignment_id='experiment_2'`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if publishedCount != 0 || jobCount != 0 {
		t.Fatalf("job failure left half publication: assignment=%d jobs=%d", publishedCount, jobCount)
	}
	if _, exists := rules.GetRule("experiment_2"); exists {
		t.Fatal("job failure left an assignment rule")
	}
	if _, err := os.Stat(filepath.Join(root, "rosters", "snapshots", card.DraftID+".csv")); !os.IsNotExist(err) {
		t.Fatalf("job failure left a roster snapshot: %v", err)
	}
	if _, err := inject.Exec(`DROP TRIGGER reject_publication_job`); err != nil {
		t.Fatal(err)
	}
	confirmed := call(http.MethodPost, path+"/confirm", "", "")
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm: %d: %s", confirmed.Code, confirmed.Body.String())
	}
	var result struct {
		AssignmentID string              `json:"assignment_id"`
		Jobs         []db.PublicationJob `json:"jobs"`
	}
	if err := json.Unmarshal(confirmed.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AssignmentID != "experiment_2" || len(result.Jobs) != 3 {
		t.Fatalf("wrong jobs: %s", confirmed.Body.String())
	}
	if _, ok := rules.GetRule("experiment_2"); !ok {
		t.Fatal("assignment rule unavailable")
	}
	var fileJob db.PublicationJob
	var noticeJob db.PublicationJob
	for _, job := range result.Jobs {
		if job.Kind == "file" {
			fileJob = job
		} else if job.Kind == "notice" {
			noticeJob = job
		}
	}
	if fileJob.JobID == "" || noticeJob.JobID == "" {
		t.Fatal("immediate jobs missing")
	}
	if rec := call(http.MethodGet, "/api/v3/publication-jobs?platform_id=course-qq&bot_id=bot-example", "", ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(fileJob.JobID)) {
		t.Fatalf("due jobs: %d: %s", rec.Code, rec.Body.String())
	}
	claim := fmt.Sprintf(`{"platform_id":"course-qq","bot_id":"bot-example"}`)
	if rec := call(http.MethodPost, "/api/v3/publication-jobs/"+fileJob.JobID+"/claim", claim, "application/json"); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"unknown"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"failure_category":"interrupted"`)) {
		t.Fatalf("claim: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v3/publication-jobs?platform_id=course-qq&bot_id=bot-example", "", ""); bytes.Contains(rec.Body.Bytes(), []byte(fileJob.JobID)) {
		t.Fatalf("claimed job was repolled: %s", rec.Body.String())
	}
	ack := `{"platform_id":"course-qq","bot_id":"bot-example","status":"sent","message_id":"synthetic-message"}`
	for i := 0; i < 2; i++ {
		if rec := call(http.MethodPost, "/api/v3/publication-jobs/"+fileJob.JobID+"/result", ack, "application/json"); rec.Code != http.StatusOK {
			t.Fatalf("ack %d: %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	if rec := call(http.MethodPost, "/api/v3/publication-jobs/"+noticeJob.JobID+"/claim", claim, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("notice claim: %d: %s", rec.Code, rec.Body.String())
	}
	unknown := `{"platform_id":"course-qq","bot_id":"bot-example","status":"unknown","failure_category":"timeout"}`
	if rec := call(http.MethodPost, "/api/v3/publication-jobs/"+noticeJob.JobID+"/result", unknown, "application/json"); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"failure_category":"timeout"`)) {
		t.Fatalf("unknown receipt was lost: %d: %s", rec.Code, rec.Body.String())
	}
	manual := `{"platform_id":"course-qq","bot_id":"bot-example","status":"manual_pending","failure_category":"dependency_unknown"}`
	if rec := call(http.MethodPost, "/api/v3/publication-jobs/"+noticeJob.JobID+"/result", manual, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("manual reconciliation: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodPost, path+"/confirm", "", ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"sent"`)) {
		t.Fatalf("repeat confirm created new jobs: %d: %s", rec.Code, rec.Body.String())
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- call(http.MethodPost, path+"/confirm", "", "") }()
	}
	wg.Wait()
	close(responses)
	for response := range responses {
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(fileJob.JobID)) {
			t.Fatalf("concurrent confirm changed jobs: %d: %s", response.Code, response.Body.String())
		}
	}
	if rec := call(http.MethodPatch, path, `{"deadline":"2035-01-01T18:00:00+08:00"}`, "application/json"); rec.Code != http.StatusConflict {
		t.Fatalf("confirmed draft was edited: %d", rec.Code)
	}
	if rec := call(http.MethodDelete, path, "", ""); rec.Code != http.StatusConflict {
		t.Fatalf("confirmed draft was cancelled: %d", rec.Code)
	}
}

func TestPublicationRuleProjectionCanResumeWithoutResending(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "rosters"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rosters", "course.csv"), []byte("学号,姓名,班级\n990000000001,示例甲,示例班A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedRules := filepath.Join(root, "blocked-rules")
	if err := os.WriteFile(blockedRules, []byte("synthetic blocker"), 0o600); err != nil {
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
	rules := rule.NewEngine(root)
	handler := NewServer(&config.Config{APIKey: "synthetic-secret", DataDir: root, RulesDir: blockedRules}, database, files, nil, rules).Routes()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("deadline", time.Now().UTC().Add(6*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("targets", `[{"platform_id":"course-qq","bot_id":"bot-example","group_openid":"group-example","class_names":["示例班A"]}]`); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "实验卡4.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("%PDF-1.4\nsynthetic")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/v3/lab-cards", &body)
	uploadReq.Header.Set("X-API-Key", "synthetic-secret")
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	upload := httptest.NewRecorder()
	handler.ServeHTTP(upload, uploadReq)
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload: %d: %s", upload.Code, upload.Body.String())
	}
	var card db.LabCardDraft
	if err := json.Unmarshal(upload.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-API-Key", "synthetic-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	path := "/api/v3/publication-drafts/" + card.DraftID
	if rec := call(http.MethodPost, path+"/confirm"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("projection failure: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, path); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"publication_status":"preparing"`)) {
		t.Fatalf("preparing state unavailable: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v3/publication-jobs?platform_id=course-qq&bot_id=bot-example"); rec.Code != http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte(`"job_id"`)) {
		t.Fatalf("preparing jobs became sendable: %d: %s", rec.Code, rec.Body.String())
	}
	if err := os.Remove(blockedRules); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blockedRules, 0o700); err != nil {
		t.Fatal(err)
	}
	if rec := call(http.MethodPost, path+"/confirm"); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"publication_status":"ready"`)) {
		t.Fatalf("projection retry: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v3/publication-jobs?platform_id=course-qq&bot_id=bot-example"); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"kind":"file"`)) {
		t.Fatalf("ready jobs unavailable: %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLabCardFormatValidation(t *testing.T) {
	if !validLabCard("实验卡1.pdf", []byte("%PDF-1.4\nsynthetic")) || validLabCard("实验卡1.pdf", []byte("not a pdf")) {
		t.Fatal("PDF signature validation failed")
	}
	if !validLabCard("实验卡1.doc", []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		t.Fatal("Word signature validation failed")
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, name := range []string{"[Content_Types].xml", "word/document.xml"} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("synthetic")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !validLabCard("实验卡1.docx", data.Bytes()) || validLabCard("实验卡1.docx", []byte("not a zip")) {
		t.Fatal("DOCX container validation failed")
	}
}

func TestSuggestedLabDeadline(t *testing.T) {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	uploaded := time.Date(2030, 10, 17, 10, 0, 0, 0, zone)
	deadline := suggestedLabDeadline(uploaded)
	if deadline.In(zone).Weekday() != time.Wednesday || deadline.In(zone).Hour() != 18 || deadline.Sub(uploaded) != 6*24*time.Hour+8*time.Hour {
		t.Fatalf("unexpected deadline: %s", deadline)
	}
}
