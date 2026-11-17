# Fix wave 2 report — CN agent review on 7ff3c69

- Worktree: `E:\Projects\warden-gateway-cn-agent` (branch `feature/cn-agent`)
- Base commit: `7ff3c69` (`feat: multi-version agent cache with rollback and loop guard`)
- Scope: all blocking F1–F5, important F7–F14, and the deferred CI-sidecar finding.
- Commit: the fix-wave commit containing this report (parent `7ff3c69`), message
  `fix: repair self-hosted binary routing, version pinning and rollback guard`.

## Blocking findings

### F1 (🔴) Version comparison was raw — fixed
- `internal/cnagent/agent.go`
  - Auto-follow now compares `normalizeVersion(reply.TargetVersion)` against
    `normalizeVersion(version.Version)`, so a CI-stamped `v1.6.0` agent and a
    gateway-sent `1.6.0` target are the same build.
  - `runCommand(cmdActionSelfUpgrade)` short-circuits when the requested version
    normalizes to the running version: `OK=true`,
    `Detail="already running <bare version>"`, no `upgradeFn` call, no swap.
  - `doUpgrade` also re-checks after resolving an empty target from the last
    reply, covering commands/legacy payloads without an explicit version.
- Tests (`internal/cnagent/upgradeguard_test.go`):
  - `TestAutoFollowNormalizesVersionComparison`: running `v1.6.0` + target
    `1.6.0` → `upgradeFn` not called; target `1.7.0` → called once and the
    upgraded sentinel is returned.
  - `TestRunCommandSelfUpgradeCurrentVersionIsNoop`: explicit command for the
    running build returns `already running 1.6.0` and does not swap.

### F2 (🔴) `DesiredVersion` conflated AutoFollow output with the operator pin — fixed
- `internal/panel/cnregistry.go`
  - `CNMachine` gained `PinnedVersion string` (persisted with Go field names,
    like the other fields). `DesiredVersion` is now documented and used as the
    computed display target only.
  - `UpdateReport(id, report, autoTarget) (effectiveTarget string, err error)`
    computes under `r.mu`: pin if non-empty, else `autoTarget` when
    `AutoFollow`, else `""`; stores it in `DesiredVersion` and returns it.
  - `SetDesiredVersion` replaced by `SetPinnedVersion(id, ver)`; empty clears.
    Both outcomes append a `pin` event (`<ver>` or `cleared`).
  - `SetAutoFollow(true)` clears `PinnedVersion` and records a `pin` event with
    the dropped value (or `cleared`).
- `internal/panel/cnagent.go`
  - `handleAgentHeartbeat` calls
    `UpdateReport(id, reportMap, AgentBinaryVersion())` and starts from the
    returned effective target. It no longer reads `machine.AutoFollow` or
    `machine.DesiredVersion` (F10 also fixed).
  - A dispatched `self-upgrade` command pins via `SetPinnedVersion`: its named
    version when present, otherwise `AgentBinaryVersion()`.
  - New `POST /api/cnmachines/{id}/pin` (`{"version": ""}` clears) validates
    non-empty versions against the cache and audits success and failure as
    `cn.pin`. Command-driven pinning continues through the command endpoint
    (`cn.command`).
- `internal/panel/web/app.js`
  - The upgrade picker gained “跟随网关版本（清除钉选）”, which calls the pin
    endpoint with an empty version instead of queueing a command.
- Tests (`internal/panel/cnagent_test.go`):
  - `TestAutoFollowAdvancesAcrossGatewayVersions`: auto target changes from
    `1.6.0` to `1.7.0` across two gateway versions.
  - `TestPinSurvivesHeartbeats`: pin `1.5.3` survives three beats.
  - `TestAutoFollowClearsPin`: enabling follow drops the pin and records it.
  - `TestUpgradeCommandPinsVersion`: dispatched upgrade pins and a later beat
    keeps it.
  - `TestPinEndpointAndAudit`: cached set, empty clear, uncached 400, unknown
    machine 404, audited.
  - `TestPinnedVersionOutranksAutoFollow` updated to `SetPinnedVersion`;
    clearing the pin restores the auto target.

### F3 (🔴) `/api/agent/binary/...` was unreachable end-to-end — fixed
- `internal/panel/panel.go`: route passes the already-stripped path to
  `handleAgentBinary(w, r, p)`.
- `internal/panel/agentbinary.go`: `handleAgentBinary` parses the routed `path`
  argument, never `r.URL.Path`.
- `internal/app/app.go`: `Handler` now routes `"/api/agent/binary/"` at the
  origin root to `a.panel.ServeHTTP`.
- Tests:
  - `internal/panel/agentbinary_test.go` table case `panel-prefixed`
    (`/warden/api/agent/binary/<v>/amd64`).
  - `internal/app/app_test.go` `TestHandlerAgentBinaryRootAndPrefixedRoutes`
    drives both the bare and `/warden`-prefixed URL through `app.Handler()`.

### F4 (🔴) Cache directory was literally named `latest` — fixed
- `bootstrap.sh`: new `cache_agent_version` resolves the concrete version before
  any directory is created:
  - explicit `VERSION` → `${VERSION#v}`;
  - `latest` → `"$BIN_PATH" version | awk 'NR == 1 {print $1}'`, stripped of a
    leading `v`;
  - fallback `$SCRIPT_DIR/VERSION`;
  - unresolved/`latest` → warning and `return 1`; no directory is created.
- `internal/panel/agentbinary.go`: `CachedAgentVersions` skips `latest`;
  `CachedAgentVersion` rejects empty/`latest`; `handleAgentBinary` rejects a
  `/agent/binary/latest/...` request. Command and pin validation go through
  `CachedAgentVersion`, so both reject `latest`.
- Tests: `TestAgentBinaryRejectsLatest` (stale `latest` dir is neither listed,
  accepted, nor served).

### F5 (🔴) Rollback guard counted the upgrade's own clean restart — fixed
- `internal/cnagent/upgradeguard.go`
  - `upgrade-pending` now contains `<version> <nrestarts>`; `nrestarts()` reads
    `systemctl show warden-cn-agent -p NRestarts --value` with a 2 s context and
    returns 0 on any failure.
  - `New` no longer clears the marker. It records `pendingAtStart` via
    `upgradePendingExists()`.
  - `RunOnce` clears the marker and removes `<executable>.prev` only after the
    first fully successful cycle (heartbeat OK and `writeState` returned nil)
    and only when the process started with a marker. A build that crashes
    before that keeps the marker for attribution.
  - `clearValidatedUpgrade` uses an injectable `selfPathFn` (default
    `os.Executable`).
- `bootstrap.sh` guard (`cn_agent_write_guard`)
  - Exits immediately when `upgrade-pending` is absent.
  - Parses the baseline (`awk '{print $2}'`, non-numeric → 0), computes
    `delta=nrestarts-baseline`, rolls back only when `delta -ge 2`, then
    converts the pending version into `upgrade-blocked` as before.
  - The old absolute `nrestarts -ge 2` condition is gone.
- F6 (the lifecycle question in the review) is handled in the same change:
  construction no longer deletes the marker; only the first successful cycle
  does.
- Tests:
  - `internal/config/config_test.go` `TestBootstrapRollbackGuardBaseline` locks
    the generated guard content: `upgrade-pending` presence check, baseline
    parse, `delta=$((nrestarts - baseline))`, `-ge 2`, blocked conversion; it
    also forbids the old absolute comparison.
  - `internal/cnagent/upgradeguard_test.go`:
    `TestPendingSurvivesConstruction` (marker survives construction and is still
    present when no cycle runs), `TestSuccessfulCycleClearsPendingAndPrev`
    (successful `RunOnce` removes both marker and `.prev`),
    `TestMarkPendingClearedOnRestart` updated to parse `<version> <baseline>`.

## Important findings

### F7 (🟡) Cache population gaps — fixed
- `cmd_install` Docker branch now calls `cache_agent_binaries || true` after
  `run_docker_container`.
- Binary install path: prefetch moved from between `install_scripts` and
  `install_cli` (inside the stopped-service window) to after `install_service`,
  i.e. after the gateway is running.
- `cmd_update` binary path: prefetch moved after `systemctl start "$SERVICE"`.

### F8 (🟡) Re-running cn-setup did not restart an existing agent — fixed
- `cn_setup_agent` stops an active `warden-cn-agent` before writing the new
  config/unit.
- `cn_agent_start_and_wait` now does `systemctl enable`, deletes
  `$CN_AGENT_STATE_DIR/state.json`, then `systemctl restart` (so an active unit
  is genuinely restarted). The wait still requires a fresh `state.json` with
  `"probe_result": "OK"`, which is now provably fresh because the old file was
  removed.
- `TestBootstrapRollbackGuardBaseline` also locks the enable/restart/state-file
  sequence and forbids `enable --now` in that function.

### F9 (🟡) Whole-file reads per unauthenticated request — fixed
- `handleAgentBinary` opens the file, `Stat`s it, sets the explicit
  Content-Type/nosniff/no-store, then uses `http.ServeContent` (streaming,
  Range, HEAD, Last-Modified). 404 semantics/message are unchanged.
- Test: `TestAgentBinaryStreamsWithLastModified` asserts `Last-Modified`,
  `Accept-Ranges: bytes`, and a `206` Range response.

### F10 (🟡) Live machine-field data race — fixed
- Covered by F2: the effective target is computed inside `UpdateReport` under
  the registry lock; the heartbeat handler no longer reads
  `machine.AutoFollow`/`machine.DesiredVersion`.

### F11 (🟡) “待升级/已钉版本” badge always on — fixed
- `buildReport` now reports `AgentVersion: normalizeVersion(version.Version)`
  (bare, no commit suffix).
- `newCNMachineRow` normalizes the reported version defensively
  (`normalizeReportedAgentVersion`, which also handles old `v1.6.0 (sha)`
  reports).
- `app.js` `cnNormVersion` normalizes both running and target versions before
  display/comparison; the picker’s “（正在运行）” now matches.
- Tests: `TestCNMachineRowNormalizesReportedVersion` and the existing
  `TestBuildReportFillsWireFields` assertion on the bare running version.

### F12 (🟢) Guard-code nits — fixed
- `clearUpgradeBlock` reads the blocked version before removing the file and
  logs that value.
- Dead `upgradeStamp()` deleted.

### F13 (🟢) Enroll limiter — fixed
- `New` initializes `enrollLimiter`; the lazy assignment was removed.
- Limit raised to 10/min/IP (`cnEnrollRateMax = 10`).
- 429 now carries `Retry-After: 60`.
- `TestAgentEnrollRateLimited` updated to 10 allowed / 11th 429 and asserts the
  header.

### F14 (🟢) cn_prune_agent_cache fallback unreachable — fixed
- Prune now probes `find "$root" -maxdepth 0 -printf ''` explicitly and only
  uses the `-printf` pipeline when supported; otherwise it falls back to
  `find ... | sed | sort`. Locked by `TestBootstrapRollbackGuardBaseline`.

## Deferred finding (fixed now)

### `.sha256` sidecars were self-generated at the gateway — fixed
- `bootstrap.sh cache_agent_binaries` now downloads `$base/$asset.sha256` from
  the same CI release as the binary, lowercases both sides
  (`tr 'A-F' 'a-f'`), verifies the downloaded bytes, and caches the CI-published
  sidecar verbatim (hash-only, one line). Missing sidecar, invalid format, or a
  mismatch skips that arch with a warning instead of caching unverifiable
  bytes.
- Locked by `TestBootstrapRollbackGuardBaseline`, which asserts the cache
  function references `$asset.sha256`, `sha256sum`, and writes the downloaded
  sidecar path.

## Interactions resolved

- F1 ↔ F5: the F1 normalized comparison prevents an upgraded build from
  re-upgrading to itself, which is what allows F5’s “clear pending only after a
  successful cycle” without an upgrade loop.
- F2 ↔ F10: computing the effective target in `UpdateReport` both gives the
  heartbeat its reply and removes the unlocked reads flagged by F10.
- F2 ↔ F11: the pin endpoint/command store bare normalized versions, and both
  the backend row and the UI normalize defensively, so pins, auto targets and
  old reports compare cleanly.

## Verification (all green)

Commands and results:

```
$ gofmt -l cmd internal
(no output)

$ go vet ./...
(no output)

$ bash -n bootstrap.sh
(no output)

$ node --check internal/panel/web/app.js
(no output)

$ go test ./... -count=1
?   	github.com/tktogether/warden-gateway/cmd/warden-cn-agent	[no test files]
?   	github.com/tktogether/warden-gateway/cmd/warden-gateway	[no test files]
ok  	github.com/tktogether/warden-gateway/internal/app	0.226s
ok  	github.com/tktogether/warden-gateway/internal/auth	0.743s
ok  	github.com/tktogether/warden-gateway/internal/backup	0.524s
ok  	github.com/tktogether/warden-gateway/internal/cnagent	3.792s
ok  	github.com/tktogether/warden-gateway/internal/config	0.465s
ok  	github.com/tktogether/warden-gateway/internal/diagnostics	0.407s
ok  	github.com/tktogether/warden-gateway/internal/dnsproxy	5.761s
ok  	github.com/tktogether/warden-gateway/internal/gateway	1.543s
ok  	github.com/tktogether/warden-gateway/internal/githubproxy	0.539s
?   	github.com/tktogether/warden-gateway/internal/logging	[no test files]
ok  	github.com/tktogether/warden-gateway/internal/panel	1.788s
ok  	github.com/tktogether/warden-gateway/internal/singbox	10.216s
ok  	github.com/tktogether/warden-gateway/internal/telemetry	0.440s
?   	github.com/tktogether/warden-gateway/internal/ui	[no test files]
?   	github.com/tktogether/warden-gateway/internal/version	[no test files]

$ bash toolbox.sh self-test
  ...
  [信息] 语法、版本和模块清单检查通过。
```

Targeted new/updated tests were also run with `-v`:
`TestAutoFollowNormalizesVersionComparison`,
`TestRunCommandSelfUpgradeCurrentVersionIsNoop`,
`TestPendingSurvivesConstruction`,
`TestSuccessfulCycleClearsPendingAndPrev`,
`TestAgentBinaryRejectsLatest`,
`TestAgentBinaryStreamsWithLastModified`,
`TestAgentBinaryServesCachedBuild/panel-prefixed`,
`TestHandlerAgentBinaryRootAndPrefixedRoutes`,
`TestAutoFollowAdvancesAcrossGatewayVersions`,
`TestPinSurvivesHeartbeats`,
`TestAutoFollowClearsPin`,
`TestUpgradeCommandPinsVersion`,
`TestPinEndpointAndAudit`,
`TestCNMachineRowNormalizesReportedVersion`,
`TestBootstrapRollbackGuardBaseline`,
`TestAgentEnrollRateLimited` — all PASS.

## Concerns / notes

- None blocking. Two operational notes:
  1. `cache_agent_binaries` intentionally skips a Docker install performed from a
     single downloaded script when neither `$BIN_PATH` nor `$SCRIPT_DIR/VERSION`
     exists; it warns instead of creating a `latest` directory. Re-running
     install/update from a release bundle or after the gateway version file
     exists populates the cache.
  2. The guard threshold follows the review exactly: `current - baseline >= 2`
     means the upgrade’s own clean restart (delta 1) cannot roll back, while the
     next restart after a crash (delta 2) does.
