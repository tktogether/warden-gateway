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
		// Fix round 1 locks: the root binary is installed only after its bare-hex
		// .sha256 sidecar verifies, and agent mode refuses to fall back to the
		// machine's own egress IP as the gateway.
		"warden-cn-agent-linux-$arch.sha256",
		"sha256sum",
		"agent 模式必须提供 --gateway-ip",
		// Self-hosted distribution: CN machines install and upgrade the agent
		// from this gateway's cached build, never straight from GitHub.
		"/api/agent/binary/",
		"cache_agent_binaries",
		// Multi-version cache + rollback loop guard: the version is a path
		// segment, only the newest few builds are kept, and a build the guard
		// rolled back is recorded so the agent stops retrying it.
		"cn_prune_agent_cache",
		"upgrade-blocked",
		"upgrade-pending",
	}
	script := string(raw)
	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrap.sh is missing %q", want)
		}
	}
	// Fix round 2 lock: a failed re-run of cn-setup --enroll-token over an
	// already-agent machine must not leave the previous agent dead — the
	// rollback function detects the pre-existing agent unit in the snapshot
	// records and re-enables warden-cn-agent (plus restoring the pre-
	// migration agent-mode resolv.conf from the snapshot archive).
	rollback := bootstrapFuncRegion(script, "cn_agent_rollback")
	if rollback == "" {
		t.Fatal("cn_agent_rollback function not found in bootstrap.sh")
	}
	for _, want := range []string{
		`CN_AGENT_UNIT`, // re-migration detection reads the unit record
		`systemctl enable --now "$CN_AGENT_UNIT_NAME"`, // the previously working agent is brought back up
	} {
		if !strings.Contains(rollback, want) {
			t.Errorf("cn_agent_rollback is missing %q (re-migration rollback must restore the agent)", want)
		}
	}
}

// TestBootstrapRollbackGuardBaseline locks the F5 crash-rollback arithmetic in
// bootstrap.sh: the guard may only act when an upgrade-pending marker exists,
// must compare NRestarts against the baseline the agent wrote before its clean
// upgrade restart (`current - baseline >= 2`), and must not use an absolute
// count (the upgrade's own exit-0 restart used to trigger it).
func TestBootstrapRollbackGuardBaseline(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "bootstrap.sh"))
	if err != nil {
		t.Fatalf("read bootstrap.sh: %v", err)
	}
	script := string(raw)

	guard := bootstrapFuncRegion(script, "cn_agent_write_guard")
	if guard == "" {
		t.Fatal("cn_agent_write_guard function not found in bootstrap.sh")
	}
	for _, want := range []string{
		"upgrade-pending",         // only act while a build is under validation
		"NRestarts",               // read the live restart counter
		`awk '{print \$2; exit}'`, // baseline = second field of the marker
		"baseline",                // ...
		`delta=\$((nrestarts - baseline))`,
		`[ "\$delta" -ge 2 ]`, // crash => two restarts beyond the baseline
		"upgrade-blocked",     // converted after a rollback
		`\$STATE_DIR/upgrade-blocked`,
	} {
		if !strings.Contains(guard, want) {
			t.Errorf("cn_agent_write_guard is missing %q", want)
		}
	}
	if strings.Contains(guard, `"\$nrestarts" -ge 2`) {
		t.Error("guard still rolls back on the absolute NRestarts count (the upgrade's own restart would trigger it)")
	}

	// F4/deferred: the cache resolves a concrete version and verifies the CI
	// sidecar (never re-hashes its own download), and never creates "latest".
	cache := bootstrapFuncRegion(script, "cache_agent_binaries")
	if cache == "" {
		t.Fatal("cache_agent_binaries function not found in bootstrap.sh")
	}
	for _, want := range []string{
		"cache_agent_version",
		`$asset.sha256`,
		"sha256sum",
		`"$ver_dir/$asset.sha256"`,
	} {
		if !strings.Contains(cache, want) {
			t.Errorf("cache_agent_binaries is missing %q", want)
		}
	}
	if !strings.Contains(script, `case "$ver" in`) || !strings.Contains(script, `''|latest|`) {
		t.Error("cache_agent_version must reject empty/latest resolutions")
	}

	// F14: the prune fallback must be selected by probing find's -printf
	// support, not by the exit status of a piped sort.
	prune := bootstrapFuncRegion(script, "cn_prune_agent_cache")
	if prune == "" {
		t.Fatal("cn_prune_agent_cache function not found in bootstrap.sh")
	}
	if !strings.Contains(prune, `find "$root" -maxdepth 0 -printf ''`) {
		t.Error("cn_prune_agent_cache does not probe find -printf support explicitly")
	}
	if strings.Contains(prune, `if dirs="$(find`) {
		t.Error("cn_prune_agent_cache still tests sort's exit status instead of find support")
	}

	// F8: re-running cn-setup must restart an active unit and delete the stale
	// state file before waiting for a fresh successful heartbeat.
	start := bootstrapFuncRegion(script, "cn_agent_start_and_wait")
	if start == "" {
		t.Fatal("cn_agent_start_and_wait function not found in bootstrap.sh")
	}
	for _, want := range []string{
		`systemctl enable "$CN_AGENT_UNIT_NAME"`,
		`systemctl restart "$CN_AGENT_UNIT_NAME"`,
		`rm -f "$CN_AGENT_STATE_DIR/state.json"`,
	} {
		if !strings.Contains(start, want) {
			t.Errorf("cn_agent_start_and_wait is missing %q", want)
		}
	}
	if strings.Contains(start, "enable --now") {
		t.Error("cn_agent_start_and_wait uses enable --now, which is a no-op on an active unit")
	}
}

// bootstrapFuncRegion returns the body of the named shell function, from its
// `name() {` line up to the next top-level function definition, so
// assertions can target one function instead of the whole script.
func bootstrapFuncRegion(script, name string) string {
	marker := name + "() {"
	start := strings.Index(script, marker)
	if start < 0 {
		return ""
	}
	bodyStart := start + len(marker)
	next := strings.Index(script[bodyStart:], "() {")
	if next < 0 {
		return script[start:]
	}
	return script[start : bodyStart+next]
}

// TestReleaseWorkflowAgentAssets locks the release pipeline contract that the
// CN agent download paths depend on (bootstrap.sh cn-setup and
// internal/cnagent upgrade.go fetch warden-cn-agent-linux-{arch} plus a
// bare-hex .sha256 sidecar from the v{VERSION} tag): .github/workflows/
// release.yml must build ./cmd/warden-cn-agent with the same version stamps
// as the gateway, publish per-arch bare binaries named
// warden-cn-agent-linux-{amd64|arm64|arm} (matrix arch armv7 maps to asset
// arch arm), generate hash-only .sha256 sidecars, and attach the bare
// binaries and sidecars to the GitHub release.
func TestReleaseWorkflowAgentAssets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release.yml: %v", err)
	}
	workflow := string(raw)
	required := []string{
		// agent build line: same flags and -o target next to the package path
		"-o dist/warden-cn-agent ./cmd/warden-cn-agent",
		"cmd/warden-cn-agent",          // package path fed to go build
		"warden-cn-agent-linux-",       // bare binary + sidecar asset prefix
		".sha256",                      // sidecar extension
		"warden-cn-agent_linux_",       // bonus tarball naming
		"awk '{print $1}'",             // sidecar contains only the hash, one line
		"dist/warden-cn-agent-linux-*", // publish attaches binaries + sidecars
	}
	for _, want := range required {
		if !strings.Contains(workflow, want) {
			t.Errorf("release.yml is missing %q", want)
		}
	}
	// Both binaries must carry the version stamp (gateway + agent).
	const stamp = "-X github.com/tktogether/warden-gateway/internal/version.Version="
	if n := strings.Count(workflow, stamp); n != 2 {
		t.Errorf("version stamp count = %d, want 2 (gateway + agent)", n)
	}
	// The sidecar must be hashed from the published bare binary itself.
	if !strings.Contains(workflow, "sha256sum dist/warden-cn-agent-linux-") {
		t.Errorf("release.yml does not hash dist/warden-cn-agent-linux-* into its sidecar")
	}
	// Explicit arch mapping: matrix arch amd64|arm64|armv7 must publish asset
	// arch amd64|arm64|arm (armv7→arm). Match whole lines so "arm64" cannot
	// stand in for the "arm" entry.
	agentArches := map[string]bool{}
	for _, line := range strings.Split(workflow, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "agent_arch: ") {
			agentArches[strings.TrimPrefix(line, "agent_arch: ")] = true
		}
	}
	for _, want := range []string{"amd64", "arm64", "arm"} {
		if !agentArches[want] {
			t.Errorf("release.yml matrix is missing agent_arch: %s (armv7 must map to arm)", want)
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
