package parser

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	rawEmail := `From: "示例甲" <student@example.invalid>
To: recipient@example.invalid
Subject: =?UTF-8?B?5rWL6K+V6YKu5Lu2?=
Date: Thu, 08 Oct 2026 12:00:00 +0000
Message-ID: <test-12345@example.invalid>
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

	if meta.MessageID != "<test-12345@example.invalid>" {
		t.Errorf("expected MessageID <test-12345@example.invalid>, got %s", meta.MessageID)
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

// [TestQQ]
func TestParser_QQBigAttachments(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "parser_qq_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storageEngine, err := storage.New(tmpDir)
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}

	// Mock QQ FTN service
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/ftn/download" {
			_ = r.ParseForm()
			if r.FormValue("key") == "mockkey" && r.FormValue("code") == "mockcode" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"head": map[string]any{"ret": 0},
					"body": map[string]any{
						"name": "实验1-示例班A-990000000001-示例甲.zip",
						"url":  "https://download.mail.qq.com/get-file",
						"size": 16,
					},
				})
				return
			}
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		if r.Method == "GET" && r.URL.Path == "/get-file" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("PK\x03\x04mockzipdata"))
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	p := New(storageEngine)
	p.SetHTTPClient(&http.Client{Transport: rewriteTransport{base: ts.Client().Transport, target: ts.URL}})

	rawEmail := "From: sender@example.invalid\n" +
		"To: student@example.invalid\n" +
		"Subject: 并行计算-实验1-示例甲\n" +
		"Date: Thu, 08 Oct 2026 12:00:00 +0000\n" +
		"Message-ID: <sender@example.invalid>\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/html; charset=utf-8\n\n" +
		"<div>姓名：示例甲</div><div>学号：990000000001</div>\n" +
		"<a href=\"" + "https://wx.mail.qq.com" + "/ftn/download?func=3&key=mockkey&code=mockcode\">进入下载页面</a>\n"

	meta, err := p.Parse(strings.NewReader(rawEmail))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(meta.Attachments) != 1 {
		t.Fatalf("expected 1 QQ attachment, got %d", len(meta.Attachments))
	}

	att := meta.Attachments[0]
	if att.Filename != "实验1-示例班A-990000000001-示例甲.zip" {
		t.Errorf("expected Filename '实验1-示例班A-990000000001-示例甲.zip', got %q", att.Filename)
	}

	absPath, err := storageEngine.ResolveAbsolutePath(att.StoragePath)
	if err != nil {
		t.Fatalf("ResolveAbsolutePath error = %v", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if !bytes.Equal(data, []byte("PK\x03\x04mockzipdata")) {
		t.Errorf("file data mismatch, got %q", string(data))
	}
}

// 测试传输只把合成 QQ 请求映射到本地服务器，生产 URL 校验保持开启。
type rewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	clone.URL = &u
	target, _ := url.Parse(rt.target)
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	return rt.base.RoundTrip(clone)
}
