package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	key := "0123456789abcdef"
	mgr, err := NewManager(strings.Repeat("x", 48), config.HashAccessKey(key), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return mgr, key
}

func TestIssueVerifyAndRotate(t *testing.T) {
	mgr, _ := newTestManager(t)
	token, sess, err := mgr.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if sess.CSRF == "" || sess.Expires <= sess.Issued {
		t.Fatalf("bad session: %+v", sess)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: mgr.CookieName(), Value: token})
	got, ok := mgr.Verify(req)
	if !ok || got.CSRF != sess.CSRF {
		t.Fatalf("verify failed: %+v ok=%v", got, ok)
	}
	mgr.SetKeyHash(config.HashAccessKey("another-key-123"))
	if _, ok := mgr.Verify(req); ok {
		t.Fatal("old session should be invalid after key rotation")
	}
}

func TestVerifyAccessKey(t *testing.T) {
	mgr, key := newTestManager(t)
	if !mgr.VerifyAccessKey(key) {
		t.Fatal("valid key rejected")
	}
	if mgr.VerifyAccessKey("wrong") {
		t.Fatal("invalid key accepted")
	}
}

func TestLimiter(t *testing.T) {
	lim := NewLoginLimiter(2, time.Minute)
	if !lim.Allow("1.2.3.4") {
		t.Fatal("first attempt should be allowed")
	}
	lim.Fail("1.2.3.4")
	lim.Fail("1.2.3.4")
	if lim.Allow("1.2.3.4") {
		t.Fatal("limiter did not block")
	}
	if !lim.Allow("5.6.7.8") {
		t.Fatal("other ip should be allowed")
	}
	lim.Reset("1.2.3.4")
	if !lim.Allow("1.2.3.4") {
		t.Fatal("reset did not clear counter")
	}
}
