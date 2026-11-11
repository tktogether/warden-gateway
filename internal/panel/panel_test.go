package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/backup"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

func newPanel(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.PublicURL = "http://127.0.0.1:18080/warden"
	cfg.Domain = ""
	cfg.TLS.Mode = "self"
	cfg.Listen.HTTP = "127.0.0.1:18080"
	cfg.Listen.HTTPS = "127.0.0.1:18443"
	cfg.Listen.Entry = ""
	cfg.Listen.ACME = ""
	cfg.SNI.Enabled = false
	cfg.SessionSecret = strings.Repeat("s", 48)
	key := "0123456789"
	cfg.AccessKeySHA256 = config.HashAccessKey(key)
	cfg.Backup.Dir = filepath.Join(dir, "backups")
	cfg.Normalize()
	cfgPath := filepath.Join(dir, "config.json")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	mgr, err := auth.NewManager(cfg.SessionSecret, cfg.AccessKeySHA256, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Config: &cfg, ConfigPath: cfgPath, DataDir: dataDir,
		ServiceName: "warden-gateway.service", Auth: mgr,
		Backups: backup.New(cfg.Backup.Dir, cfgPath, dataDir, filepath.Join(dir, "svc"), 3),
		Stats:   telemetry.NewStore(), Started: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, key
}

func do(t *testing.T, srv *Server, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestLoginAndBackup(t *testing.T) {
	srv, key := newPanel(t)
	rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": key}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login status %d: %s", rec.Code, rec.Body)
	}
	var login struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if !login.Authenticated || login.CSRF == "" {
		t.Fatalf("bad login payload: %+v", login)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wg_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}
	if rec := do(t, srv, "GET", "/warden/api/session", nil, cookie, ""); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("authenticated")) {
		t.Fatalf("session after login: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "POST", "/warden/api/backups", map[string]any{}, cookie, login.CSRF)
	if rec.Code != http.StatusOK {
		t.Fatalf("backup status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "GET", "/warden/api/backups", nil, cookie, "")
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("backup-")) {
		t.Fatalf("backup list: %d %s", rec.Code, rec.Body)
	}
}

func TestKeyRotationInvalidatesSession(t *testing.T) {
	srv, key := newPanel(t)
	rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": key}, nil, "")
	var login struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wg_session" {
			cookie = c
		}
	}
	newKey := "changed-key-987654"
	rec = do(t, srv, "POST", "/warden/api/key/change", map[string]string{"current_key": key, "new_key": newKey}, cookie, login.CSRF)
	if rec.Code != http.StatusOK {
		t.Fatalf("key change status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "GET", "/warden/api/session", nil, cookie, "")
	var after struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Authenticated {
		t.Fatal("old session still valid after key change")
	}
	if rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": newKey}, nil, ""); rec.Code != 200 {
		t.Fatalf("new key login status %d", rec.Code)
	}
	if rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": key}, nil, ""); rec.Code != 401 {
		t.Fatalf("old key login status %d", rec.Code)
	}
}
