package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

// GHCR serves manifests from ghcr.io but redirects every blob request to
// pkg-containers.githubusercontent.com. The CN split must route both hosts
// through the gateway: with only ghcr.io, docker pull fetches the manifest and
// then stalls on layers that a domestic line cannot reach.
func TestSplitDomainsCoverGHCRBlobs(t *testing.T) {
	required := []string{"ghcr.io", "pkg-containers.githubusercontent.com"}
	got := DefaultSplitDomains()
	for _, want := range required {
		if !slices.Contains(got, want) {
			t.Errorf("DefaultSplitDomains() is missing %s", want)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "bootstrap.sh"))
	if err != nil {
		t.Fatalf("read bootstrap.sh: %v", err)
	}
	line := ""
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "CN_DEFAULT_DOMAINS=") {
			line = strings.Trim(strings.TrimPrefix(l, "CN_DEFAULT_DOMAINS="), `"`)
			break
		}
	}
	if line == "" {
		t.Fatal("CN_DEFAULT_DOMAINS not found in bootstrap.sh")
	}
	script := strings.Split(line, ",")
	for _, want := range required {
		if !slices.Contains(script, want) {
			t.Errorf("bootstrap.sh CN_DEFAULT_DOMAINS is missing %s", want)
		}
	}
	slices.Sort(got)
	slices.Sort(script)
	if !slices.Equal(got, script) {
		t.Errorf("DefaultSplitDomains() and bootstrap.sh CN_DEFAULT_DOMAINS diverge:\nconfig: %v\nscript: %v", got, script)
	}
}

// TestBootstrapAgentMode locks the agent-mode migration pieces of
// bootstrap.sh (spec §9.4): the new CLI flags, the agent systemd unit with
// the controller-ruled watchdog budget, the resolv.conf final form (agent
// stub first, public fallback second), the managed hosts block removal, the
// binary rollback guard, the agent.json heartbeat floor, and the legacy
// deprecation hint printed when cn-setup runs without --enroll-token.
func TestBootstrapAgentMode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "bootstrap.sh"))
	if err != nil {
		t.Fatalf("read bootstrap.sh: %v", err)
	}
	required := []string{
		"--enroll-token", // new flag switching cn-setup into agent mode
		"--no-agent",     // force the legacy path
		"--upstream",     // agent upstream DNS override
		"warden-cn-agent.service",
		"WatchdogSec=60",        // ruling R7: worst-case cycle ~39s, 30s would starve
		"nameserver 198.18.0.1", // agent stub resolver is primary after migration
		"nameserver 223.5.5.5",  // public fallback after migration
		"cn_remove_hosts",       // spec §9.4 step 7: managed hosts block removal
		"CN_HOSTS_BEGIN",        // the block markers the removal works on
		"agent-rollback-guard",  // ExecStartPre crash-rollback guard script
		"heartbeat_seconds",     // agent.json field, written >= 10
		"已弃用",                   // legacy path deprecation hint
	}
	script := string(raw)
	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrap.sh is missing %q", want)
		}
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
