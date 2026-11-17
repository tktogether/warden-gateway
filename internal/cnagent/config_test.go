package cnagent

import (
	"encoding/json"
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
	cfg.InsecureTLS = true
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

// TestInsecureTLSJSON pins the install-time opt-in for self-signed/IP-only
// gateways: the normal ACME path omits the field so agent.json stays minimal,
// and only an enabled opt-in serializes "insecure_tls": true.
func TestInsecureTLSJSON(t *testing.T) {
	cfg := testConfig(t)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "insecure_tls") {
		t.Errorf("marshal without InsecureTLS = %s, want no insecure_tls field", data)
	}
	cfg.InsecureTLS = true
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"insecure_tls":true`) {
		t.Errorf("marshal with InsecureTLS = %s, want insecure_tls true", data)
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

// TestValidateUpstream pins the ip:port upstream contract: the host part
// lands verbatim in resolv.conf nameserver lines (bootstrap) and is dialed
// directly by the DNS proxy, so only an IPv4 host with a numeric port
// passes; an empty Upstream falls back to DefaultUpstream.
func TestValidateUpstream(t *testing.T) {
	cfg := testConfig(t)

	// The default and a plain override both pass.
	for _, good := range []string{"", " ", "223.5.5.5:53", "119.29.29.29:5353"} {
		cfg.Upstream = good
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate with upstream %q = %v, want nil", good, err)
		}
	}

	for _, bad := range []string{
		"dns.example.com:53", // hostname host part: resolv.conf needs an IPv4 literal
		"223.5.5.5",          // missing port
		"223.5.5.5:",         // empty port
		"223.5.5.5:53:99",    // malformed pair
		"999.9.9.9:53",       // not an IP
		"2001:db8::1:53",     // IPv6 host (ambiguous without brackets, not an IPv4 literal)
		"[2001:db8::1]:53",   // bracketed IPv6: resolv.conf nameserver would break the agent form
		"223.5.5.5:0",        // port 0 is not dialable
		"223.5.5.5:70000",    // out of range
		"223.5.5.5:dns",      // non-numeric port
	} {
		cfg.Upstream = bad
		err := cfg.Validate()
		if err == nil {
			t.Errorf("Validate with upstream %q = nil, want an error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "upstream") {
			t.Errorf("Validate with upstream %q = %v, want the error to mention upstream", bad, err)
		}
	}
}
