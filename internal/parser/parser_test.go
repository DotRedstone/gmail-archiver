package parser

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/dot/gmail-archiver/internal/storage"
)

// [Test]
func TestParser_MIMEAttachments(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "parser_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storageEngine, err := storage.New(tmpDir)
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}

	p := New(storageEngine)

	// Sample raw email with RFC 2047 encoded subject and attachment filename
	rawEmail := `From: "Jane Doe" <jane@example.com>
To: recipient@example.com
Subject: =?UTF-8?B?5rWL6K+V6YKu5Lu2?=
Date: Thu, 08 Oct 2026 12:00:00 +0000
Message-ID: <test-12345@example.com>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="boundary-xyz"

--boundary-xyz
Content-Type: text/plain; charset=utf-8

This is the email body.
--boundary-xyz
Content-Type: application/pdf
Content-Disposition: attachment; filename="=?UTF-8?B?5rWL6K+V5paH5Lu2LnBkZg==?="
Content-Transfer-Encoding: base64

aGVsbG8gd29ybGQgYXR0YWNobWVudA==
--boundary-xyz--
`

	meta, err := p.Parse(strings.NewReader(rawEmail))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if meta.MessageID != "<test-12345@example.com>" {
		t.Errorf("expected MessageID <test-12345@example.com>, got %s", meta.MessageID)
	}

	if meta.Subject != "测试邮件" {
		t.Errorf("expected Subject '测试邮件', got %q", meta.Subject)
	}

	if len(meta.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(meta.Attachments))
	}

	att := meta.Attachments[0]
	if att.Filename != "测试文件.pdf" {
		t.Errorf("expected Filename '测试文件.pdf', got %q", att.Filename)
	}

	if att.FileSize != 22 { // len("hello world attachment")
		t.Errorf("expected FileSize 22, got %d", att.FileSize)
	}

	absPath, err := storageEngine.ResolveAbsolutePath(att.StoragePath)
	if err != nil {
		t.Fatalf("ResolveAbsolutePath error = %v", err)
	}

	savedBytes, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}

	if !bytes.Equal(savedBytes, []byte("hello world attachment")) {
		t.Errorf("saved content mismatch: got %q", string(savedBytes))
	}
}
