package cnagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The systemd ExecStartPre rollback guard (bootstrap.sh cn_agent_write_guard)
// restores <bin>.prev when the freshly installed build crashes twice in a row.
// Without a record of *which* build failed, the recovered agent would see the
// same target_version on its next heartbeat and try the identical upgrade
// again — a crash loop that no amount of systemd restarts can escape.
//
// Two plain-text marker files under the state dir close that loop:
//
//	upgrade-pending  written by the agent right before it exits for a restart:
//	                 "<version> <nrestarts>" — the build the guard is about to
//	                 validate and the unit's restart counter at that moment.
//	                 The baseline matters because the upgrade's own clean
//	                 exit-0 restart increments NRestarts; the guard rolls back
//	                 only when current-baseline >= 3 (the clean restart plus two
//	                 consecutive crashes), never on an absolute count. When the
//	                 counter cannot be read the baseline is -1 ("unknown"): the
//	                 guard then requires current >= 3, so a unit with pre-upgrade
//	                 restarts is not rolled back on a healthy build.
//	upgrade-blocked  written by the guard when it rolls that build back.
//
// The agent skips automatic upgrade attempts toward a blocked build and reports
// the block in its heartbeat; an explicit self-upgrade command from the panel
// (a human decision) clears it.
const (
	upgradePendingFile = "upgrade-pending"
	upgradeBlockedFile = "upgrade-blocked"
)

// stateFile resolves a marker path beneath the agent's state directory.
func (a *Agent) stateFile(name string) string {
	return filepath.Join(filepath.Dir(a.statePath), name)
}

// upgradePendingExists reports whether a pending marker was on disk before this
// process began. New records it so a later successful cycle can clear the
// marker without clearing one written by an upgrade that happened since.
func (a *Agent) upgradePendingExists() bool {
	_, err := os.Stat(a.stateFile(upgradePendingFile))
	return err == nil
}

// markUpgradePending records the build about to be validated by the guard and
// the unit's restart count before the expected clean restart. An unreadable
// counter is recorded as -1 ("unknown") so the guard knows not to subtract a
// baseline it never had; it then requires the absolute counter to prove
// repeated failures (>= 3) before rolling anything back.
func (a *Agent) markUpgradePending(ver string) {
	path := a.stateFile(upgradePendingFile)
	payload := strings.TrimSpace(ver) + " " + strconv.Itoa(nrestarts()) + "\n"
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		a.log.Warnf("record pending upgrade %s: %v", ver, err)
	}
}

// clearUpgradePending removes the pending marker: the new build proved healthy
// and the guard has nothing to roll back.
func (a *Agent) clearUpgradePending() {
	if err := os.Remove(a.stateFile(upgradePendingFile)); err != nil && !os.IsNotExist(err) {
		a.log.Warnf("clear pending upgrade marker: %v", err)
	}
}

// clearValidatedUpgrade runs after the first fully successful cycle of a
// process that started with a pending marker: the build has demonstrably
// worked, so drop the marker and the rollback copy. Clearing .prev here (not
// at construction) is what lets a build that crashes during its very first
// cycle still be rolled back and attributed.
func (a *Agent) clearValidatedUpgrade() {
	a.clearUpgradePending()
	self := ""
	if a.selfPathFn != nil {
		if path, err := a.selfPathFn(); err == nil {
			self = path
		}
	} else if path, err := os.Executable(); err == nil {
		self = path
	}
	if self != "" {
		if err := os.Remove(self + ".prev"); err != nil && !os.IsNotExist(err) {
			a.log.Warnf("remove validated rollback copy %s.prev: %v", self, err)
		}
	}
	a.pendingAtStart = false
}

// nrestarts reads the unit's NRestarts counter. Any failure (no systemd, no
// unit, non-Linux test host) yields -1: the pending marker then records an
// "unknown" baseline, and the guard requires the absolute counter to be high
// enough (>= 3) instead of subtracting a delta it cannot trust.
func nrestarts() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", cnAgentService, "-p", "NRestarts", "--value").Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// blockedUpgrade reports the build the guard rolled back, if any.
func (a *Agent) blockedUpgrade() string {
	raw, err := os.ReadFile(a.stateFile(upgradeBlockedFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// upgradeBlocked reports whether ver is the build that already failed to start
// on this machine. Both sides are normalized: the guard records whatever the
// pending marker held (release tags carry a "v"), and callers pass either form.
func (a *Agent) upgradeBlocked(ver string) bool {
	blocked := normalizeVersion(a.blockedUpgrade())
	return blocked != "" && blocked == normalizeVersion(ver)
}

// clearUpgradeBlock removes the block, recording why: a manual upgrade command
// is the operator overriding the earlier automatic rollback.
func (a *Agent) clearUpgradeBlock(reason string) {
	blocked := a.blockedUpgrade()
	path := a.stateFile(upgradeBlockedFile)
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			a.log.Warnf("clear upgrade block: %v", err)
		}
		return
	}
	a.log.Infof("cleared upgrade block for %s (%s)", blocked, reason)
}

// normalizeVersion trims the leading "v" so "v1.6.0" and "1.6.0" compare equal.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}
