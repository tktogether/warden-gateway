package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tktogether/warden-gateway/internal/config"
)

func TestHandlerRouting(t *testing.T) {
	cfg := config.Default()
	cfg.PublicURL = "http://127.0.0.1:18080/warden"
	cfg.TLS.Mode = "self"
	cfg.Listen.HTTP = "127.0.0.1:18080"
	cfg.Listen.HTTPS = "127.0.0.1:18443"
	cfg.Listen.Entry = ""
	cfg.Listen.ACME = ""
	cfg.SNI.Enabled = false
	cfg.Docker.Enabled = false
	cfg.GitHub.Enabled = false
	cfg.SessionSecret = strings.Repeat("s", 48)
	cfg.AccessKeySHA256 = config.HashAccessKey("0123456789")
	dir := t.TempDir()
	cfg.Backup.Dir = filepath.Join(dir, "backups")
	cfg.Normalize()
	a, err := New(Options{Config: &cfg, Paths: Paths{
		ConfigPath: filepath.Join(dir, "config.json"), DataDir: dir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/warden/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status %d: %s", rec.Code, rec.Body)
	}
	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil || health["ok"] != true {
		t.Fatalf("health body %s err=%v", rec.Body, err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/warden/" {
		t.Fatalf("root redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled docker status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/https://github.com/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled github status %d", rec.Code)
	}
}

// TestHandlerHealthz covers the unauthenticated liveness endpoint on both the
// TLS front handler and the plain-HTTP handler.
func TestHandlerHealthz(t *testing.T) {
	a := newTestApp(t)

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("https healthz status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("healthz content type %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("healthz cache control %q", cc)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz body %s err=%v", rec.Body, err)
	}
	if body["ok"] != true {
		t.Fatalf("healthz ok field %v", body["ok"])
	}
	if _, ok := body["version"].(string); !ok {
		t.Fatalf("healthz version field %v", body["version"])
	}

	plain := a.HTTPHandler()
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("http healthz status %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["ok"] != true {
		t.Fatalf("http healthz body %s err=%v", rec.Body, err)
	}

	// The shared TLS manager redirects plain-HTTP panel traffic permanently.
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/warden/", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("http panel redirect status %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://") {
		t.Fatalf("http panel redirect location %q", loc)
	}

	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("healthz post status %d", rec.Code)
	}
}

// TestHandlerShortGitHubPath pins the short /<host>/<path> routing: the path is
// claimed by the github branch even when the proxy is disabled (503, not 404),
// and unrelated paths stay 404.
func TestHandlerShortGitHubPath(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/github.com/tktogether/llmio", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled short github status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("favicon status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/warden/api/cn", nil))
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("panel path was claimed by the disabled github branch: %s", rec.Body)
	}
}

// TestPlainHTTPServesShortGitHubPath pins the plain-HTTP behaviour: the short
// /<host>/<path> form is served by the GitHub proxy on port 80 as well, so a
// client whose path to 443 is unreliable can still clone over http://, while
// ACME challenges and every other path keep going to the TLS manager.
func TestPlainHTTPServesShortGitHubPath(t *testing.T) {
	a := newTestApp(t)
	plain := a.HTTPHandler()

	// The proxy is disabled in the test config, so the path must be claimed by
	// the github branch (503) rather than falling through to the https redirect.
	rec := httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/github.com/tktogether/llmio/info/refs?service=git-upload-pack", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("plain http short github status %d: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/https://github.com/tktogether/llmio", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("plain http long github status %d: %s", rec.Code, rec.Body)
	}

	// ACME challenges must never be swallowed by the proxy.
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/token", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("acme challenge status %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://") {
		t.Fatalf("acme challenge location %q", loc)
	}

	// Unrelated paths still redirect to https.
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("favicon plain http status %d", rec.Code)
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Default()
	cfg.PublicURL = "http://127.0.0.1:18080/warden"
	cfg.TLS.Mode = "self"
	cfg.Listen.HTTP = "127.0.0.1:18080"
	cfg.Listen.HTTPS = "127.0.0.1:18443"
	cfg.Listen.Entry = ""
	cfg.Listen.ACME = ""
	cfg.SNI.Enabled = false
	cfg.Docker.Enabled = false
	cfg.GitHub.Enabled = false
	cfg.SessionSecret = strings.Repeat("s", 48)
	cfg.AccessKeySHA256 = config.HashAccessKey("0123456789")
	dir := t.TempDir()
	cfg.Backup.Dir = filepath.Join(dir, "backups")
	cfg.Normalize()
	a, err := New(Options{Config: &cfg, Paths: Paths{
		ConfigPath: filepath.Join(dir, "config.json"), DataDir: dir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
