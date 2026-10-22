package api

import (
	"github.com/dot/gmail-archiver/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		key, url, header string
		want             int
	}{
		{"", "/api/assignments", "", 503},
		{"synthetic-secret", "/api/assignments?token=synthetic-secret", "", 401},
		{"synthetic-secret", "/api/assignments?api_key=synthetic-secret", "", 401},
		{"synthetic-secret", "/api/assignments", "wrong", 401},
		{"synthetic-secret", "/api/assignments", "synthetic-secret", 200},
	} {
		server := NewServer(&config.Config{APIKey: tc.key}, nil, nil, nil, nil)
		req := httptest.NewRequest("GET", tc.url, nil)
		req.Header.Set("X-API-Key", tc.header)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("want %d, got %d", tc.want, rec.Code)
		}
	}
}

func TestBindingRequiresStaffApprovalBeforeRosterLookup(t *testing.T) {
	server := NewServer(&config.Config{APIKey: "synthetic-secret"}, nil, nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/bindings", strings.NewReader(`{"qq_id":"9000000001","student_id":"990000000001","student_name":"示例甲"}`))
	req.Header.Set("X-API-Key", "synthetic-secret")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unapproved identity accepted: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "示例甲") {
		t.Fatal("identity leaked")
	}
}
