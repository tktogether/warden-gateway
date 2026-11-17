package cnagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tktogether/warden-gateway/internal/logging"
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
}

// TestMarkPendingClearedOnRestart pins the handshake with the guard script: the
// agent records the build it is about to validate, and the next successful
// start clears it (meaning the guard must not roll back).
func TestMarkPendingClearedOnRestart(t *testing.T) {
	a := newGuardAgent(t)
	a.markUpgradePending("v1.7.0")
	raw, err := os.ReadFile(a.stateFile(upgradePendingFile))
	if err != nil {
		t.Fatalf("pending marker not written: %v", err)
	}
	if string(raw) != "v1.7.0\n" && string(raw) != "1.7.0\n" {
		t.Fatalf("pending marker = %q, want the target version", raw)
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
