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
	oldSettle, oldCount := settleHook, unitRestartCountHook
	unitPathHook = func(string) string { return unitPath }
	if restart != nil {
		restartUnitHook = restart
	}
	if wait != nil {
		waitActiveHook = wait
	}
	resetFailedHook = func() { resets++ }
	settleHook = func(string, int) error { return nil }
	unitRestartCountHook = func(string) int { return 0 }
	t.Cleanup(func() {
		unitPathHook, restartUnitHook, waitActiveHook, resetFailedHook = oldPath, oldRestart, oldWait, oldReset
		settleHook, unitRestartCountHook = oldSettle, oldCount
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

func TestRestartDetectsCrashAfterStartup(t *testing.T) {
	m, _ := newTestManager(t)
	attempts := 0
	withRestartHooks(t, "/etc/systemd/system/sing-box.service",
		func(string) error { attempts++; return nil },
		func(string, time.Duration) (string, string) { return "active", "running" })
	settleHook = func(string, int) error { return errors.New("sing-box 启动后随即退出：failed failed") }
	err := m.Restart()
	if err == nil {
		t.Fatalf("Restart reported success for a process that crashed after startup")
	}
	if attempts != restartAttempts {
		t.Fatalf("restart attempts = %d, want %d", attempts, restartAttempts)
	}
}

func TestSettleActiveRejectsUnitThatDied(t *testing.T) {
	oldWait, oldState, oldCount := restartSettleWait, stateHook, unitRestartCountHook
	t.Cleanup(func() { restartSettleWait, stateHook, unitRestartCountHook = oldWait, oldState, oldCount })
	restartSettleWait = 0
	stateHook = func(string) (string, string) { return "failed", "start-limit-hit" }
	if err := settleActive("sing-box.service", 0); err == nil {
		t.Fatalf("settleActive accepted a failed unit")
	}
}

func TestSettleActiveRejectsAutomaticRestart(t *testing.T) {
	oldWait, oldState, oldCount := restartSettleWait, stateHook, unitRestartCountHook
	t.Cleanup(func() { restartSettleWait, stateHook, unitRestartCountHook = oldWait, oldState, oldCount })
	restartSettleWait = 0
	stateHook = func(string) (string, string) { return "active", "running" }
	unitRestartCountHook = func(string) int { return 3 }
	if err := settleActive("sing-box.service", 2); err == nil {
		t.Fatalf("settleActive ignored the unit restart counter going up")
	}
}

func TestSettleActiveAcceptsStableUnit(t *testing.T) {
	oldWait, oldState, oldCount := restartSettleWait, stateHook, unitRestartCountHook
	t.Cleanup(func() { restartSettleWait, stateHook, unitRestartCountHook = oldWait, oldState, oldCount })
	restartSettleWait = 0
	stateHook = func(string) (string, string) { return "active", "running" }
	unitRestartCountHook = func(string) int { return 2 }
	if err := settleActive("sing-box.service", 2); err != nil {
		t.Fatalf("settleActive rejected a stable unit: %v", err)
	}
}

func TestStripANSIKeepsJournalText(t *testing.T) {
	raw := "\x1b[31mFATAL\x1b[0m[0000] start service: bind: address already in use"
	got := stripANSI(raw)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("stripANSI kept escapes: %q", got)
	}
	if !strings.HasPrefix(got, "FATAL[0000] start service") {
		t.Fatalf("stripANSI mangled the text: %q", got)
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
