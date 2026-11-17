package cnagent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/version"
)

// newGuardAgent builds an agent whose state dir is a temp dir and whose state
// file lives inside it, matching the layout the guard script writes to.
func newGuardAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	a := &Agent{statePath: filepath.Join(dir, "state.json")}
	a.log = &logging.Logger{}
	return a
}

// TestUpgradeBlockedAfterRollback pins the loop guard: once the ExecStartPre
// guard recorded that a build was rolled back, the agent refuses to retry that
// same automatic upgrade.
func TestUpgradeBlockedAfterRollback(t *testing.T) {
	a := newGuardAgent(t)
	if a.upgradeBlocked("1.7.0") {
		t.Fatal("no block recorded yet, upgradeBlocked must be false")
	}
	if err := os.WriteFile(a.stateFile(upgradeBlockedFile), []byte("1.7.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.upgradeBlocked("1.7.0") {
		t.Fatal("recorded build must be blocked")
	}
	if !a.upgradeBlocked("v1.7.0") {
		t.Fatal("a v-prefixed target must match the recorded block")
	}
	if a.upgradeBlocked("1.8.0") {
		t.Fatal("a different build must not be blocked")
	}
	// O3: the stored value is normalized too. The guard copies the pending
	// marker verbatim, so a release tag ("v1.7.0") must match a bare target.
	if err := os.WriteFile(a.stateFile(upgradeBlockedFile), []byte("v1.7.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.upgradeBlocked("1.7.0") || !a.upgradeBlocked("v1.7.0") {
		t.Fatal("a v-prefixed recorded block must match both spellings")
	}
}

// TestMarkPendingClearedOnRestart pins the handshake with the guard script: the
// agent records the build it is about to validate plus the pre-restart
// NRestarts baseline, and the next successful start clears it (meaning the
// guard must not roll back).
func TestMarkPendingClearedOnRestart(t *testing.T) {
	a := newGuardAgent(t)
	a.markUpgradePending("v1.7.0")
	raw, err := os.ReadFile(a.stateFile(upgradePendingFile))
	if err != nil {
		t.Fatalf("pending marker not written: %v", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Fatalf("pending marker = %q, want \"<version> <nrestarts>\"", raw)
	}
	if fields[0] != "v1.7.0" && fields[0] != "1.7.0" {
		t.Fatalf("pending marker version = %q, want the target version", fields[0])
	}
	if _, err := strconv.Atoi(fields[1]); err != nil {
		t.Fatalf("pending marker baseline = %q, want an integer NRestarts", fields[1])
	}
	a.clearUpgradePending()
	if _, err := os.Stat(a.stateFile(upgradePendingFile)); !os.IsNotExist(err) {
		t.Fatalf("pending marker should be gone, stat err = %v", err)
	}
}

// TestManualUpgradeClearsBlock pins the escape hatch: an explicit self-upgrade
// command from the panel overrides an earlier automatic rollback.
func TestManualUpgradeClearsBlock(t *testing.T) {
	a := newGuardAgent(t)
	if err := os.WriteFile(a.stateFile(upgradeBlockedFile), []byte("1.7.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := ""
	a.upgradeFn = func(ctx context.Context, ver string) error {
		called = ver
		return nil
	}
	res := a.runCommand(context.Background(), Command{ID: "c1", Action: cmdActionSelfUpgrade, Params: map[string]string{"version": "1.7.0"}})
	if !res.OK {
		t.Fatalf("manual upgrade failed: %s", res.Detail)
	}
	if called != "1.7.0" {
		t.Fatalf("upgradeFn called with %q, want 1.7.0", called)
	}
	if a.upgradeBlocked("1.7.0") {
		t.Fatal("a manual upgrade must clear the block")
	}
}

// TestAutoUpgradeSkipsBlockedBuild pins that the automatic path (heartbeat
// target_version) does NOT clear the block — only a human command does.
func TestAutoUpgradeSkipsBlockedBuild(t *testing.T) {
	a := newGuardAgent(t)
	if err := os.WriteFile(a.stateFile(upgradeBlockedFile), []byte("9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempted := false
	a.upgradeFn = func(ctx context.Context, ver string) error {
		attempted = true
		return nil
	}
	if !a.upgradeBlocked("9.9.9") {
		t.Fatal("fixture should be blocked")
	}
	if attempted {
		t.Fatal("upgradeFn must not run for a blocked build")
	}
}

// TestAutoFollowNormalizesVersionComparison pins F1: CI stamps the built
// version as "v1.6.0" while the gateway sends the normalized "1.6.0", so a raw
// comparison would upgrade, restart and get blocked forever. A heartbeat naming
// the same version in the other spelling must be a no-op; a genuinely newer
// version still upgrades exactly once.
func TestAutoFollowNormalizesVersionComparison(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "v1.6.0"

	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		if n == 1 {
			return http.StatusOK, HeartbeatReply{TargetVersion: "1.6.0"} // same build, bare form
		}
		return http.StatusOK, HeartbeatReply{TargetVersion: "1.7.0"}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	calls := 0
	ta.agent.upgradeFn = func(ctx context.Context, ver string) error {
		calls++
		if ver != "1.7.0" {
			t.Errorf("upgradeFn version = %q, want 1.7.0", ver)
		}
		return nil
	}

	if err := ta.agent.RunOnce(context.Background()); err != nil {
		t.Fatalf("same-version cycle = %v, want nil", err)
	}
	if calls != 0 {
		t.Fatalf("upgradeFn calls with target 1.6.0 vs running v1.6.0 = %d, want 0", calls)
	}
	if err := ta.agent.RunOnce(context.Background()); !errors.Is(err, errAgentUpgraded) {
		t.Fatalf("newer-version cycle = %v, want errAgentUpgraded", err)
	}
	if calls != 1 {
		t.Fatalf("upgradeFn calls for 1.7.0 = %d, want 1", calls)
	}
}

// TestRunCommandSelfUpgradeCurrentVersionIsNoop pins the other half of F1: an
// explicit self-upgrade command for the version that is already running (in
// either spelling) must not swap the binary or exit the process.
func TestRunCommandSelfUpgradeCurrentVersionIsNoop(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "v1.6.0"

	a := newGuardAgent(t)
	called := false
	a.upgradeFn = func(ctx context.Context, ver string) error {
		called = true
		return nil
	}
	res := a.runCommand(context.Background(), Command{
		ID: "up-current", Action: cmdActionSelfUpgrade, Params: map[string]string{"version": "1.6.0"},
	})
	if !res.OK {
		t.Fatalf("current-version self-upgrade = %+v, want OK (no-op)", res)
	}
	if res.Detail != "already running 1.6.0" {
		t.Errorf("detail = %q, want %q", res.Detail, "already running 1.6.0")
	}
	if called {
		t.Error("upgradeFn ran for the version already on disk")
	}
	if a.upgradedSnapshot() {
		t.Error("agent marked upgraded although nothing was swapped")
	}
}

// TestNoopUpgradeKeepsExecutingBatch pins N1: a no-op self-upgrade does not
// exit the process, so it must not stop the rest of the dispatched batch. The
// deregister queued right after it still has to run and trip the exit sentinel.
func TestNoopUpgradeKeepsExecutingBatch(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "v1.6.0"

	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{Commands: []Command{
			{ID: "up", Action: cmdActionSelfUpgrade, Params: map[string]string{"version": "1.6.0"}},
			{ID: "d1", Action: cmdActionDeregister},
		}}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	cfgPath := filepath.Join(t.TempDir(), "agent.json")
	if err := Save(cfgPath, cfg); err != nil {
		t.Fatalf("save agent config: %v", err)
	}
	ta := newTestAgent(t, cfg)
	ta.agent.configPath = cfgPath
	upgrades := 0
	ta.agent.upgradeFn = func(ctx context.Context, ver string) error {
		upgrades++
		return nil
	}

	err := ta.agent.RunOnce(context.Background())
	if !errors.Is(err, errAgentDeregistered) {
		t.Fatalf("RunOnce err = %v, want errAgentDeregistered (deregister must run after the no-op upgrade)", err)
	}
	if upgrades != 0 {
		t.Fatalf("upgradeFn calls = %d, want 0 (same-version upgrade is a no-op)", upgrades)
	}
	if !ta.agent.deregisteredSnapshot() {
		t.Error("deregister command after the no-op upgrade was dropped")
	}
	if execs := ta.snapshotExecs(); len(execs) != 2 {
		t.Fatalf("exec calls = %v, want the two deregister steps", execs)
	}
}

// TestPendingSurvivesConstruction pins F5's lifecycle half: New must not clear
// a pending marker, because a build that crashes before finishing its first
// cycle has to stay attributable to the guard. Simulating the crash by simply
// not running a cycle, the marker must still be there.
func TestPendingSurvivesConstruction(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	marker := filepath.Join(dir, upgradePendingFile)
	if err := os.WriteFile(marker, []byte("1.7.0 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newAgent(testConfig(t), nil, statePath)
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	if !a.pendingAtStart {
		t.Fatal("newAgent did not record the pending marker")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pending marker was removed at construction: %v", err)
	}
}

// TestSuccessfulCycleClearsPendingAndPrev pins the completion half: the marker
// and the .prev rollback copy survive only until the first fully successful
// cycle (heartbeat OK + state written); after that the build is proven and the
// guard stops watching it.
func TestSuccessfulCycleClearsPendingAndPrev(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	marker := filepath.Join(dir, upgradePendingFile)
	if err := os.WriteFile(marker, []byte("1.7.0 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	selfPath := filepath.Join(dir, "warden-cn-agent")
	if err := os.WriteFile(selfPath, []byte("new build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selfPath+".prev", []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}

	a, err := newAgent(cfg, nil, statePath)
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	a.dnsListen = "127.0.0.1:0"
	a.prober = &fakeProber{pingOK: true}
	a.selfPathFn = func() (string, error) { return selfPath, nil }

	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pending marker still present after a successful cycle: %v", err)
	}
	if _, err := os.Stat(selfPath + ".prev"); !os.IsNotExist(err) {
		t.Fatalf(".prev still present after a successful cycle: %v", err)
	}
	if a.pendingAtStart {
		t.Error("pendingAtStart still set after the validated cycle")
	}
}
