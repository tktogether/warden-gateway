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
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/tktogether/warden-gateway/internal/version"
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
	// tokenAtExec[i] is the agent token as it stood ON DISK when execs[i]
	// ran (empty when the config file is unreadable). It lets tests pin the
	// deregister ordering: the token must be cleared before the systemd
	// stop, because the stop kills this very process.
	tokenAtExec []string
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
		if c, err := Load(a.configPath); err == nil {
			ta.tokenAtExec = append(ta.tokenAtExec, c.Token)
		} else {
			ta.tokenAtExec = append(ta.tokenAtExec, "")
		}
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
// order through the injected execFn (review fix): resolv.conf restored to
// public-only resolvers first, then the token cleared on disk, and the
// irreversible `systemctl disable --now` LAST — because it stops this very
// process, nothing after it is guaranteed to run, so the token must already
// be off disk when it fires. RunOnce reports the sentinel so the run loop
// exits cleanly.
func TestRunOnceDeregisterRestoresAndClearsToken(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{
			Commands: []Command{{ID: "d1", Action: "deregister"}},
		}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	origToken := cfg.Token // deregister clears cfg.Token in place; keep the original for the ordering assertions

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
		t.Fatalf("exec calls = %v, want 2 (resolv.conf restore, then systemctl disable last)", execs)
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
		// The token must still be on disk while the resolver is restored:
		// only the stop may run after the token clear.
		if ta.tokenAtExec[0] != origToken {
			t.Errorf("token on disk during resolv.conf restore = %q, want it still present (%q...)", ta.tokenAtExec[0], origToken[:8])
		}
	}
	if len(execs[1]) != 4 || execs[1][0] != "systemctl" || execs[1][1] != "disable" || execs[1][2] != "--now" || execs[1][3] != "warden-cn-agent" {
		t.Errorf("second (final) exec = %v, want systemctl disable --now warden-cn-agent", execs[1])
	}
	// The self-stop race regression: by the time `disable --now` runs, the
	// token must already be cleared on disk (the stop may kill the process,
	// so nothing after it is guaranteed to execute).
	if ta.tokenAtExec[1] != "" {
		t.Errorf("token on disk when systemctl disable ran = %q, want empty (Save must happen before the stop)", ta.tokenAtExec[1])
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

// TestRunOnceDeregisterStopFailureStillExits pins the failure branch of the
// reordered teardown: when `systemctl disable --now` fails AFTER the token
// was cleared, the error lands in the command detail (last_command.ok=false)
// but the agent still exits via the sentinel — heartbeats with the cleared
// token fail harmlessly.
func TestRunOnceDeregisterStopFailureStillExits(t *testing.T) {
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
	// resolv.conf restore succeeds, the systemd stop fails.
	a := ta.agent
	a.execFn = func(name string, args ...string) error {
		ta.mu.Lock()
		ta.execs = append(ta.execs, append([]string{name}, args...))
		if c, err := Load(a.configPath); err == nil {
			ta.tokenAtExec = append(ta.tokenAtExec, c.Token)
		} else {
			ta.tokenAtExec = append(ta.tokenAtExec, "")
		}
		ta.mu.Unlock()
		if name == "systemctl" {
			return errors.New("systemd not running")
		}
		return nil
	}

	err := a.RunOnce(context.Background())
	if !errors.Is(err, errAgentDeregistered) {
		t.Fatalf("RunOnce err = %v, want errAgentDeregistered even though the stop failed", err)
	}

	execs := ta.snapshotExecs()
	if len(execs) != 2 || execs[1][0] != "systemctl" {
		t.Fatalf("exec calls = %v, want resolv.conf restore then the failing systemctl stop", execs)
	}
	if ta.tokenAtExec[1] != "" {
		t.Errorf("token on disk when the failing stop ran = %q, want empty (clear happens before the stop)", ta.tokenAtExec[1])
	}

	loaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("reload agent config: %v", err)
	}
	if loaded.Token != "" {
		t.Errorf("token after failed stop = %q, want empty (cleared before the stop)", loaded.Token)
	}

	st := ta.readState(t)
	if last, ok := st["last_command"].(map[string]any); !ok {
		t.Fatalf("state last_command = %v, want object", st["last_command"])
	} else {
		if last["ok"] != false {
			t.Errorf("last_command.ok = %v, want false after a failed stop", last["ok"])
		}
		if detail, _ := last["detail"].(string); !strings.Contains(detail, "disable") {
			t.Errorf("last_command.detail = %q, want the disable failure recorded", detail)
		}
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

// startUpgradeGateway serves the heartbeat endpoint and the Task 7 release
// fixture layout (binary + bare-hex .sha256 sidecar) on one test server, so a
// RunOnce cycle can heartbeat, read target_version and download the "new
// binary" from the same origin — exactly how production self-upgrade flows.
func startUpgradeGateway(t *testing.T, reply func(body map[string]any) (int, any), relVersion string, fix releaseFixture) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode heartbeat body: %v", err)
		}
		status, payload := reply(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	})
	base := "/api/agent/binary/" + strings.TrimPrefix(relVersion, "v") + "/"
	mux.HandleFunc(base+"warden-cn-agent-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(fix.binary)
	})
	mux.HandleFunc(base+"warden-cn-agent-linux-amd64.sha256", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(fix.sidecar))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestRunOnceUpgradesAndExits pins the actuated self-upgrade path (final
// review fix 1a): a queued self-upgrade command whose swap completes makes
// RunOnce return the errAgentUpgraded sentinel and Run exit cleanly, so
// systemd Restart=always starts the freshly installed binary. SelfUpgrade
// returning nil means the on-disk swap is already done, so the exit happens
// strictly after the binary file was replaced.
func TestRunOnceUpgradesAndExits(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	oldBinary := []byte("old warden-cn-agent binary v1.5.0")
	newBinary := []byte("new warden-cn-agent binary v999, a different payload")
	srv, hits := startUpgradeGateway(t,
		func(body map[string]any) (int, any) {
			return http.StatusOK, HeartbeatReply{
				TargetVersion: "v999",
				Commands:      []Command{{ID: "up1", Action: "self-upgrade", Params: map[string]string{"version": "v999"}}},
			}
		},
		"v999", releaseFixture{binary: newBinary, sidecar: hexOf(newBinary) + "\n"})
	cfg := testConfig(t)
	cfg.Origin = srv.URL
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	// Same self-path injection pattern as the Task 7 SelfUpgrade tests: the
	// hook runs the real swap against a fake "currently running binary".
	selfPath := filepath.Join(t.TempDir(), "warden-cn-agent")
	writeSelf(t, selfPath, oldBinary)
	a := ta.agent
	a.upgradeFn = func(ctx context.Context, ver string) error {
		return SelfUpgrade(ctx, cfg, ver, "amd64", selfPath, nil)
	}

	if err := a.RunOnce(context.Background()); !errors.Is(err, errAgentUpgraded) {
		t.Fatalf("RunOnce err = %v, want errAgentUpgraded (swap done, process must exit for systemd)", err)
	}
	got, err := os.ReadFile(selfPath)
	if err != nil {
		t.Fatalf("read self binary: %v", err)
	}
	if string(got) != string(newBinary) {
		t.Errorf("self binary after upgrade = %q, want the downloaded fixture %q", got, newBinary)
	}
	assertNoNewFile(t, selfPath)
	if n := hits.Load(); n != 2 {
		t.Errorf("release requests = %d, want 2 (binary + sidecar, exactly one upgrade)", n)
	}

	// Run converts the sentinel into a clean nil exit. The gateway keeps
	// naming the same target version, so the very next cycle completes the
	// (idempotent) swap again and Run returns nil right away.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after upgrade = %v, want nil (clean exit for the systemd restart)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after the upgrade within 5s")
	}
}

// TestAutoFollowTriggersUpgrade pins the auto-follow ruling (spec §6.1: agent
// 仅在目标版本与本地不一致时执行自升级): a heartbeat reply whose target_version
// differs from the running version runs the upgrade path exactly once (and a
// completed swap trips the exit sentinel); a matching target_version never
// touches the upgrade path.
func TestAutoFollowTriggersUpgrade(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")

	// target_version differs from the running version → one upgrade attempt.
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{TargetVersion: "v999"}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	calls := 0
	ta.agent.upgradeFn = func(ctx context.Context, ver string) error {
		calls++
		if ver != "v999" {
			t.Errorf("upgradeFn ver = %q, want the reply's target_version v999", ver)
		}
		return nil
	}
	if err := ta.agent.RunOnce(context.Background()); !errors.Is(err, errAgentUpgraded) {
		t.Fatalf("RunOnce err = %v, want errAgentUpgraded after the auto-follow upgrade", err)
	}
	if calls != 1 {
		t.Errorf("upgradeFn calls = %d, want 1", calls)
	}

	// target_version equals the running version → no upgrade at all and a
	// normal nil cycle.
	gwSame := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{TargetVersion: version.Version}
	}}
	cfgSame := gwSame.start(t)
	cfgSame.Upstream = upstream
	taSame := newTestAgent(t, cfgSame)
	sameCalls := 0
	taSame.agent.upgradeFn = func(ctx context.Context, ver string) error {
		sameCalls++
		return nil
	}
	if err := taSame.agent.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce with matching target_version = %v, want nil", err)
	}
	if sameCalls != 0 {
		t.Errorf("upgradeFn calls with matching target_version = %d, want 0 (no upgrade when versions match)", sameCalls)
	}
}

// TestUpgradeFailureKeepsRunning pins the upgrade-loop guard (final review
// fix 1): a failed self-upgrade is reported via last_command with ok=false
// and must NOT trip the exit sentinel — the agent keeps running, the DNS
// proxy keeps serving, and the next cycle retries (spec §6.1: 下载失败或校验
// 不过 → 本次不动，下个心跳重试).
func TestUpgradeFailureKeepsRunning(t *testing.T) {
	upstream := startFakeUpstream(t, "192.0.2.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		return http.StatusOK, HeartbeatReply{TargetVersion: "v999"}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)
	calls := 0
	ta.agent.upgradeFn = func(ctx context.Context, ver string) error {
		calls++
		return errors.New("download failed: connection reset")
	}

	for i := 1; i <= 2; i++ {
		if err := ta.agent.RunOnce(context.Background()); err != nil {
			t.Fatalf("cycle %d err = %v, want nil (a failed upgrade must not exit)", i, err)
		}
	}
	if calls != 2 {
		t.Errorf("upgradeFn calls = %d, want 2 (keeps running and retries every cycle)", calls)
	}
	listen := ta.agent.dnsListen
	if got := queryA(t, listen, "public.example.com"); got != "192.0.2.1" {
		t.Errorf("public query after failed upgrades = %q, want upstream 192.0.2.1 (DNS keeps serving)", got)
	}

	st := ta.readState(t)
	last, ok := st["last_command"].(map[string]any)
	if !ok {
		t.Fatalf("state last_command = %v, want object", st["last_command"])
	}
	if last["ok"] != false {
		t.Errorf("last_command.ok = %v, want false after the failed upgrade", last["ok"])
	}
	if detail, _ := last["detail"].(string); !strings.Contains(detail, "download failed") {
		t.Errorf("last_command.detail = %q, want the upgrade failure recorded", detail)
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

// TestNewWiresUpgradeFn pins the Task 7 wiring: New() must set upgradeFn (a
// hand-built Agent{} keeps it nil and self-upgrade reports "not wired yet"),
// and the wired hook must dispatch into SelfUpgrade rather than being a
// no-op. The closure passes runtime.GOARCH, which is always a supported
// build target, so the arch rejection the ruling asks about is asserted
// against SelfUpgrade itself.
func TestNewWiresUpgradeFn(t *testing.T) {
	cfg := testConfig(t)
	a, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.upgradeFn == nil {
		t.Fatal("New did not wire upgradeFn; self-upgrade would report \"not wired yet\"")
	}

	// Dispatch proof: SelfUpgrade rejects an empty target version before any
	// network call, so the wired hook must surface that error instead of
	// returning nil (or the unwired placeholder).
	if err := a.upgradeFn(context.Background(), ""); err == nil {
		t.Error("upgradeFn(\"\") = nil, want the SelfUpgrade \"version is required\" error")
	} else if !strings.Contains(err.Error(), "version") {
		t.Errorf("upgradeFn(\"\") = %v, want an error mentioning the missing version", err)
	}

	// The arch leg of the ruling: the dispatched function errors on an
	// unsupported arch, mentioning arch.
	err = SelfUpgrade(context.Background(), cfg, "v1.6.0", "riscv64", filepath.Join(t.TempDir(), "agent"), nil)
	if err == nil || !strings.Contains(err.Error(), "arch") {
		t.Errorf("SelfUpgrade with arch riscv64 = %v, want an error mentioning arch", err)
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
	if want := normalizeVersion(version.Version); rep.AgentVersion != want {
		t.Errorf("report agent_version = %q, want the bare running version %q", rep.AgentVersion, want)
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

// TestSeedDomainsFromState pins the cold-start contract: after a restart the
// agent serves the last persisted split list immediately, before the first
// heartbeat reply arrives. A missing, empty or unparsable state file leaves the
// cache untouched.
func TestSeedDomainsFromState(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{statePath: filepath.Join(dir, "state.json")}
	a.dnsListen = "127.0.0.1:0"
	st := agentState{DomainsHash: "abc", Domains: []string{"github.com", "ghcr.io"}}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.statePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	a.seedDomainsFromState()
	a.mu.Lock()
	got, hash := append([]string(nil), a.domains...), a.domainsHash
	a.mu.Unlock()
	if hash != "abc" || len(got) != 2 || got[0] != "github.com" || got[1] != "ghcr.io" {
		t.Fatalf("seeded domains = %v hash %q, want [github.com ghcr.io] abc", got, hash)
	}

	// An already-populated cache is never overwritten by stale state.
	a2 := &Agent{statePath: a.statePath}
	a2.domains = []string{"fresh.example"}
	a2.seedDomainsFromState()
	if len(a2.domains) != 1 || a2.domains[0] != "fresh.example" {
		t.Fatalf("seed overwrote a loaded list: %v", a2.domains)
	}

	// Missing file is a no-op.
	a3 := &Agent{statePath: filepath.Join(dir, "missing.json")}
	a3.seedDomainsFromState()
	if len(a3.domains) != 0 {
		t.Fatalf("missing state seeded %v", a3.domains)
	}
}
