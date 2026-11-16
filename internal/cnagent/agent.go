package cnagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/dnsproxy"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/version"
)

// errAgentDeregistered is the sentinel returned by RunOnce after a successful
// deregister command. Run turns it into a clean nil exit so systemd sees a
// normal stop of the (just disabled) unit instead of a failure.
var errAgentDeregistered = errors.New("cnagent: agent deregistered")

const (
	// DefaultStatePath is where the agent persists its per-cycle state file
	// (0644, world-readable on purpose so cn-check can read it without
	// privileges).
	DefaultStatePath = "/var/lib/warden-gateway-cn/state.json"

	// defaultConfigPath is where cmd wiring loads agent.json from by default;
	// deregister rewrites this file to clear the machine token.
	defaultConfigPath = "/etc/warden-gateway-cn/agent.json"

	// resolvConfPath is the resolver configuration deregister restores to
	// public-only nameservers.
	resolvConfPath = "/etc/resolv.conf"

	// fallbackPublicDNS is the well-known public resolver written alongside
	// the configured upstream when resolv.conf is restored during deregister.
	fallbackPublicDNS = "119.29.29.29"

	// cnAgentService is the systemd unit deregister stops and disables.
	cnAgentService = "warden-cn-agent"

	// watchdogPing is the sd_notify payload sent once per cycle through the
	// notifyFn hook (wired to SDNotify by Task 8).
	watchdogPing = "WATCHDOG=1"

	// heartbeatHTTPTimeout bounds one heartbeat POST so a hanging gateway can
	// never stall the run loop.
	heartbeatHTTPTimeout = 8 * time.Second

	// workloadDiscoverTimeout bounds docker workload discovery per cycle
	// (Task 5 review: the nil-client path has no default timeout, so the run
	// loop supplies the deadline itself).
	workloadDiscoverTimeout = 8 * time.Second
)

// Command actions the gateway can queue for the agent.
const (
	cmdActionSyncDomains = "sync-domains"
	cmdActionSelfCheck   = "self-check"
	cmdActionSelfUpgrade = "self-upgrade"
	cmdActionDeregister  = "deregister"
)

// Agent drives one full cycle per heartbeat interval: probe the gateway, sync
// the degraded verdict into the DNS proxy, send the heartbeat, apply the
// reply (domain payload, queued commands), and persist the state file.
type Agent struct {
	cfg *Config
	log *logging.Logger

	// --- Injection points for tests and later tasks (same package) ---
	//
	// statePath is the per-cycle state file location (mode 0644).
	statePath string
	// configPath is the agent.json location deregister rewrites to clear the
	// token; cmd wiring overrides it when --config points elsewhere.
	configPath string
	// dnsListen is the address handed to dnsproxy as cfg.Listen. It defaults
	// from cfg.DNSListen ("198.18.0.1:53" in production); tests set
	// "127.0.0.1:0" and resolveListen() pins the ephemeral port once so
	// rebuilds keep serving the same address.
	dnsListen string
	// prober performs the gateway probe round; production uses realProber{},
	// tests inject a scripted fake (ruling 2).
	prober prober
	// notifyFn is the sd_notify hook invoked once per cycle with
	// watchdogPing. Default: no-op. Task 8 wires it to SDNotify so the
	// systemd watchdog (WatchdogSec=30) sees liveness every cycle; do not
	// remove or move this call when wiring Task 8.
	notifyFn func(state string)
	// upgradeFn is the self-upgrade dispatch (Task 7). New wires it to
	// SelfUpgrade with the executable path resolved at call time; only
	// hand-built Agent structs (tests) carry a nil upgradeFn, which makes
	// the command report OK=false "not wired yet".
	upgradeFn func(ctx context.Context, version string) error
	// checkFn is the self-check dispatch (Task 8). nil (default) makes the
	// command report OK=false "not wired yet".
	checkFn func() map[string]interface{}
	// execFn runs the deregister shell-outs (resolv.conf restore, systemctl
	// disable). Default: os/exec. Tests inject a recorder so systemctl is
	// never really executed (ruling 4).
	execFn func(name string, args ...string) error
	// httpClient posts heartbeats; bounded by heartbeatHTTPTimeout.
	httpClient *http.Client

	// --- Run state, guarded by mu ---
	mu            sync.Mutex
	proxy         *dnsproxy.Proxy // current DNS proxy (nil before first build)
	domains       []string        // locally applied split-domain list
	domainsHash   string          // hash of the applied list, echoed in every report
	degraded      bool            // verdict of the last probe round
	targetVersion string          // gateway-requested agent version from the latest reply
	lastCommand   *CommandResult  // result of the last executed command, reported next cycle
	lastError     string          // last local failure (proxy build), surfaced in the state file
	applies       int             // count of successful proxy builds (tests pin "no rebuild")
	deregistered  bool            // set once deregister completed; RunOnce returns the sentinel
}

// New builds an agent from the loaded configuration. It never dials anything:
// the DNS proxy is built lazily by the first RunOnce cycle so that a machine
// boots into a working DNS-forwarding state even if the gateway is unreachable
// (ensureProxy forwards everything upstream until the first payload arrives).
func New(cfg *Config, log *logging.Logger) (*Agent, error) {
	if cfg == nil {
		return nil, errors.New("cnagent: config is nil")
	}
	listen := strings.TrimSpace(cfg.DNSListen)
	if listen == "" {
		listen = DefaultDNSListen
	}
	return &Agent{
		cfg:        cfg,
		log:        log,
		statePath:  DefaultStatePath,
		configPath: defaultConfigPath,
		dnsListen:  listen,
		prober:     realProber{},
		notifyFn:   func(string) {},
		execFn:     func(name string, args ...string) error { return exec.Command(name, args...).Run() },
		// Task 7 wiring: dispatch self-upgrade into SelfUpgrade. The binary
		// path is resolved at CALL time (the path is stable, but tests may
		// replace the executable between construction and the command), the
		// arch is the build target, and the nil client makes SelfUpgrade use
		// its own 60s download budget instead of the 8s heartbeat client.
		upgradeFn: func(ctx context.Context, ver string) error {
			selfPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cnagent upgrade: resolve executable path: %w", err)
			}
			return SelfUpgrade(ctx, cfg, ver, runtime.GOARCH, selfPath, nil)
		},
		httpClient: &http.Client{Timeout: heartbeatHTTPTimeout},
	}, nil
}

// RunOnce executes one complete cycle: probe → dns sync → heartbeat → apply
// reply → write state file. It returns nil unless the agent deregistered
// (errAgentDeregistered); every other failure is logged and retried next cycle
// by Run.
func (a *Agent) RunOnce(ctx context.Context) error {
	// systemd watchdog ping, once per cycle. Task 8 replaces the default
	// no-op with SDNotify; keep this exactly once per cycle so the
	// WatchdogSec=30 budget holds.
	if a.notifyFn != nil {
		a.notifyFn(watchdogPing)
	}

	// 1. Probe the gateway (bounded timeouts live in probe.go).
	pr := ProbeGatewayWith(ctx, a.cfg.GatewayIP, time.Now, a.prober)
	degraded := pr.Result != ProbeOK

	// 2. DNS sync. Locked rule (no hysteresis): probe != OK means degraded
	// now, probe == OK means not degraded. Then make sure a proxy exists at
	// all — the first cycle after boot builds it from the local cache so the
	// machine's resolver keeps answering even before the first reply.
	a.mu.Lock()
	a.degraded = degraded
	p := a.proxy
	a.mu.Unlock()
	if p != nil {
		p.SetDegraded(degraded)
	}
	a.ensureProxy()

	// 3. Heartbeat.
	report := a.buildReport(ctx, pr, degraded)
	reply, err := SendHeartbeat(ctx, a.cfg, report, a.httpClient)
	if err != nil {
		// Locked (Review Focus #4 / spec §8 safety net #6): a failed
		// heartbeat produces no local action at all — no state file write,
		// no DNS change. The machine keeps running on its current state
		// until the next successful cycle.
		a.log.Errorf("heartbeat failed, keeping local state unchanged: %v", err)
		return nil
	}
	a.mu.Lock()
	a.targetVersion = reply.TargetVersion
	a.mu.Unlock()

	// 4. Apply the reply's domain payload. Locked order: old proxy Close() →
	// dnsproxy.New(...) → Serve(); a failure is recorded for the state file
	// and retried next cycle. Only a changed hash triggers a rebuild.
	if reply.Domains != nil && reply.Domains.Hash != a.domainsHashSnapshot() {
		_ = a.buildProxy(reply.Domains.Hash, reply.Domains.Domains)
	}

	// 5. Execute queued commands sequentially; the last result is reported
	// with the next heartbeat (Report.LastCommand).
	for _, cmd := range reply.Commands {
		res := a.runCommand(ctx, cmd)
		a.mu.Lock()
		a.lastCommand = &res
		a.mu.Unlock()
		if res.OK && cmd.Action == cmdActionDeregister {
			break // the process is about to exit; nothing after teardown matters
		}
	}

	// 6. Persist the cycle. Failures here are logged, not fatal: the DNS
	// proxy must never go down because a state write failed.
	a.writeState(pr, degraded)

	if a.deregisteredSnapshot() {
		return errAgentDeregistered
	}
	return nil
}

// Run drives cycles forever: the first cycle starts immediately (bootstrap
// waits for state.json to appear), then every wait is HeartbeatSeconds scaled
// by a ±20% jitter so fleets of CN machines do not hammer the gateway in
// lockstep. It returns nil on ctx cancellation and after a deregister, and
// keeps looping through any other cycle error.
func (a *Agent) Run(ctx context.Context) error {
	base := a.cfg.HeartbeatSeconds
	if base <= 0 {
		base = DefaultHeartbeatSeconds
	}
	for {
		err := a.RunOnce(ctx)
		if errors.Is(err, errAgentDeregistered) {
			a.log.Infof("agent deregistered, exiting")
			a.shutdown()
			return nil
		}
		if err != nil {
			a.log.Errorf("cycle failed: %v", err)
		}

		timer := time.NewTimer(jitteredInterval(base))
		select {
		case <-ctx.Done():
			timer.Stop()
			a.shutdown()
			return nil
		case <-timer.C:
		}
	}
}

// jitteredInterval scales base seconds by a random factor in [0.8, 1.2].
func jitteredInterval(base int) time.Duration {
	if base <= 0 {
		base = DefaultHeartbeatSeconds
	}
	factor := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(base) * factor * float64(time.Second))
}

// shutdown closes the DNS proxy (used on clean exits).
func (a *Agent) shutdown() {
	a.mu.Lock()
	p := a.proxy
	a.mu.Unlock()
	if err := p.Close(); err != nil {
		a.log.Warnf("close dns proxy: %v", err)
	}
}

// buildReport assembles the heartbeat request body for this cycle.
func (a *Agent) buildReport(ctx context.Context, pr ProbeResult, degraded bool) *Report {
	a.mu.Lock()
	hash := a.domainsHash
	last := a.lastCommand
	a.mu.Unlock()

	boot, uptime := bootInfo()

	// Workload discovery gets its own deadline (ruling 8): the nil-client
	// path in DiscoverWorkloads has no default timeout, so the run loop
	// supplies one.
	wlCtx, cancel := context.WithTimeout(ctx, workloadDiscoverTimeout)
	defer cancel()
	workloads, err := DiscoverWorkloads(wlCtx, dockerSocketPath, nil)
	if err != nil {
		a.log.Warnf("workload discovery: %v", err)
		workloads = nil
	}

	return &Report{
		MachineID:     a.cfg.MachineID,
		Hostname:      a.cfg.Hostname,
		AgentVersion:  version.String(),
		BootTime:      boot,
		UptimeSeconds: uptime,
		Probe: Probe{
			Result:    pr.Result,
			CheckedAt: pr.CheckedAt.UTC().Format(time.RFC3339),
			Detail:    pr.Detail,
		},
		DNS: DNS{
			Listen:   a.boundDNSListen(),
			Upstream: a.cfg.Upstream,
			Degraded: degraded,
		},
		DomainsHash: hash,
		System:      sampleSystem(),
		Workloads:   workloads,
		LastCommand: last,
	}
}

// ensureProxy builds the DNS proxy from the locally cached domain list when
// none is currently serving (first cycle, or the previous build failed).
func (a *Agent) ensureProxy() {
	a.mu.Lock()
	p := a.proxy
	a.mu.Unlock()
	if p != nil && p.Status().Running {
		return
	}
	a.mu.Lock()
	domains, hash := a.domains, a.domainsHash
	a.mu.Unlock()
	_ = a.buildProxy(hash, domains)
}

// buildProxy (re)builds the DNS proxy. The order is locked by ruling 6:
// old proxy Close() → dnsproxy.New(...) → Serve(). A failure is recorded in
// the state file (LastError) and retried next cycle; the local cache is only
// updated on success.
func (a *Agent) buildProxy(hash string, domains []string) error {
	a.mu.Lock()
	old := a.proxy
	a.mu.Unlock()
	if old != nil {
		if err := old.Close(); err != nil {
			a.log.Warnf("close old dns proxy: %v", err)
		}
	}

	dcfg := config.DNSConfig{
		Enabled:   true,
		Listen:    a.resolveListen(),
		GatewayIP: a.cfg.GatewayIP,
		Domains:   domains,
		Upstream:  a.cfg.Upstream,
	}
	p, err := dnsproxy.New(dcfg, nil, a.log) // stats nil: the agent does not feed the gateway telemetry store
	if err != nil {
		a.recordError(fmt.Errorf("build dns proxy: %w", err))
		return err
	}
	// The degraded verdict must survive rebuilds, otherwise applying a new
	// domain list during an outage would start answering split queries with
	// an unreachable gateway address.
	p.SetDegraded(a.degradedSnapshot())
	if err := p.Serve(); err != nil {
		a.recordError(err)
		return err
	}

	a.mu.Lock()
	a.proxy = p
	a.domains = append([]string(nil), domains...)
	a.domainsHash = hash
	a.applies++
	a.lastError = ""
	a.mu.Unlock()
	return nil
}

// resolveListen returns the address to bind the DNS proxy on. An explicitly
// requested ephemeral port (tests use 127.0.0.1:0) is resolved to a concrete
// port exactly once and cached, so proxy rebuilds keep serving the same
// address instead of hopping ports under the machine's resolv.conf.
func (a *Agent) resolveListen() string {
	a.mu.Lock()
	current := a.dnsListen
	a.mu.Unlock()
	host, port, err := net.SplitHostPort(current)
	if err != nil || port != "0" {
		return current
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
	if err != nil {
		return current
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	a.mu.Lock()
	a.dnsListen = addr
	a.mu.Unlock()
	return addr
}

// boundDNSListen returns the address the DNS proxy is actually serving on
// (the listener bound by Serve), falling back to the configured/resolved
// dnsListen field when nothing is running. The dnsListen FIELD doubles as the
// test hook: tests set it to "127.0.0.1:0" and resolveListen rewrites it to
// the concrete bound address once, so reading the field after a cycle yields
// the actual bound address.
func (a *Agent) boundDNSListen() string {
	a.mu.Lock()
	p := a.proxy
	a.mu.Unlock()
	if p != nil {
		if st := p.Status(); st.Running && st.Listen != "" {
			return st.Listen
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dnsListen
}

// runCommand executes one gateway command. sync-domains and deregister are
// handled fully here; self-upgrade and self-check dispatch through the
// upgradeFn/checkFn hooks that Tasks 7/8 wire (nil default reports
// "not wired yet").
func (a *Agent) runCommand(ctx context.Context, cmd Command) CommandResult {
	res := CommandResult{ID: cmd.ID, Action: cmd.Action}
	switch cmd.Action {
	case cmdActionSyncDomains:
		domains, hash := a.syncDomainTargets(cmd.Params)
		if hash == "" && len(domains) == 0 {
			// Nothing received from the gateway yet; the next heartbeat
			// reply carries the payload, so there is nothing to sync.
			res.OK = true
			res.Detail = "no split-domain list received yet"
			return res
		}
		if err := a.buildProxy(hash, domains); err != nil {
			res.OK = false
			res.Detail = err.Error()
			return res
		}
		res.OK = true
		res.Detail = fmt.Sprintf("applied %d domains (hash %s)", len(domains), hash)

	case cmdActionSelfUpgrade:
		if a.upgradeFn == nil {
			res.OK = false
			res.Detail = "not wired yet" // Task 7 wires upgradeFn
			return res
		}
		target := strings.TrimSpace(cmd.Params["version"])
		if target == "" {
			a.mu.Lock()
			target = a.targetVersion
			a.mu.Unlock()
		}
		if err := a.upgradeFn(ctx, target); err != nil {
			res.OK = false
			res.Detail = err.Error()
			return res
		}
		res.OK = true
		res.Detail = "upgrade to " + target + " started"

	case cmdActionSelfCheck:
		if a.checkFn == nil {
			res.OK = false
			res.Detail = "not wired yet" // Task 8 wires checkFn
			return res
		}
		m := a.checkFn()
		res.OK, _ = m["ok"].(bool)
		if data, err := json.Marshal(m); err == nil {
			res.Detail = string(data)
		} else {
			res.Detail = fmt.Sprintf("self-check completed (ok=%t)", res.OK)
		}

	case cmdActionDeregister:
		if err := a.deregister(); err != nil {
			res.OK = false
			res.Detail = err.Error()
			return res
		}
		res.OK = true
		res.Detail = "resolv.conf restored, " + cnAgentService + " disabled, token cleared"

	default:
		res.OK = false
		res.Detail = "unknown action: " + cmd.Action
	}
	return res
}

// syncDomainTargets resolves the list a sync-domains command should apply:
// the command params may carry the list directly ("domains", comma-separated,
// plus optional "hash"; without a hash one is computed over the normalized
// list); without params the last list received from the gateway is re-applied.
func (a *Agent) syncDomainTargets(params map[string]string) ([]string, string) {
	if raw := strings.TrimSpace(params["domains"]); raw != "" {
		var domains []string
		seen := make(map[string]bool)
		for _, d := range strings.Split(raw, ",") {
			d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
			if d != "" && !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
		hash := strings.TrimSpace(params["hash"])
		if hash == "" {
			hash = hashDomains(domains)
		}
		return domains, hash
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.domains...), a.domainsHash
}

// hashDomains computes a deterministic hash over the normalized domain list;
// it is only used when the gateway pushes a list without its own hash.
func hashDomains(domains []string) string {
	sorted := append([]string(nil), domains...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:8])
}

// deregister tears the CN setup down in an order that is safe against the
// self-stop race (review finding): `systemctl disable --now warden-cn-agent`
// stops this very process, so nothing after that call is guaranteed to run.
// The steps therefore run as:
//  1. resolv.conf is restored to public-only nameservers while the local DNS
//     proxy is still serving — a failure here aborts the whole teardown,
//     because stopping the unit now would leave the machine without a working
//     resolver;
//  2. the machine token is cleared on disk — from here on the machine is in a
//     safe state even if the stop kills this process mid-call: no token,
//     heartbeats impossible, unit still running but harmless;
//  3. the irreversible stop goes last. A failure there is reported for the
//     command detail but the teardown still completes via the sentinel:
//     heartbeats with the cleared token fail harmlessly and the unit will not
//     come back on the next boot.
func (a *Agent) deregister() error {
	if err := a.execFn("sh", "-c", a.restoreResolvConfCmd()); err != nil {
		return fmt.Errorf("restore %s: %w", resolvConfPath, err)
	}
	cfg := *a.cfg
	cfg.Token = ""
	if err := Save(a.configPath, &cfg); err != nil {
		return fmt.Errorf("clear token in %s: %w", a.configPath, err)
	}
	a.cfg.Token = ""
	stopErr := a.execFn("systemctl", "disable", "--now", cnAgentService)
	a.mu.Lock()
	a.deregistered = true
	a.mu.Unlock()
	if stopErr != nil {
		return fmt.Errorf("disable %s (token already cleared): %w", cnAgentService, stopErr)
	}
	return nil
}

// restoreResolvConfCmd returns the shell command that rewrites resolv.conf
// with public-only resolvers (the configured upstream plus one well-known
// fallback). It runs through execFn so tests can inspect it without
// touching the real file.
func (a *Agent) restoreResolvConfCmd() string {
	upstream := strings.TrimSpace(a.cfg.Upstream)
	if host, _, err := net.SplitHostPort(upstream); err == nil {
		upstream = host
	}
	return fmt.Sprintf("printf 'nameserver %s\\nnameserver %s\\n' > %s", upstream, fallbackPublicDNS, resolvConfPath)
}

// agentState is the on-disk state file (mode 0644) consumed by warden-cn
// cn-check and by Task 8's SelfCheck (freshness via updated_at).
type agentState struct {
	UpdatedAt      string         `json:"updated_at"`
	ProbeResult    string         `json:"probe_result"`
	ProbeCheckedAt string         `json:"probe_checked_at"`
	ProbeDetail    string         `json:"probe_detail,omitempty"`
	DNSListen      string         `json:"dns_listen"`
	DNSRunning     bool           `json:"dns_running"`
	DNSDegraded    bool           `json:"dns_degraded"`
	DomainsHash    string         `json:"domains_hash"`
	DomainsCount   int            `json:"domains_count"`
	HeartbeatOK    bool           `json:"heartbeat_ok"`
	LastCommand    *CommandResult `json:"last_command,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
}

// writeState overwrites the state file for this cycle (ruling 1: mode 0644).
// It is only called on successful-heartbeat cycles — a failed heartbeat must
// not touch the file at all (ruling 7).
func (a *Agent) writeState(pr ProbeResult, degraded bool) {
	a.mu.Lock()
	st := agentState{
		ProbeResult:  pr.Result,
		DomainsHash:  a.domainsHash,
		DomainsCount: len(a.domains),
		LastCommand:  a.lastCommand,
		LastError:    a.lastError,
	}
	a.mu.Unlock()
	// Lock-free calls after the snapshot (they take their own locks).
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	st.ProbeCheckedAt = pr.CheckedAt.UTC().Format(time.RFC3339)
	st.ProbeDetail = pr.Detail
	st.DNSDegraded = degraded
	st.DNSListen = a.boundDNSListen()
	st.DNSRunning = a.proxyRunning()
	st.HeartbeatOK = true

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		a.log.Errorf("encode state file: %v", err)
		return
	}
	if err := writeFile0644(a.statePath, data); err != nil {
		a.log.Errorf("write state file %s: %v", a.statePath, err)
	}
}

// writeFile0644 writes data to path atomically (temp file + rename) with mode
// 0644 so cn-check readers never observe a half-written file.
func writeFile0644(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// recordError stores a local failure for the state file and logs it.
func (a *Agent) recordError(err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	a.lastError = err.Error()
	a.mu.Unlock()
	a.log.Errorf("%v", err)
}

// --- small snapshot helpers (all take mu; never call these while holding mu) ---

func (a *Agent) domainsHashSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.domainsHash
}

func (a *Agent) degradedSnapshot() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.degraded
}

func (a *Agent) targetVersionSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.targetVersion
}

func (a *Agent) deregisteredSnapshot() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deregistered
}

func (a *Agent) proxyRunning() bool {
	a.mu.Lock()
	p := a.proxy
	a.mu.Unlock()
	if p == nil {
		return false
	}
	return p.Status().Running
}
