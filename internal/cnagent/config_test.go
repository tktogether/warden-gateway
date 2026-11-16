package cnagent

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func testConfig(t *testing.T) *Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Origin = "https://gateway.example.com"
	cfg.Token = strings.Repeat("ab", 32)
	cfg.GatewayIP = "198.18.0.1"
	return &cfg
}

func TestConfigRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := Save(path, cfg); err != nil {
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
	if !reflect.DeepEqual(cfg, loaded) {
		t.Fatalf("round trip mismatch: saved %+v, loaded %+v", cfg, loaded)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected Load of missing file to fail")
	}
}

func TestValidateRejectsShortToken(t *testing.T) {
	cfg := testConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, token := range []string{
		strings.Repeat("a", 31), // one hex char short
		strings.Repeat("g", 64), // 64 chars but not hex
	} {
		cfg.Token = token
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected token %q to be rejected", token)
		}
	}
}

func TestDefaultValues(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Upstream != "223.5.5.5:53" {
		t.Fatalf("Upstream = %q, want %q", cfg.Upstream, "223.5.5.5:53")
	}
	if cfg.DNSListen != "198.18.0.1:53" {
		t.Fatalf("DNSListen = %q, want %q", cfg.DNSListen, "198.18.0.1:53")
	}
	if cfg.HeartbeatSeconds != 10 {
		t.Fatalf("HeartbeatSeconds = %d, want 10", cfg.HeartbeatSeconds)
	}
	if cfg.Repo != "tktogether/warden-gateway" {
		t.Fatalf("Repo = %q, want %q", cfg.Repo, "tktogether/warden-gateway")
	}
}
