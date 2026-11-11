package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Default()
	cfg.PublicURL = "https://panel.example.com/warden"
	cfg.Domain = "panel.example.com"
	cfg.Email = "ops@example.com"
	cfg.TLS.Mode = "auto"
	cfg.TLS.CacheDir = t.TempDir()
	cfg.Listen.HTTP = "127.0.0.1:18080"
	cfg.Listen.HTTPS = "127.0.0.1:18443"
	cfg.Listen.Entry = "127.0.0.1:18444"
	cfg.Listen.ACME = "127.0.0.1:18081"
	cfg.SessionSecret = strings.Repeat("s", 48)
	cfg.AccessKeySHA256 = HashAccessKey("0123456789")
	cfg.Normalize()
	return &cfg
}

func TestDefaultAndValidate(t *testing.T) {
	cfg := testConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.PanelURL() != "https://panel.example.com/warden" {
		t.Fatalf("panel url = %q", cfg.PanelURL())
	}
}

func TestValidateRejectsShortKey(t *testing.T) {
	for _, key := range []string{"short", "123456789", "has space12345"} {
		if err := ValidateAccessKey(key); err == nil {
			t.Fatalf("expected key %q to be rejected", key)
		}
	}
	if err := ValidateAccessKey("0123456789"); err != nil {
		t.Fatalf("10 character key rejected: %v", err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PublicURL != cfg.PublicURL || loaded.AccessKeySHA256 != cfg.AccessKeySHA256 {
		t.Fatalf("loaded config mismatch: %+v", loaded)
	}
}

func TestSelfModeAllowsEmptyEntry(t *testing.T) {
	cfg := testConfig(t)
	cfg.PublicURL = "https://203.0.113.10:8443/warden"
	cfg.Domain = "203.0.113.10"
	cfg.TLS.Mode = "self"
	cfg.TLS.CacheDir = ""
	cfg.Listen.Entry = ""
	cfg.Listen.ACME = ""
	cfg.SNI.Enabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("self mode should validate: %v", err)
	}
}
