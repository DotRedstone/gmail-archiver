package parser

import (
	"net"
	"net/url"
	"testing"
)

func TestDownloadDestinations(t *testing.T) {
	for _, raw := range []string{"http://wx.mail.qq.com/x", "https://qq.com.attacker.invalid/x", "http://127.0.0.1/ftn/download", "https://localhost/", "https://user@wx.mail.qq.com/x", "https://wx.mail.qq.com:8443/x"} {
		u, _ := url.Parse(raw)
		if allowedQQURL(u) {
			t.Fatalf("unsafe URL accepted")
		}
	}
	for _, raw := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "fc00::1", "::ffff:127.0.0.1"} {
		if publicIP(net.ParseIP(raw)) {
			t.Fatal("private IP accepted")
		}
	}
	u, _ := url.Parse("https://wx.mail.qq.com/ftn/download")
	if !allowedQQURL(u) {
		t.Fatal("valid upstream rejected")
	}
}
