package cnagent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// findCheck pulls one named entry out of a SelfCheck report.
func findCheck(res map[string]interface{}, name string) (checkEntry, bool) {
	entries, _ := res["checks"].([]checkEntry)
	for _, c := range entries {
		if c.Name == name {
			return c, true
		}
	}
	return checkEntry{}, false
}

// writeFreshState writes a state file with an updated_at of now and the given
// probe verdict, plus an optional split-domain list.
func writeFreshState(t *testing.T, path, probeResult string, domains []string) {
	t.Helper()
	st := map[string]any{
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
		"probe_result": probeResult,
	}
	if domains != nil {
		st["domains"] = domains
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}
}

// TestSelfCheckReportsBrokenDNS is the failure leg of ruling 5: with
// cfg.DNSListen pointing at a port nothing listens on (and no state file at
// all), SelfCheck must report ok=false and carry a failing dns entry whose
// detail names the probed address.
func TestSelfCheckReportsBrokenDNS(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grab a port: %v", err)
	}
	closed := l.Addr().String()
	_ = l.Close() // nothing listens on this port anymore, UDP included in practice

	cfg := &Config{DNSListen: closed}
	res := SelfCheck(context.Background(), cfg, filepath.Join(t.TempDir(), "state.json"))

	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("ok = true, want false (no state file, dead DNS port %s): %v", closed, res)
	}
	dnsCheck, found := findCheck(res, "dns")
	if !found {
		t.Fatalf("checks have no dns entry: %v", res)
	}
	if dnsCheck.OK {
		t.Errorf("dns check = %+v, want ok=false against the closed port", dnsCheck)
	}
	if !strings.Contains(dnsCheck.Detail, closed) {
		t.Errorf("dns check detail %q does not mention the probed address %s", dnsCheck.Detail, closed)
	}
}

// TestSelfCheckFreshStateOk is the green leg of ruling 5: a freshly written
// state.json plus a fake DNS server (miekg/dns, the Task 6 helper) at
// cfg.DNSListen must yield ok=true with every single check passing, and the
// DNS check must have queried the first domain from the state file's list.
func TestSelfCheckFreshStateOk(t *testing.T) {
	addr := startFakeUpstream(t, "198.18.0.1") // answers every A query with the IP
	cfg := &Config{DNSListen: addr}

	statePath := filepath.Join(t.TempDir(), "state.json")
	writeFreshState(t, statePath, ProbeOK, []string{"app1.example.com", "app2.example.com"})

	res := SelfCheck(context.Background(), cfg, statePath)

	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("ok = false, want true with fresh state and a live DNS server: %v", res)
	}
	for _, c := range res["checks"].([]checkEntry) {
		if !c.OK {
			t.Errorf("check %s = %+v, want ok", c.Name, c)
		}
	}
	if _, err := time.Parse(time.RFC3339, res["as_of"].(string)); err != nil {
		t.Errorf("as_of %v is not RFC3339: %v", res["as_of"], err)
	}
	dnsCheck, found := findCheck(res, "dns")
	if !found {
		t.Fatalf("checks have no dns entry: %v", res)
	}
	if !strings.Contains(dnsCheck.Detail, "app1.example.com") {
		t.Errorf("dns check detail %q does not mention the first split domain", dnsCheck.Detail)
	}
}

// TestSelfCheckStaleStateFails pins the freshness boundary (ruling 5a): a
// state file older than 5 minutes must fail the state check and therefore the
// whole report, even with everything else healthy.
func TestSelfCheckStaleStateFails(t *testing.T) {
	addr := startFakeUpstream(t, "198.18.0.1")
	cfg := &Config{DNSListen: addr}

	statePath := filepath.Join(t.TempDir(), "state.json")
	st := map[string]any{
		"updated_at":   time.Now().UTC().Add(-stateFreshness - time.Minute).Format(time.RFC3339),
		"probe_result": ProbeOK,
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	res := SelfCheck(context.Background(), cfg, statePath)
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("ok = true, want false with a stale state file: %v", res)
	}
	stateCheck, _ := findCheck(res, "state")
	if stateCheck.OK {
		t.Errorf("state check = %+v, want ok=false for a stale updated_at", stateCheck)
	}
}

// TestNewWiresCheckFnAndNotifyFn pins the Task 8 wiring (rulings 1 and 2):
// New() must set checkFn to a SelfCheck closure that reads the CURRENT
// statePath (and the agent's actual DNS listener) at call time, and notifyFn
// to the SDNotify watchdog ping with its error swallowed.
func TestNewWiresCheckFnAndNotifyFn(t *testing.T) {
	a, err := New(testConfig(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.notifyFn == nil {
		t.Fatal("New did not wire notifyFn; the systemd watchdog would starve")
	}
	a.notifyFn(watchdogPing) // must not panic; SDNotify errors are swallowed

	if a.checkFn == nil {
		t.Fatal("New did not wire checkFn; the self-check command would report \"not wired yet\"")
	}

	// Override the injection points AFTER New (like newTestAgent does): the
	// closure must still see them — fresh state + live fake DNS -> ok=true.
	addr := startFakeUpstream(t, "198.18.0.1")
	a.dnsListen = addr
	a.statePath = filepath.Join(t.TempDir(), "state.json")
	writeFreshState(t, a.statePath, ProbeOK, nil)

	m := a.checkFn()
	if ok, _ := m["ok"].(bool); !ok {
		t.Fatalf("checkFn() = %v, want ok=true (fresh state, live DNS)", m)
	}
	if _, ok := m["as_of"].(string); !ok {
		t.Errorf("checkFn() report has no as_of string: %v", m)
	}
	entries, ok := m["checks"].([]checkEntry)
	if !ok || len(entries) != 4 {
		t.Errorf("checks = %v, want 4 named entries (state, probe, dns, config_mode)", m["checks"])
	}
}

// TestRunOnceExecutesSelfCheckCommand works the wiring end-to-end: a gateway
// reply queueing a self-check command must reach the wired checkFn and come
// back as an OK CommandResult whose detail is the marshalled report.
func TestRunOnceExecutesSelfCheckCommand(t *testing.T) {
	upstream := startFakeUpstream(t, "198.18.0.1")
	gw := &fakeGateway{reply: func(n int, body map[string]any) (int, any) {
		if n == 1 {
			return http.StatusOK, HeartbeatReply{Commands: []Command{{ID: "c1", Action: "self-check"}}}
		}
		return http.StatusOK, HeartbeatReply{}
	}}
	cfg := gw.start(t)
	cfg.Upstream = upstream
	ta := newTestAgent(t, cfg)

	// Fresh state + a live DNS listener so every wired check passes.
	ta.agent.dnsListen = upstream
	writeFreshState(t, ta.statePath, ProbeOK, nil)

	if err := ta.agent.RunOnce(context.Background()); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	res := ta.agent.lastCommand
	if res == nil || res.ID != "c1" || res.Action != "self-check" {
		t.Fatalf("last command = %+v, want the executed self-check", res)
	}
	if !res.OK {
		t.Fatalf("self-check command = %+v, want ok=true (fresh state, live DNS)", res)
	}
	if !strings.Contains(res.Detail, "\"ok\":true") {
		t.Errorf("self-check detail = %q, want the marshalled report with ok=true", res.Detail)
	}
}
