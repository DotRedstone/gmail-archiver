package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/storage"
)

// [Test]
func TestAPIServer(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "api_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer database.Close()

	storageEngine, err := storage.New(tmpDir)
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}

	cfg := &config.Config{
		DataDir:  tmpDir,
		HTTPPort: 8080,
		APIKey:   "secret-token-123",
	}

	srv := NewServer(cfg, database, storageEngine, nil)
	handler := srv.Routes()

	// 1. Health check should be accessible without auth
	reqHealth := httptest.NewRequest("GET", "/health", nil)
	recHealth := httptest.NewRecorder()
	handler.ServeHTTP(recHealth, reqHealth)

	if recHealth.Code != http.StatusOK {
		t.Fatalf("health endpoint returned status %d", recHealth.Code)
	}

	// 2. Attachments without API key should return 401
	reqListNoAuth := httptest.NewRequest("GET", "/api/attachments", nil)
	recListNoAuth := httptest.NewRecorder()
	handler.ServeHTTP(recListNoAuth, reqListNoAuth)

	if recListNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", recListNoAuth.Code)
	}

	// 3. Save a test attachment
	fileContent := []byte("confidential document bytes")
	now := time.Now().UTC()
	relPath, hash, size, err := storageEngine.Save(bytes.NewReader(fileContent), "财务报表.pdf", now)
	if err != nil {
		t.Fatalf("storage.Save error = %v", err)
	}

	att := &db.Attachment{
		MessageID:   "<finance-001@example.com>",
		Sender:      "CFO <cfo@example.com>",
		Subject:     "Q3 Financials",
		ReceivedAt:  now,
		Filename:    "财务报表.pdf",
		FileSize:    size,
		SHA256:      hash,
		MIMEType:    "application/pdf",
		StoragePath: relPath,
		CreatedAt:   now,
	}
	id, err := database.InsertAttachment(att)
	if err != nil {
		t.Fatalf("InsertAttachment error = %v", err)
	}

	// 4. Attachments with X-API-Key should return 200 and data
	reqListAuth := httptest.NewRequest("GET", "/api/attachments?keyword=财务", nil)
	reqListAuth.Header.Set("X-API-Key", "secret-token-123")
	recListAuth := httptest.NewRecorder()
	handler.ServeHTTP(recListAuth, reqListAuth)

	if recListAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", recListAuth.Code)
	}

	var listResp struct {
		Data       []db.Attachment `json:"data"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(recListAuth.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}
	if listResp.Pagination.Total != 1 || len(listResp.Data) != 1 {
		t.Fatalf("expected 1 record, got %d", listResp.Pagination.Total)
	}

	// 5. Download attachment with Bearer token
	reqDownload := httptest.NewRequest("GET", fmt.Sprintf("/api/attachments/%d/download", id), nil)
	reqDownload.Header.Set("Authorization", "Bearer secret-token-123")
	recDownload := httptest.NewRecorder()
	handler.ServeHTTP(recDownload, reqDownload)

	if recDownload.Code != http.StatusOK {
		t.Fatalf("download expected 200 OK, got %d", recDownload.Code)
	}
	if !bytes.Equal(recDownload.Body.Bytes(), fileContent) {
		t.Fatalf("downloaded content mismatch")
	}

	// Verify Content-Disposition header
	dispHeader := recDownload.Header().Get("Content-Disposition")
	if dispHeader == "" {
		t.Errorf("missing Content-Disposition header")
	}
}
