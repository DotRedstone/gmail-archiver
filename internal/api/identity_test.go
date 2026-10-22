package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/rule"
	"github.com/dot/gmail-archiver/internal/storage"
)

func TestExternalBindingAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "roster.csv"), []byte("学号,姓名,班级\n990000000001,示例甲,示例班A\n990000000002,示例乙,示例班A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ruleText := `id: example_lab
name: 示例实验
rosters: [roster.csv]
deadline: "2030-01-01T18:00:00+08:00"
patterns:
  subject_regex: "^示例实验$"
target_filename: "实验1-{class}-{student_id}-{name}.{ext}"
`
	if err := os.WriteFile(filepath.Join(root, "rule.yaml"), []byte(ruleText), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := rule.NewEngine(root)
	if _, err := rules.LoadRuleFile("rule.yaml"); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	storageEngine, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(&config.Config{APIKey: "synthetic-secret"}, database, storageEngine, nil, rules).Routes()
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	legacy := `{"qq_id":"9000000001","student_id":"990000000002","student_name":"示例乙","force":true}`
	if rec := call(http.MethodPost, "/api/bindings", legacy, "synthetic-secret"); rec.Code != http.StatusOK {
		t.Fatalf("legacy binding returned %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v3/bindings?platform_id=course-qq&bot_id=bot-example&external_user_id=9000000001", "", "synthetic-secret"); rec.Code != http.StatusNotFound {
		t.Fatalf("legacy QQ ID was interpreted as OpenID: %d", rec.Code)
	}
	legacyClaim := `{"platform_id":"course-qq","bot_id":"bot-example","external_user_id":"9000000001","student_id":"990000000002","student_name":"示例乙"}`
	if rec := call(http.MethodPost, "/api/v3/binding-intents", legacyClaim, "synthetic-secret"); rec.Code != http.StatusConflict {
		t.Fatalf("legacy identity was auto-claimed: %d", rec.Code)
	}
	claim := `{"platform_id":"course-qq","bot_id":"bot-example","external_user_id":"user-one","student_id":"990000000001","student_name":"示例甲"}`
	if rec := call(http.MethodPost, "/api/v3/binding-intents", claim, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed claim returned %d", rec.Code)
	}
	if rec := call(http.MethodPost, "/api/v3/binding-intents", claim, "synthetic-secret"); rec.Code != http.StatusCreated {
		t.Fatalf("claim returned %d: %s", rec.Code, rec.Body.String())
	}
	key := `{"platform_id":"course-qq","bot_id":"bot-example","external_user_id":"user-one"}`
	if rec := call(http.MethodPost, "/api/v3/binding-intents/confirm", key, "synthetic-secret"); rec.Code != http.StatusOK {
		t.Fatalf("confirm returned %d: %s", rec.Code, rec.Body.String())
	} else {
		var body db.ExternalBinding
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Source != "self_asserted" {
			t.Fatalf("unexpected binding source: %q, err=%v", body.Source, err)
		}
	}
	if rec := call(http.MethodGet, "/api/v3/bindings?platform_id=course-qq&bot_id=bot-example&external_user_id=user-one", "", "synthetic-secret"); rec.Code != http.StatusOK {
		t.Fatalf("binding lookup returned %d", rec.Code)
	}
	if rec := call(http.MethodPost, "/api/v3/my-assignments/query", key, "synthetic-secret"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"submitted":false`) {
		t.Fatalf("self-asserted status unavailable: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodPost, "/api/v3/assignments/example_lab/mine/query", key, "synthetic-secret"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"history":[]`) {
		t.Fatalf("self-asserted history unavailable: %d: %s", rec.Code, rec.Body.String())
	}
	otherKey := `{"platform_id":"course-qq","bot_id":"bot-example","external_user_id":"user-two"}`
	if rec := call(http.MethodPost, "/api/v3/assignments/example_lab/mine/query", otherKey, "synthetic-secret"); rec.Code != http.StatusForbidden {
		t.Fatalf("unbound account read another student's history: %d", rec.Code)
	}
	if rec := call(http.MethodGet, "/api/v3/bindings?platform_id=course-qq&bot_id=bot-example", "", "synthetic-secret"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"external_user_id":"user-one"`) {
		t.Fatalf("bot binding list unavailable: %d: %s", rec.Code, rec.Body.String())
	}
	upload := func() *httptest.ResponseRecorder {
		t.Helper()
		var compressed bytes.Buffer
		zw := zip.NewWriter(&compressed)
		entry, err := zw.Create("README.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("synthetic homework")); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for field, value := range map[string]string{
			"platform_id": "course-qq", "bot_id": "bot-example", "external_user_id": "user-one",
			"student_id": "990000000002", "student_name": "示例乙",
		} {
			if err := writer.WriteField(field, value); err != nil {
				t.Fatal(err)
			}
		}
		file, err := writer.CreateFormFile("file", "实验1-示例班A-990000000001-示例甲.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(compressed.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v3/assignments/example_lab/upload", &body)
		req.Header.Set("X-API-Key", "synthetic-secret")
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := upload(); rec.Code != http.StatusOK {
		t.Fatalf("self-asserted identity could not upload a file: %d: %s", rec.Code, rec.Body.String())
	} else {
		var body struct {
			StudentID string `json:"student_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.StudentID != "990000000001" {
			t.Fatalf("spoofed student field was not replaced: %q, err=%v", body.StudentID, err)
		}
	}
	if rec := call(http.MethodPost, "/api/v3/assignments/example_lab/mine/query", key, "synthetic-secret"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"submitted":true`) || strings.Contains(rec.Body.String(), "storage_path") {
		t.Fatalf("own history leaked storage path or missed upload: %d: %s", rec.Code, rec.Body.String())
	}
	verify := `{"platform_id":"course-qq","bot_id":"bot-example","external_user_id":"user-one","student_name":"示例甲","verified_by":"course-admin"}`
	if rec := call(http.MethodPost, "/api/v3/bindings/990000000001/verify", verify, "synthetic-secret"); rec.Code != http.StatusOK {
		t.Fatalf("verify returned %d: %s", rec.Code, rec.Body.String())
	} else {
		var body db.ExternalBinding
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Source != "staff_verified" || body.VerifiedBy != "course-admin" || body.VerifiedAt == nil {
			t.Fatalf("verification state missing: source=%q by=%q at=%v err=%v", body.Source, body.VerifiedBy, body.VerifiedAt, err)
		}
	}
	if rec := upload(); rec.Code != http.StatusOK {
		t.Fatalf("verified upload returned %d: %s", rec.Code, rec.Body.String())
	} else {
		var body struct {
			StudentID string `json:"student_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.StudentID != "990000000001" {
			t.Fatalf("spoofed student field was not replaced: %q, err=%v", body.StudentID, err)
		}
	}
	if rec := call(http.MethodGet, "/api/assignments/example_lab/plagiarism", "", "synthetic-secret"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"index_status":"complete"`) {
		t.Fatalf("complete plagiarism report unavailable: %d: %s", rec.Code, rec.Body.String())
	}
	attachmentID, err := database.InsertAttachment(&db.Attachment{
		MessageID: "synthetic-mail", Sender: "mail@example.invalid", Subject: "示例实验",
		ReceivedAt: time.Now().UTC(), Filename: "example.zip", FileSize: 3,
		SHA256: "synthetic-hash", StoragePath: "attachments/example.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := &db.Submission{
		AssignmentID: "example_lab", StudentID: "990000000002", StudentName: "示例乙",
		ClassName: "示例班A", AttachmentID: attachmentID, SubmittedAt: time.Now().UTC(),
		TargetFilename: "example.zip",
	}
	if _, _, err := database.RecordSubmission(pending); err != nil {
		t.Fatal(err)
	}
	if rec := call(http.MethodGet, "/api/assignments/example_lab/plagiarism", "", "synthetic-secret"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"index_status":"processing"`) {
		t.Fatalf("incomplete index was reported as complete: %d: %s", rec.Code, rec.Body.String())
	}
	if err := database.SetPlagiarismIndexProgress(pending.ID, "failed", "extraction_failed"); err != nil {
		t.Fatal(err)
	}
	if rec := call(http.MethodGet, "/api/assignments/example_lab/plagiarism", "", "synthetic-secret"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"extraction_failed":1`) {
		t.Fatalf("failed index did not expose a safe category: %d: %s", rec.Code, rec.Body.String())
	}
	if err := os.MkdirAll(filepath.Join(root, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	var recoverable bytes.Buffer
	zipWriter := zip.NewWriter(&recoverable)
	entry, err := zipWriter.Create("README.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("synthetic repair")); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "attachments", "example.zip"), recoverable.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := call(http.MethodPost, "/api/assignments/example_lab/plagiarism/reindex", "", "synthetic-secret"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reindexed":1`) {
		t.Fatalf("failed to repair index: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/assignments/example_lab/plagiarism", "", "synthetic-secret"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"index_status":"complete"`) {
		t.Fatalf("repaired report unavailable: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodPost, "/api/v3/binding-intents", strings.Replace(claim, "user-one", "user-two", 1), "synthetic-secret"); rec.Code != http.StatusConflict {
		t.Fatalf("occupied student accepted: %d", rec.Code)
	}
	if rec := call(http.MethodDelete, "/api/v3/bindings/990000000001", "", "synthetic-secret"); rec.Code != http.StatusOK {
		t.Fatalf("delete returned %d", rec.Code)
	}
	if rec := call(http.MethodGet, "/api/v3/bindings?student_id=990000000001", "", "synthetic-secret"); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted binding returned %d", rec.Code)
	}
}
