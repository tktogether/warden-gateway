package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/cnagent"
	"github.com/tktogether/warden-gateway/internal/version"
)

// newAgentPanel builds a panel server wired to a CN agent registry loaded
// from the same temp data dir app.go uses in production.
func newAgentPanel(t *testing.T) (*Server, string) {
	t.Helper()
	srv, key := newPanel(t)
	reg, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv.cnReg = reg
	return srv, key
}

// enrollMachine registers a machine through the HTTP enroll endpoint and
// returns its machine ID and raw machine token.
func enrollMachine(t *testing.T, srv *Server, hostname string) (string, string) {
	t.Helper()
	rawEnroll, err := srv.cnReg.NewEnrollToken(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/agent/enroll",
		map[string]string{"enroll_token": rawEnroll, "hostname": hostname}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		MachineID string `json:"machine_id"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.MachineID == "" || body.Token == "" {
		t.Fatalf("enroll reply incomplete: %s", rec.Body)
	}
	return body.MachineID, body.Token
}

// postAgentJSON posts a JSON body with an optional Bearer token to the panel.
func postAgentJSON(t *testing.T, srv *Server, path, bearer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// heartbeatReport builds a report whose domains_hash matches the gateway's
// current list, so a clean reply carries no domain payload.
func heartbeatReport(srv *Server, machineID string) *cnagent.Report {
	return &cnagent.Report{
		Protocol:     cnagent.ProtocolVersion,
		MachineID:    machineID,
		Hostname:     "edge-01",
		AgentVersion: "test-1",
		Probe:        cnagent.Probe{Result: "ok", CheckedAt: "2026-11-17T12:00:00Z"},
		DNS:          cnagent.DNS{Listen: "198.18.0.1:53", Upstream: "1.1.1.1:53"},
		DomainsHash:  hashDomains(srv.cfg.DNS.Domains),
		System:       cnagent.System{DiskUsedPct: 42, MemUsedPct: 55, Load1: 0.7},
		Workloads:    []cnagent.Workload{{Name: "web", Image: "nginx:1.27", State: "running", Coolify: false}},
	}
}

func TestAgentEnrollFlow(t *testing.T) {
	srv, _ := newAgentPanel(t)

	rawEnroll, err := srv.cnReg.NewEnrollToken(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Agents post to the bare /api/agent path (no panel prefix), just like
	// the production heartbeat URL; the panel-prefixed form routes too.
	rec := do(t, srv, "POST", "/api/agent/enroll",
		map[string]string{"enroll_token": rawEnroll, "hostname": "edge-01"}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll status %d: %s", rec.Code, rec.Body)
	}
	var reply struct {
		MachineID string `json:"machine_id"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.MachineID == "" || reply.Token == "" {
		t.Fatalf("enroll reply incomplete: %s", rec.Body)
	}

	// The machine is registered with the requested hostname.
	if m := srv.cnReg.MachineByID(reply.MachineID); m == nil {
		t.Fatal("machine missing from registry after enroll")
	} else if m.Hostname != "edge-01" {
		t.Errorf("hostname = %q, want edge-01", m.Hostname)
	}

	// The enroll token is single-use and persisted as used.
	reloaded, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.tokens) != 1 || !reloaded.tokens[0].Used {
		t.Fatalf("enroll token not marked used after enroll: %+v", reloaded.tokens)
	}

	// Re-using the token is rejected (panel-prefixed route this time).
	rec = do(t, srv, "POST", srv.cfg.PanelPath+"/api/agent/enroll",
		map[string]string{"enroll_token": rawEnroll, "hostname": "edge-02"}, nil, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reused enroll token status %d, want 403", rec.Code)
	}
	// An unknown token is rejected too.
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/agent/enroll",
		map[string]string{"enroll_token": "deadbeef", "hostname": "edge-03"}, nil, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("unknown enroll token status %d, want 403", rec.Code)
	}
	// GET is not allowed on the enroll endpoint.
	if rec := do(t, srv, "GET", srv.cfg.PanelPath+"/api/agent/enroll", nil, nil, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET enroll status %d, want 405", rec.Code)
	}
	// Oversized bodies are rejected before reaching the registry.
	huge := strings.Repeat("a", 2<<20)
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/enroll", "", huge); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized enroll body status %d, want 400", rec.Code)
	}
}

func TestAgentHeartbeatRoundTrip(t *testing.T) {
	srv, _ := newAgentPanel(t)
	srv.minBeatInterval = 0 // the round trip must not trip the rate limiter

	id, token := enrollMachine(t, srv, "edge-01")
	report := heartbeatReport(srv, id)

	rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", rec.Code, rec.Body)
	}
	// Matching domains hash: the reply carries no domains payload, no
	// commands and no target version.
	if got, want := rec.Body.String(), "{\"commands\":[],\"domains\":null,\"target_version\":\"\"}\n"; got != want {
		t.Fatalf("heartbeat reply = %q, want %q", got, want)
	}

	// The report was persisted on the machine (fresh copy from disk).
	reloaded, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	m := reloaded.MachineByID(id)
	if m == nil {
		t.Fatal("machine missing after heartbeat")
	}
	if m.Report == nil {
		t.Fatal("machine report missing after heartbeat")
	}
	for _, field := range []string{"hostname", "agent_version", "probe", "system", "workloads"} {
		if _, ok := m.Report[field]; !ok {
			t.Errorf("machine report missing %q: %v", field, m.Report)
		}
	}
	if got := m.Report["agent_version"]; got != "test-1" {
		t.Errorf("report agent_version = %v, want test-1", got)
	}
	if m.DesiredVersion != "" {
		t.Errorf("desired version = %q, want empty for non-autofollow machine", m.DesiredVersion)
	}

	// A changed gateway domain list is pushed down on the next beat. The
	// agent still reports the hash of the list it applied earlier, which is
	// now stale.
	srv.cfg.DNS.Domains = []string{"a.example.com", "b.example.com"}
	rec = postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
	if rec.Code != http.StatusOK {
		t.Fatalf("second heartbeat status %d: %s", rec.Code, rec.Body)
	}
	var reply2 cnagent.HeartbeatReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply2); err != nil {
		t.Fatal(err)
	}
	if reply2.Domains == nil {
		t.Fatal("changed domain list must be pushed, got null payload")
	}
	if want := hashDomains(srv.cfg.DNS.Domains); reply2.Domains.Hash != want {
		t.Errorf("pushed hash = %q, want %q", reply2.Domains.Hash, want)
	}
	if !reflect.DeepEqual(reply2.Domains.Domains, []string{"a.example.com", "b.example.com"}) {
		t.Errorf("pushed domains = %v, want the gateway list", reply2.Domains.Domains)
	}
	if reply2.TargetVersion != "" {
		t.Errorf("target_version = %q, want empty", reply2.TargetVersion)
	}

	// An unsupported protocol version is rejected.
	bad := heartbeatReport(srv, id)
	bad.Protocol = 7
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("protocol 7 status %d, want 400", rec.Code)
	}
}

func TestHeartbeatRateLimited(t *testing.T) {
	srv, _ := newAgentPanel(t) // default minBeatInterval (8s) stays active

	id, token := enrollMachine(t, srv, "edge-01")
	report := heartbeatReport(srv, id)

	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report); rec.Code != http.StatusOK {
		t.Fatalf("first beat status %d: %s", rec.Code, rec.Body)
	}
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rapid second beat status %d, want 429", rec.Code)
	}
	// A different (unknown) token is still answered with 401, not the limiter.
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", strings.Repeat("f", 64), report); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token beat status %d, want 401", rec.Code)
	}
}

func TestHeartbeatUnknownToken401(t *testing.T) {
	srv, _ := newAgentPanel(t)

	// The production agent posts to the bare /api/agent path.
	if rec := postAgentJSON(t, srv, "/api/agent/heartbeat", "", heartbeatReport(srv, "m-1")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status %d, want 401", rec.Code)
	}
	if rec := postAgentJSON(t, srv, "/api/agent/heartbeat", "no-such-token", heartbeatReport(srv, "m-1")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token status %d, want 401", rec.Code)
	}
	// Non-bearer schemes are rejected as well.
	req := httptest.NewRequest(http.MethodPost, "/api/agent/heartbeat", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("basic auth status %d, want 401", rec.Code)
	}
}

func TestCommandDelivery(t *testing.T) {
	srv, _ := newAgentPanel(t)
	srv.minBeatInterval = 0

	id, token := enrollMachine(t, srv, "edge-01")
	report := heartbeatReport(srv, id)

	// One queued command comes back and leaves the queue.
	if err := srv.cnReg.QueueCommand(id, "dns_set", map[string]string{"zone": "example.com"}); err != nil {
		t.Fatal(err)
	}
	var pendingID string
	if m := srv.cnReg.MachineByID(id); m == nil || len(m.Pending) != 1 {
		t.Fatalf("pending queue before beat = %+v, want one command", m)
	} else {
		pendingID = m.Pending[0].ID
	}
	rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
	if rec.Code != http.StatusOK {
		t.Fatalf("beat status %d: %s", rec.Code, rec.Body)
	}
	var reply cnagent.HeartbeatReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Commands) != 1 {
		t.Fatalf("commands = %+v, want one", reply.Commands)
	}
	if reply.Commands[0].ID != pendingID || reply.Commands[0].Action != "dns_set" || reply.Commands[0].Params["zone"] != "example.com" {
		t.Errorf("delivered command = %+v, want queued dns_set %s", reply.Commands[0], pendingID)
	}
	if reply.TargetVersion != "" {
		t.Errorf("target_version = %q, want empty for a non-upgrade command", reply.TargetVersion)
	}
	reloaded, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || len(m.Pending) != 0 {
		t.Fatalf("pending queue after dispatch = %+v, want empty", m)
	} else if len(m.Events) == 0 || m.Events[0].Kind != "dispatched" {
		t.Errorf("events after dispatch = %+v, want newest dispatched", m.Events)
	}

	// Only up to 5 commands are handed out per beat.
	for i := 0; i < 7; i++ {
		if err := srv.cnReg.QueueCommand(id, "self-check", nil); err != nil {
			t.Fatal(err)
		}
	}
	rec = postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
	if rec.Code != http.StatusOK {
		t.Fatalf("beat status %d: %s", rec.Code, rec.Body)
	}
	reply = cnagent.HeartbeatReply{}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Commands) != 5 {
		t.Fatalf("commands = %d, want at most 5 per beat", len(reply.Commands))
	}
	reloaded, err = LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || len(m.Pending) != 2 {
		t.Fatalf("pending after capped dispatch = %+v, want 2 left", m)
	}

	// A queued self-upgrade sets target_version even without autofollow.
	if err := srv.cnReg.QueueCommand(id, "self-upgrade", nil); err != nil {
		t.Fatal(err)
	}
	rec = postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
	if rec.Code != http.StatusOK {
		t.Fatalf("beat status %d: %s", rec.Code, rec.Body)
	}
	reply = cnagent.HeartbeatReply{}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.TargetVersion != version.Version {
		t.Errorf("target_version = %q, want %q (self-upgrade popped)", reply.TargetVersion, version.Version)
	}

	// AutoFollow machines are always told the gateway version and get it
	// recorded as their desired version. Test-only mutation: the registry
	// does not expose an AutoFollow setter yet.
	if m := srv.cnReg.MachineByID(id); m == nil {
		t.Fatal("machine missing")
	} else {
		m.AutoFollow = true
	}
	rec = postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
	if rec.Code != http.StatusOK {
		t.Fatalf("beat status %d: %s", rec.Code, rec.Body)
	}
	reply = cnagent.HeartbeatReply{}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.TargetVersion != version.Version {
		t.Errorf("target_version = %q, want %q (autofollow)", reply.TargetVersion, version.Version)
	}
	reloaded, err = LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || m.DesiredVersion != version.Version {
		t.Fatalf("desired version after autofollow beat = %+v, want %q", m, version.Version)
	}
}
