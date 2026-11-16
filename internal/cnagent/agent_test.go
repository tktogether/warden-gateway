package cnagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// fakeGateway is a scripted heartbeat endpoint: it records every request body
// and lets each test decide the status code and reply payload. The reply
// function receives n (the 1-based request counter) and the decoded request
// body, mirroring how the real gateway decides whether to push domains.
type fakeGateway struct {
	mu     sync.Mutex
	bodies []map[string]any
	reply  func(n int, body map[string]any) (int, any)
}

// start runs the gateway on a local HTTP test server and returns an agent
// config pointing at it.
func (g *fakeGateway) start(t *testing.T) *Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode heartbeat body: %v", err)
		}
		g.mu.Lock()
		g.bodies = append(g.bodies, body)
		n := len(g.bodies)
		replyFn := g.reply
		g.mu.Unlock()
		status, payload := replyFn(n, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	cfg := testConfig(t)
	cfg.Origin = srv.URL
	return cfg
}

func (g *fakeGateway) snapshotBodies() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]map[string]any, len(g.bodies))
	copy(out, g.bodies)
	return out
}

// startFakeUpstream runs a minimal DNS server on 127.0.0.1 that answers every
// A query with ip. It stands in for the public upstream the proxy forwards to.
func startFakeUpstream(t *testing.T, ip string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind fake upstream: %v", err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		for _, q := range req.Question {
			if q.Qtype == dns.TypeA {
				resp.Answer = append(resp.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP(ip),
				})
			}
		}
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

// queryA resolves name against the DNS server at addr and returns the first A
// record ("" when the answer carries none).
func queryA(t *testing.T, addr, name string) string {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	cl := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	resp, _, err := cl.Exchange(m, addr)
	if err != nil {
		t.Fatalf("query %s via %s: %v", name, addr, err)
	}
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			return a.A.String()
		}
	}
	return ""
}

// testAgent wraps an Agent with the injection points the rulings require:
// state file in a temp dir, loopback DNS listener, scripted prober, and
// recording notify/exec hooks.
type testAgent struct {
	agent     *Agent
	statePath string

	mu       sync.Mutex
	notifies []string
	execs    [][]string
}

func newTestAgent(t *testing.T, cfg *Config) *testAgent {
	t.Helper()
	a, err := New(cfg, nil) // nil logger: logging methods are nil-safe and tests stay quiet
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ta := &testAgent{statePath: filepath.Join(t.TempDir(), "state.json")}
	a.statePath = ta.statePath
	// dnsListen is the injectable field (ruling 1); resolveListen rewrites
	// "127.0.0.1:0" to the concrete bound address once, so reading the field
	// after a cycle yields the address the proxy is serving on.
	a.dnsListen = "127.0.0.1:0"
	a.prober = &fakeProber{pingOK: true}
	a.notifyFn = func(state string) {
		ta.mu.Lock()
		ta.notifies = append(ta.notifies, state)
		ta.mu.Unlock()
	}
	a.execFn = func(name string, args ...string) error {
		ta.mu.Lock()
		ta.execs = append(ta.execs, append([]string{name}, args...))
		ta.mu.Unlock()
		return nil
	}
	ta.agent = a
	return ta
}

func (ta *testAgent) snapshotNotifies() []string {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	return append([]string(nil), ta.notifies...)
}

func (ta *testAgent) snapshotExecs() [][]string {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	out := make([][]string, len(ta.execs))
	copy(out, ta.execs)
	return out
}

func (ta *testAgent) readState(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(ta.statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse state file: %v\n%s", err, data)
	}
	return st
}

// TestRunOnceAppliesDomainsAndBuildsDNS covers the happy path: the first
// heartbeat reply pushes a domain payload, the agent builds the DNS proxy and
// answers split queries with the gateway IP (everything else forwarded to the
// upstream); the second cycle reports the applied hash, gets domains=null and
// must not rebuild the proxy.
func TestRunOnceAppliesDomainsAndBuildsDNS(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		hash, _ := body["domains_hash"].(string)
		if hash == "" {
			return http.StatusOK, HeartbeatReply{
				Domains: &DomainPayload{Hash: "h1", Domains: []string{"app1.example.com", "app2.example.com"}},
			}
		}
		return http.StatusOK, HeartbeatReply{}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	ctx := context.Background()

	if err := ta.agent.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	listen := ta.agent.dnsListen
	if strings.HasSuffix(listen, ":0") {
		t.Fatalf("dnsListen = %q, want the concrete bound address (resolved from 127.0.0.1:0)", listen)
	}
	if got := queryA(t, listen, "app1.example.com"); got != cfg.GatewayIP {
		t.Errorf("app1.example.com = %q, want gateway IP %q", got, cfg.GatewayIP)
	}
	if got := queryA(t, listen, "public.example.com"); got != "192.0.2.1" {
		t.Errorf("public.example.com = %q, want upstream 192.0.2.1", got)
	}

	if err := ta.agent.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}

	bodies := gw.snapshotBodies()
	if len(bodies) != 2 {
		t.Fatalf("heartbeat requests = %d, want 2", len(bodies))
	}
	if got := bodies[0]["domains_hash"]; got != "" {
		t.Errorf("first request domains_hash = %v, want empty", got)
	}
	if got := bodies[1]["domains_hash"]; got != "h1" {
		t.Errorf("second request domains_hash = %v, want h1 (unchanged)", got)
	}
	// Cycle 1 built the proxy twice by design: the first-cycle empty build
	// (so DNS answers before the payload arrives) plus the payload apply.
	buildsAfterCycle1 := ta.agent.applies
	if buildsAfterCycle1 != 2 {
		t.Errorf("proxy builds after cycle 1 = %d, want 2 (initial empty build + payload apply)", buildsAfterCycle1)
	}
	if again := ta.agent.dnsListen; again != listen {
		t.Errorf("dns listen changed across cycles: %q -> %q", listen, again)
	}
	if got := queryA(t, listen, "app2.example.com"); got != cfg.GatewayIP {
		t.Errorf("app2.example.com after cycle 2 = %q, want gateway IP %q", got, cfg.GatewayIP)
	}
	if ta.agent.applies != buildsAfterCycle1 {
		t.Errorf("proxy builds after cycle 2 = %d, want %d (no rebuild when the reply hash is unchanged)", ta.agent.applies, buildsAfterCycle1)
	}
	notifies := ta.snapshotNotifies()
	if len(notifies) != 2 {
		t.Errorf("watchdog pings = %d, want one per cycle (2)", len(notifies))
	}
}

// TestRunOnceDegradesWhenProbeFails pins the degrade linkage: a probe result
// other than OK must make the proxy forward split queries upstream instead of
// answering them with the (unreachable) gateway IP.
func TestRunOnceDegradesWhenProbeFails(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{
			Domains: &DomainPayload{Hash: "h1", Domains: []string{"app1.example.com"}},
		}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	// 443 blocked and healthz failing with 80 open -> LINK_443_BLOCKED.
	ta.agent.prober = &fakeProber{
		pingOK:  true,
		dialErr: map[string]error{"443": errRefused},
		httpErr: map[string]error{"http://" + cfg.GatewayIP + "/healthz": errRefused},
	}

	if err := ta.agent.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	listen := ta.agent.dnsListen
	got := queryA(t, listen, "app1.example.com")
	if got == cfg.GatewayIP {
		t.Fatalf("degraded split query answered with gateway IP %q, want the upstream answer", cfg.GatewayIP)
	}
	if got != "192.0.2.1" {
		t.Errorf("degraded split query = %q, want upstream 192.0.2.1", got)
	}

	st := ta.readState(t)
	if st["dns_degraded"] != true {
		t.Errorf("state dns_degraded = %v, want true", st["dns_degraded"])
	}
	if st["probe_result"] != ProbeLink443Blocked {
		t.Errorf("state probe_result = %v, want %q", st["probe_result"], ProbeLink443Blocked)
	}
}

// TestNon200KeepsLocalState pins Review Focus #4: a rejected heartbeat must
// change nothing locally — the DNS proxy keeps answering and the state file
// stays byte-identical to the last successful cycle.
func TestNon200KeepsLocalState(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		if n == 1 {
			return http.StatusOK, HeartbeatReply{
				Domains: &DomainPayload{Hash: "h1", Domains: []string{"app1.example.com"}},
			}
		}
		return http.StatusUnauthorized, nil
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	ctx := context.Background()

	if err := ta.agent.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	listen := ta.agent.dnsListen
	before, err := os.ReadFile(ta.statePath)
	if err != nil {
		t.Fatalf("read state after cycle 1: %v", err)
	}

	if err := ta.agent.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 2 (401) must not fail RunOnce: %v", err)
	}

	after, err := os.ReadFile(ta.statePath)
	if err != nil {
		t.Fatalf("read state after cycle 2: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("state file changed on heartbeat failure:\nbefore: %s\nafter:  %s", before, after)
	}
	if got := queryA(t, listen, "app1.example.com"); got != cfg.GatewayIP {
		t.Errorf("app1.example.com after 401 = %q, want gateway IP %q (DNS must keep serving)", got, cfg.GatewayIP)
	}
}

// TestRunOnceExecutesCommand pins command execution: a sync-domains command
// carrying a new list applies it, the next heartbeat reports the new hash,
// and the state file records the successful last_command.
func TestRunOnceExecutesCommand(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		switch n {
		case 1:
			return http.StatusOK, HeartbeatReply{
				Domains: &DomainPayload{Hash: "h1", Domains: []string{"app1.example.com"}},
			}
		case 2:
			return http.StatusOK, HeartbeatReply{
				Commands: []Command{{
					ID:     "cmd-1",
					Action: "sync-domains",
					Params: map[string]string{"domains": "app1.example.com,app2.example.com", "hash": "h2"},
				}},
			}
		default:
			return http.StatusOK, HeartbeatReply{}
		}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := ta.agent.RunOnce(ctx); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	bodies := gw.snapshotBodies()
	if len(bodies) != 3 {
		t.Fatalf("heartbeat requests = %d, want 3", len(bodies))
	}
	if got := bodies[2]["domains_hash"]; got != "h2" {
		t.Errorf("third request domains_hash = %v, want h2 (applied by the command)", got)
	}
	listen := ta.agent.dnsListen
	if got := queryA(t, listen, "app2.example.com"); got != cfg.GatewayIP {
		t.Errorf("app2.example.com = %q, want gateway IP %q", got, cfg.GatewayIP)
	}

	st := ta.readState(t)
	last, ok := st["last_command"].(map[string]any)
	if !ok {
		t.Fatalf("state last_command = %v, want object", st["last_command"])
	}
	if last["id"] != "cmd-1" || last["action"] != "sync-domains" {
		t.Errorf("last_command id/action = %v/%v, want cmd-1/sync-domains", last["id"], last["action"])
	}
	if last["ok"] != true {
		t.Errorf("last_command.ok = %v, want true", last["ok"])
	}
}

// TestStateFileWritten pins the state file contract: written every cycle as
// parseable JSON with mode 0644 (permission asserted on non-Windows only).
func TestStateFileWritten(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{
			Domains: &DomainPayload{Hash: "h1", Domains: []string{"app1.example.com"}},
		}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)

	if err := ta.agent.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	info, err := os.Stat(ta.statePath)
	if err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Errorf("state file mode = %o, want 0644", info.Mode().Perm())
	}
	st := ta.readState(t)
	if st["probe_result"] != ProbeOK {
		t.Errorf("probe_result = %v, want %q", st["probe_result"], ProbeOK)
	}
	if st["heartbeat_ok"] != true {
		t.Errorf("heartbeat_ok = %v, want true", st["heartbeat_ok"])
	}
	if st["domains_hash"] != "h1" {
		t.Errorf("domains_hash = %v, want h1", st["domains_hash"])
	}
	if st["dns_running"] != true {
		t.Errorf("dns_running = %v, want true", st["dns_running"])
	}
	if st["dns_degraded"] != false {
		t.Errorf("dns_degraded = %v, want false", st["dns_degraded"])
	}
}

// TestRunOnceDeregisterRestoresAndClearsToken pins the deregister execution
// order through the injected execFn: resolv.conf restored to public-only
// resolvers first, then the service disabled, then the token cleared on disk;
// RunOnce reports the sentinel so the run loop exits cleanly.
func TestRunOnceDeregisterRestoresAndClearsToken(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{
			Commands: []Command{{ID: "d1", Action: "deregister"}},
		}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream

	cfgPath := filepath.Join(t.TempDir(), "agent.json")
	if err := Save(cfgPath, cfg); err != nil {
		t.Fatalf("save agent config: %v", err)
	}
	ta := newTestAgent(t, cfg)
	ta.agent.configPath = cfgPath

	err := ta.agent.RunOnce(context.Background())
	if !errors.Is(err, errAgentDeregistered) {
		t.Fatalf("RunOnce err = %v, want errAgentDeregistered", err)
	}

	execs := ta.snapshotExecs()
	if len(execs) != 2 {
		t.Fatalf("exec calls = %v, want 2 (resolv.conf restore + systemctl disable)", execs)
	}
	if execs[0][0] != "sh" || execs[0][1] != "-c" {
		t.Errorf("first exec = %v, want sh -c ...", execs[0])
	} else {
		script := execs[0][2]
		// The configured upstream (here the fake upstream at 127.0.0.1) plus
		// the well-known fallback must both appear as public-only resolvers.
		if !strings.Contains(script, "/etc/resolv.conf") || !strings.Contains(script, "nameserver 127.0.0.1") || !strings.Contains(script, "nameserver 119.29.29.29") {
			t.Errorf("resolv.conf restore script = %q, want public-only nameservers", script)
		}
	}
	if len(execs[1]) != 4 || execs[1][0] != "systemctl" || execs[1][1] != "disable" || execs[1][2] != "--now" || execs[1][3] != "warden-cn-agent" {
		t.Errorf("second exec = %v, want systemctl disable --now warden-cn-agent", execs[1])
	}

	loaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("reload agent config: %v", err)
	}
	if loaded.Token != "" {
		t.Errorf("token after deregister = %q, want empty", loaded.Token)
	}
	if loaded.Origin != cfg.Origin {
		t.Errorf("origin after deregister = %q, want %q (only the token is cleared)", loaded.Origin, cfg.Origin)
	}

	st := ta.readState(t)
	if last, ok := st["last_command"].(map[string]any); !ok || last["ok"] != true {
		t.Errorf("state last_command = %v, want ok=true", st["last_command"])
	}
}

// TestRunExitsCleanlyAfterDeregister pins the run-loop contract: the
// deregister sentinel must end up as a clean nil return, not an error.
func TestRunExitsCleanlyAfterDeregister(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{
			Commands: []Command{{ID: "d1", Action: "deregister"}},
		}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ta.agent.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after deregister = %v, want nil (clean exit)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after deregister within 5s")
	}
}

// TestRunCommandUnwiredHooks pins the Task 7/8 dispatch contract: nil
// upgradeFn/checkFn report "not wired yet", an unknown action is rejected,
// and sync-domains without any cached list reports success without work.
func TestRunCommandUnwiredHooks(t *testing.T) {
	a := &Agent{}
	ctx := context.Background()

	res := a.runCommand(ctx, Command{ID: "u1", Action: "self-upgrade"})
	if res.OK || res.Detail != "not wired yet" || res.ID != "u1" || res.Action != "self-upgrade" {
		t.Errorf("self-upgrade without upgradeFn = %+v, want OK=false detail \"not wired yet\"", res)
	}
	res = a.runCommand(ctx, Command{ID: "c1", Action: "self-check"})
	if res.OK || res.Detail != "not wired yet" {
		t.Errorf("self-check without checkFn = %+v, want OK=false detail \"not wired yet\"", res)
	}
	res = a.runCommand(ctx, Command{ID: "s1", Action: "sync-domains"})
	if !res.OK {
		t.Errorf("sync-domains with empty cache = %+v, want OK (nothing to sync yet)", res)
	}
	res = a.runCommand(ctx, Command{ID: "x1", Action: "reboot-machine"})
	if res.OK || !strings.Contains(res.Detail, "unknown action") {
		t.Errorf("unknown action = %+v, want OK=false with \"unknown action\" detail", res)
	}
}

// TestJitteredInterval pins the ±20%% heartbeat jitter bounds and the
// fallback to the default interval for unconfigured values.
func TestJitteredInterval(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := jitteredInterval(10)
		if d < 8*time.Second || d > 12*time.Second {
			t.Fatalf("jitteredInterval(10) = %v, want within [8s, 12s]", d)
		}
	}
	for _, base := range []int{0, -3} {
		d := jitteredInterval(base)
		if d < 8*time.Second || d > 12*time.Second {
			t.Fatalf("jitteredInterval(%d) = %v, want default-based [8s, 12s]", base, d)
		}
	}
}

// TestSampleSystem pins the metrics contract: values present and in range
// where /proc exists; zeroed and panic-free where it does not (Windows).
func TestSampleSystem(t *testing.T) {
	s := sampleSystem()
	if s.DiskUsedPct < 0 || s.DiskUsedPct > 100 {
		t.Errorf("disk_used_pct = %v, want within [0, 100]", s.DiskUsedPct)
	}
	if s.MemUsedPct < 0 || s.MemUsedPct > 100 {
		t.Errorf("mem_used_pct = %v, want within [0, 100]", s.MemUsedPct)
	}
	if s.Load1 < 0 {
		t.Errorf("load1 = %v, want >= 0", s.Load1)
	}
	if _, err := os.Stat("/proc"); err != nil {
		if s != (System{}) {
			t.Errorf("sampleSystem without /proc = %+v, want zeroed System", s)
		}
	}
}

// TestRunOnceContextCancelledStillSafe: a cancelled context must not leave a
// half-applied cycle — the heartbeat leg fails and RunOnce returns nil.
func TestRunOnceContextCancelledStillSafe(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ta.agent.RunOnce(ctx); err != nil {
		t.Errorf("RunOnce with cancelled ctx = %v, want nil (logged, no local action)", err)
	}
	if _, err := os.Stat(ta.statePath); !os.IsNotExist(err) {
		t.Errorf("state file written despite failed heartbeat: %v", err)
	}
}

// TestBuildReportFillsWireFields pins the Report construction: identity from
// config, probe round, DNS state, hash, and metrics all filled.
func TestBuildReportFillsWireFields(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)

	pr := ProbeResult{Result: ProbeOK, CheckedAt: time.Now().UTC(), Detail: ""}
	rep := ta.agent.buildReport(context.Background(), pr, false)
	if rep.MachineID != cfg.MachineID || rep.Hostname != cfg.Hostname {
		t.Errorf("report identity = %q/%q, want %q/%q", rep.MachineID, rep.Hostname, cfg.MachineID, cfg.Hostname)
	}
	if rep.AgentVersion == "" {
		t.Error("report agent_version empty")
	}
	if rep.Probe.Result != ProbeOK || rep.Probe.CheckedAt == "" {
		t.Errorf("report probe = %+v, want result and RFC3339 checked_at", rep.Probe)
	}
	if _, err := time.Parse(time.RFC3339, rep.Probe.CheckedAt); err != nil {
		t.Errorf("probe checked_at %q: %v", rep.Probe.CheckedAt, err)
	}
	if rep.DNS.Upstream != upstream || rep.DNS.Degraded {
		t.Errorf("report dns = %+v, want upstream %q degraded=false", rep.DNS, upstream)
	}
	if rep.LastCommand != nil {
		t.Errorf("report last_command = %+v, want nil before any command ran", rep.LastCommand)
	}
}
