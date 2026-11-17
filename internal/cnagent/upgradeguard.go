package cnagent

import (
	"os"
	"path/filepath"
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
//	upgrade-pending  written by the agent right before it exits for a restart,
//	                 naming the build the guard is about to validate.
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

// markUpgradePending records the build about to be validated by the guard.
func (a *Agent) markUpgradePending(ver string) {
	path := a.stateFile(upgradePendingFile)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(ver)+"\n"), 0o644); err != nil {
		a.log.Warnf("record pending upgrade %s: %v", ver, err)
	}
}

// clearUpgradePending removes the pending marker: the new build started fine,
// so the guard has nothing to roll back.
func (a *Agent) clearUpgradePending() {
	if err := os.Remove(a.stateFile(upgradePendingFile)); err != nil && !os.IsNotExist(err) {
		a.log.Warnf("clear pending upgrade marker: %v", err)
	}
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
// on this machine.
func (a *Agent) upgradeBlocked(ver string) bool {
	blocked := a.blockedUpgrade()
	return blocked != "" && blocked == normalizeVersion(ver)
}

// clearUpgradeBlock removes the block, recording why: a manual upgrade command
// is the operator overriding the earlier automatic rollback.
func (a *Agent) clearUpgradeBlock(reason string) {
	path := a.stateFile(upgradeBlockedFile)
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			a.log.Warnf("clear upgrade block: %v", err)
		}
		return
	}
	a.log.Infof("cleared upgrade block for %s (%s)", a.blockedUpgrade(), reason)
}

// normalizeVersion trims the leading "v" so "v1.6.0" and "1.6.0" compare equal.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// upgradeStamp is the version a fresh process reports as "running since".
func upgradeStamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
