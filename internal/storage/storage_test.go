package storage

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// [Test]
func TestStorageEngine(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "storage_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	engine, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	content := []byte("hello world attachment content")
	recvTime := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	relPath, hash, size, err := engine.Save(bytes.NewReader(content), "../../test..//doc:ument?.pdf", recvTime)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if size != int64(len(content)) {
		t.Errorf("size expected %d, got %d", len(content), size)
	}

	if !strings.HasPrefix(relPath, "attachments/2026/10/") {
		t.Errorf("unexpected relPath prefix: %s", relPath)
	}

	// Verify security resolution
	abs, err := engine.ResolveAbsolutePath(relPath)
	if err != nil {
		t.Fatalf("ResolveAbsolutePath() error = %v", err)
	}
	readBack, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(readBack, content) {
		t.Errorf("content mismatch")
	}

	// Test path traversal protection
	badPaths := []string{
		"../test",
		"/etc/passwd",
		"attachments/../../etc/passwd",
	}
	for _, bp := range badPaths {
		if _, err := engine.ResolveAbsolutePath(bp); err != ErrPathTraversal {
			t.Errorf("expected ErrPathTraversal for %s, got %v", bp, err)
		}
	}

	// Test saving identical file (dedup/reuse)
	relPath2, hash2, size2, err := engine.Save(bytes.NewReader(content), "../../test..//doc:ument?.pdf", recvTime)
	if err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	if relPath != relPath2 || hash != hash2 || size != size2 {
		t.Errorf("identical file should return same path and hash")
	}
}
