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
