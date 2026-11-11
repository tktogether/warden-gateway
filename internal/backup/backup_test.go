package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tktogether/warden-gateway/internal/config"
)

func testConfigFile(t *testing.T, dir string) string {
	t.Helper()
	cfg := config.Default()
	cfg.PublicURL = "https://panel.example.com/warden"
	cfg.Domain = "panel.example.com"
	cfg.TLS.Mode = "self"
	cfg.TLS.CacheDir = filepath.Join(dir, "data", "acme")
	cfg.Listen.HTTP = "127.0.0.1:18080"
	cfg.Listen.HTTPS = "127.0.0.1:18443"
	cfg.Listen.Entry = ""
	cfg.Listen.ACME = ""
	cfg.SNI.Enabled = false
	cfg.SessionSecret = strings.Repeat("s", 48)
	cfg.AccessKeySHA256 = config.HashAccessKey("0123456789")
	cfg.Normalize()
	path := filepath.Join(dir, "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupCreateListRestore(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "acme", "cert.pem"), []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := testConfigFile(t, dir)
	service := filepath.Join(dir, "warden-gateway.service")
	if err := os.WriteFile(service, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := New(filepath.Join(dataDir, "backups"), cfgPath, dataDir, service, 3)
	manifest, err := mgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Size <= 0 || manifest.SHA256 == "" || len(manifest.Files) == 0 {
		t.Fatalf("bad manifest: %+v", manifest)
	}
	list, err := mgr.List()
	if err != nil || len(list) != 1 || list[0].ID != manifest.ID {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := os.WriteFile(cfgPath, []byte("{\"broken\":true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Restore(manifest.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "panel.example.com") {
		t.Fatalf("config was not restored: %s", raw)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, "acme", "cert.pem")); err != nil || string(got) != "cert" {
		t.Fatalf("acme file not restored: %q err=%v", got, err)
	}
}

func TestRejectBadID(t *testing.T) {
	mgr := New(t.TempDir(), "cfg", "data", "svc", 1)
	if _, err := mgr.Get("../../etc/passwd"); err == nil {
		t.Fatal("path traversal id accepted")
	}
}
