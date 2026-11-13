package singbox

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// withRestartHooks replaces the systemd touchpoints for the duration of a test.
func withRestartHooks(t *testing.T, unitPath string, restart func(string) error, wait func(string, time.Duration) (string, string)) *int {
	t.Helper()
	resets := 0
	oldPath, oldRestart, oldWait, oldReset := unitPathHook, restartUnitHook, waitActiveHook, resetFailedHook
	unitPathHook = func(string) string { return unitPath }
	if restart != nil {
		restartUnitHook = restart
	}
	if wait != nil {
		waitActiveHook = wait
	}
	resetFailedHook = func() { resets++ }
	t.Cleanup(func() {
		unitPathHook, restartUnitHook, waitActiveHook, resetFailedHook = oldPath, oldRestart, oldWait, oldReset
	})
	return &resets
}

func TestRestartRequiresInstalledUnit(t *testing.T) {
	m, _ := newTestManager(t)
	withRestartHooks(t, "", nil, nil)
	if err := m.Restart(); err == nil || !strings.Contains(err.Error(), "无法重启") {
		t.Fatalf("Restart without unit = %v", err)
	}
}

func TestRestartClearsFailedStateAndWaitsForActive(t *testing.T) {
	m, _ := newTestManager(t)
	resets := withRestartHooks(t, "/etc/systemd/system/sing-box.service",
		func(string) error { return nil },
		func(string, time.Duration) (string, string) { return "active", "running" })
	if err := m.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if *resets != 1 {
		t.Fatalf("resetFailed calls = %d, want 1", *resets)
	}
}

func TestRestartRetriesBecauseOfPortReuse(t *testing.T) {
	m, _ := newTestManager(t)
	attempts := 0
	withRestartHooks(t, "/etc/systemd/system/sing-box.service",
		func(string) error {
			attempts++
			if attempts < 2 {
				return errors.New("job for sing-box.service failed")
			}
			return nil
		},
		func(string, time.Duration) (string, string) { return "active", "running" })
	if err := m.Restart(); err != nil {
		t.Fatalf("Restart after transient failure: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("restart attempts = %d, want 2", attempts)
	}
}

func TestRestartFailsWhenUnitNeverBecomesActive(t *testing.T) {
	m, _ := newTestManager(t)
	withRestartHooks(t, "/etc/systemd/system/sing-box.service",
		func(string) error { return nil },
		func(string, time.Duration) (string, string) { return "failed", "start-limit-hit" })
	err := m.Restart()
	if err == nil {
		t.Fatalf("Restart reported success for a failed unit")
	}
	if !strings.Contains(err.Error(), "start-limit-hit") {
		t.Fatalf("error does not mention the unit state: %v", err)
	}
}

func TestRestartGivesUpAfterBoundedAttempts(t *testing.T) {
	m, _ := newTestManager(t)
	attempts := 0
	withRestartHooks(t, "/etc/systemd/system/sing-box.service",
		func(string) error { attempts++; return errors.New("exit status 1") },
		func(string, time.Duration) (string, string) { return "active", "running" })
	if err := m.Restart(); err == nil {
		t.Fatalf("Restart reported success while systemctl kept failing")
	}
	if attempts != restartAttempts {
		t.Fatalf("restart attempts = %d, want %d", attempts, restartAttempts)
	}
}
