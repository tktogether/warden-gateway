package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// TestAgentEnrollRateLimited pins the per-IP enroll limiter (final review fix
// 4, widened to 10/min in fix wave 2): /api/agent/enroll is unauthenticated and
// audited per attempt, so an anonymous flood must be capped — ten rapid
// attempts from one IP are still processed (403 for the bad token), the
// eleventh gets 429 with Retry-After before the registry is touched, and
// over-limit attempts write no audit line (bounding audit.log is the point of
// the limiter).
func TestAgentEnrollRateLimited(t *testing.T) {
	srv, _ := newAgentPanel(t)
	body := map[string]string{"enroll_token": "deadbeef", "hostname": "flood"}

	for i := 1; i <= cnEnrollRateMax; i++ {
		rec := do(t, srv, "POST", "/api/agent/enroll", body, nil, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status %d, want 403 (still within the per-IP budget)", i, rec.Code)
		}
	}
	rec := do(t, srv, "POST", "/api/agent/enroll", body, nil, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th rapid attempt status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}

	before := countAuditActions(t, srv, "cn.enroll")
	rec = do(t, srv, "POST", "/api/agent/enroll", body, nil, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("12th rapid attempt status %d, want 429", rec.Code)
	}
	if after := countAuditActions(t, srv, "cn.enroll"); after != before {
		t.Errorf("audit cn.enroll lines %d -> %d, want unchanged (over-limit attempts must not be audited)", before, after)
	}
}

// countAuditActions counts audit log entries whose action field equals action.
func countAuditActions(t *testing.T, srv *Server, action string) int {
	t.Helper()
	raw, err := os.ReadFile(srv.auditPath)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), `"action":"`+action+`"`)
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
	// recorded as their desired version.
	if err := srv.cnReg.SetAutoFollow(id, true); err != nil {
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
		t.Errorf("target_version = %q, want %q (autofollow)", reply.TargetVersion, version.Version)
	}
	reloaded, err = LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || m.DesiredVersion != version.Version {
		t.Fatalf("desired version after autofollow beat = %+v, want %q", m, version.Version)
	} else if !m.AutoFollow {
		t.Errorf("auto follow flag not persisted")
	}
}

// cnListShape mirrors the GET /api/cnmachines response the panel UI consumes.
type cnListShape struct {
	AsOf     string       `json:"as_of"`
	Machines []cnRowReply `json:"machines"`
}

// cnRowReply mirrors one machine row of the GET /api/cnmachines response the
// panel UI consumes.
type cnRowReply struct {
	ID             string                 `json:"id"`
	Hostname       string                 `json:"hostname"`
	Online         bool                   `json:"online"`
	LastSeen       string                 `json:"last_seen"`
	EnrolledAt     string                 `json:"enrolled_at"`
	Version        string                 `json:"version"`
	DesiredVersion string                 `json:"desired_version"`
	AutoFollow     bool                   `json:"auto_follow"`
	Probe          map[string]interface{} `json:"probe"`
	System         map[string]interface{} `json:"system"`
	Workloads      []interface{}          `json:"workloads"`
	Events         []map[string]string    `json:"events"`
	Pending        int                    `json:"pending"`
}

// cnMachineList fetches the management list with a panel session.
func cnMachineList(t *testing.T, srv *Server, cookie *http.Cookie) (cnListShape, *httptest.ResponseRecorder) {
	t.Helper()
	rec := do(t, srv, "GET", srv.cfg.PanelPath+"/api/cnmachines", nil, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cn machines list status %d: %s", rec.Code, rec.Body)
	}
	var list cnListShape
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	return list, rec
}

func TestCNMachinesRequiresSession(t *testing.T) {
	srv, key := newAgentPanel(t)

	// Without a session the list, the token mint and machine commands are
	// all rejected.
	if rec := do(t, srv, "GET", srv.cfg.PanelPath+"/api/cnmachines", nil, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status %d, want 401", rec.Code)
	}
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/enroll-token", nil, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated enroll-token status %d, want 401", rec.Code)
	}
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/abc/command",
		map[string]string{"action": "self-check"}, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated command status %d, want 401", rec.Code)
	}

	// With a session the list answers, but POSTs still need the CSRF token.
	cookie, csrf := login(t, srv, key)
	if rec := do(t, srv, "GET", srv.cfg.PanelPath+"/api/cnmachines", nil, cookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("authenticated list status %d, want 200", rec.Code)
	}
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/enroll-token", nil, cookie, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status %d, want 403", rec.Code)
	}

	// Wrong methods are rejected even with a valid session.
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines", map[string]string{}, cookie, csrf); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST list status %d, want 405", rec.Code)
	}
	if rec := do(t, srv, "GET", srv.cfg.PanelPath+"/api/cnmachines/enroll-token", nil, cookie, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET enroll-token status %d, want 405", rec.Code)
	}
}

func TestEnrollTokenAndList(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0
	cookie, csrf := login(t, srv, key)

	// Mint an enroll token through the management endpoint.
	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/enroll-token", nil, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll-token status %d: %s", rec.Code, rec.Body)
	}
	var minted struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}
	if minted.Token == "" {
		t.Fatal("enroll-token reply missing token")
	}
	exp, err := time.Parse(time.RFC3339, minted.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q is not RFC3339: %v", minted.ExpiresAt, err)
	}
	if until := time.Until(exp); until < 14*time.Minute || until > 16*time.Minute {
		t.Errorf("expires_at %v, want about 15 minutes out", minted.ExpiresAt)
	}

	// The minted token enrolls a machine through the agent endpoint.
	rec = do(t, srv, "POST", srv.cfg.PanelPath+"/api/agent/enroll",
		map[string]string{"enroll_token": minted.Token, "hostname": "edge-01"}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll status %d: %s", rec.Code, rec.Body)
	}
	var enrolled struct {
		MachineID string `json:"machine_id"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &enrolled); err != nil {
		t.Fatal(err)
	}
	if enrolled.MachineID == "" || enrolled.Token == "" {
		t.Fatalf("enroll reply incomplete: %s", rec.Body)
	}

	// One heartbeat so the list has a report to show.
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat",
		enrolled.Token, heartbeatReport(srv, enrolled.MachineID)); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", rec.Code, rec.Body)
	}

	list, _ := cnMachineList(t, srv, cookie)
	if list.AsOf == "" {
		t.Error("as_of missing from list reply")
	}
	if len(list.Machines) != 1 {
		t.Fatalf("machines = %d, want 1: %+v", len(list.Machines), list.Machines)
	}
	m := list.Machines[0]
	if m.ID != enrolled.MachineID {
		t.Errorf("id = %q, want %q", m.ID, enrolled.MachineID)
	}
	if m.Hostname != "edge-01" {
		t.Errorf("hostname = %q, want edge-01", m.Hostname)
	}
	if !m.Online {
		t.Error("machine that just beat must be online")
	}
	if m.LastSeen == "" || m.EnrolledAt == "" {
		t.Errorf("timestamps missing: last_seen %q enrolled_at %q", m.LastSeen, m.EnrolledAt)
	}
	if m.Version != "test-1" {
		t.Errorf("version = %q, want test-1 from the report", m.Version)
	}
	if m.Probe == nil || m.System == nil || len(m.Workloads) != 1 {
		t.Errorf("report passthrough incomplete: %+v", m)
	}
	if m.AutoFollow {
		t.Error("auto_follow must default to false")
	}
	if m.Pending != 0 {
		t.Errorf("pending = %d, want 0", m.Pending)
	}
	found := false
	for _, ev := range m.Events {
		if ev["kind"] == "enrolled" {
			found = true
		}
	}
	if !found {
		t.Errorf("enrolled event missing: %+v", m.Events)
	}

	// The mint is audited.
	if items := srv.auditItems(50, "cn.enroll_token"); len(items) == 0 {
		t.Error("cn.enroll_token not audited")
	}
}

func TestQueueUpgradeCommandSetsVersion(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.scriptDir = t.TempDir()
	cacheAgentBuild(t, srv, AgentBinaryVersion(), "amd64", []byte("current"))
	cookie, csrf := login(t, srv, key)
	id, _ := enrollMachine(t, srv, "edge-01")

	// Unknown machines and off-allowlist actions are rejected.
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/deadbeef/command",
		map[string]string{"action": "self-check"}, cookie, csrf); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown machine status %d, want 404", rec.Code)
	}
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/command",
		map[string]string{"action": "reboot"}, cookie, csrf); rec.Code != http.StatusBadRequest {
		t.Fatalf("off-allowlist action status %d, want 400", rec.Code)
	}
	// GET is not allowed on the command endpoint.
	if rec := do(t, srv, "GET", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/command", nil, cookie, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET command status %d, want 405", rec.Code)
	}

	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/command",
		map[string]string{"action": "self-upgrade"}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("command status %d: %s", rec.Code, rec.Body)
	}

	// The queued command carries the gateway's own version as target (compared
	// in normalized, v-less form: that is the cache path segment).
	m := srv.cnReg.MachineByID(id)
	if m == nil || len(m.Pending) != 1 {
		t.Fatalf("pending queue = %+v, want one command", m)
	}
	if m.Pending[0].Action != "self-upgrade" {
		t.Errorf("queued action = %q, want self-upgrade", m.Pending[0].Action)
	}
	if got := m.Pending[0].Params["version"]; got != AgentBinaryVersion() {
		t.Errorf("queued params.version = %q, want %q", got, AgentBinaryVersion())
	}

	// The list shows one pending command without leaking its params. The
	// top-level gateway_version/cached_versions fields legitimately name builds,
	// so the check targets the params payload itself.
	_, rec = cnMachineList(t, srv, cookie)
	if !strings.Contains(rec.Body.String(), "\"pending\":1") {
		t.Errorf("list does not report the pending command: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"params"`) || strings.Contains(rec.Body.String(), `"version":"`+AgentBinaryVersion()+`"`) {
		t.Errorf("list leaks command params: %s", rec.Body)
	}
}

func TestAutoFollowToggle(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0
	cookie, csrf := login(t, srv, key)
	id, token := enrollMachine(t, srv, "edge-01")

	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/deadbeef/auto-follow",
		map[string]bool{"enabled": true}, cookie, csrf); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown machine status %d, want 404", rec.Code)
	}

	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/auto-follow",
		map[string]bool{"enabled": true}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("auto-follow status %d: %s", rec.Code, rec.Body)
	}
	if m := srv.cnReg.MachineByID(id); m == nil || !m.AutoFollow {
		t.Fatalf("auto follow = %+v, want enabled", m)
	}

	// The next beat pins the gateway version as the desired version.
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat",
		token, heartbeatReport(srv, id)); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", rec.Code, rec.Body)
	}
	list, _ := cnMachineList(t, srv, cookie)
	if len(list.Machines) != 1 || !list.Machines[0].AutoFollow || list.Machines[0].DesiredVersion != version.Version {
		t.Fatalf("row = %+v, want auto_follow with the gateway desired version", list.Machines)
	}

	// Toggling off persists too and is recorded as an event.
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/auto-follow",
		map[string]bool{"enabled": false}, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("auto-follow off status %d: %s", rec.Code, rec.Body)
	}
	reloaded, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || m.AutoFollow || len(m.Events) == 0 || m.Events[0].Kind != "auto_follow" {
		t.Fatalf("machine after toggle off = %+v, want flag cleared with auto_follow event", m)
	}
}

func TestRemoveForce(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0
	cookie, csrf := login(t, srv, key)
	id, token := enrollMachine(t, srv, "edge-01")
	report := heartbeatReport(srv, id)
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report); rec.Code != http.StatusOK {
		t.Fatalf("beat status %d: %s", rec.Code, rec.Body)
	}

	// Force removal revokes the machine immediately.
	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/remove",
		map[string]bool{"force": true}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("force remove status %d: %s", rec.Code, rec.Body)
	}
	list, _ := cnMachineList(t, srv, cookie)
	if len(list.Machines) != 0 {
		t.Fatalf("machines after force remove = %+v, want none", list.Machines)
	}

	// The revoked token no longer beats, and its rate-limit record is gone.
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token beat status %d, want 401", rec.Code)
	}
	srv.beatMu.Lock()
	beats := len(srv.lastBeat)
	srv.beatMu.Unlock()
	if beats != 0 {
		t.Errorf("lastBeat holds %d entries after removal, want 0", beats)
	}

	// Removing an unknown machine is a 404.
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/deadbeef/remove",
		map[string]bool{"force": true}, cookie, csrf); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown machine remove status %d, want 404", rec.Code)
	}
	// The removal is audited.
	if items := srv.auditItems(50, "cn.remove"); len(items) == 0 {
		t.Error("cn.remove not audited")
	}
}

func TestRemoveQueuedDeregisterAck(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0
	cookie, csrf := login(t, srv, key)
	id, token := enrollMachine(t, srv, "edge-01")
	report := heartbeatReport(srv, id)

	// A soft remove only queues the deregister command.
	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/remove",
		map[string]bool{"force": false}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("soft remove status %d: %s", rec.Code, rec.Body)
	}
	var queued struct {
		Queued bool `json:"queued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &queued); err != nil || !queued.Queued {
		t.Fatalf("soft remove reply = %s, want queued true", rec.Body)
	}
	if m := srv.cnReg.MachineByID(id); m == nil || len(m.Pending) != 1 || m.Pending[0].Action != "deregister" {
		t.Fatalf("pending queue = %+v, want one deregister", m)
	}
	list, _ := cnMachineList(t, srv, cookie)
	if len(list.Machines) != 1 || list.Machines[0].Pending != 1 {
		t.Fatalf("machine must stay listed after soft remove: %+v", list.Machines)
	}

	// The next heartbeat hands out the command and the ack removes the machine.
	rec = postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack beat status %d: %s", rec.Code, rec.Body)
	}
	var reply cnagent.HeartbeatReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Commands) != 1 || reply.Commands[0].Action != "deregister" {
		t.Fatalf("commands = %+v, want one deregister", reply.Commands)
	}
	if srv.cnReg.MachineByID(id) != nil {
		t.Fatal("machine still registered after deregister ack")
	}
	list, _ = cnMachineList(t, srv, cookie)
	if len(list.Machines) != 0 {
		t.Fatalf("machines after ack = %+v, want none", list.Machines)
	}
	if rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report); rec.Code != http.StatusUnauthorized {
		t.Fatalf("post-ack beat status %d, want 401", rec.Code)
	}
}

func TestMachineListConcurrentWithHeartbeats(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0

	id, token := enrollMachine(t, srv, "edge-01")
	report := heartbeatReport(srv, id)
	// A small pending queue lets the early beats also mutate Pending and
	// Events while the list runs, exercising every Snapshot clone path.
	for i := 0; i < 10; i++ {
		if err := srv.cnReg.QueueCommand(id, "self-check", nil); err != nil {
			t.Fatal(err)
		}
	}
	cookie, _ := login(t, srv, key)

	// -race cannot link on this machine, so the race is exercised by
	// hammering both endpoints concurrently and asserting the final state
	// afterwards instead of relying on the detector. The list builds its rows
	// from registry snapshots (value copies taken under the lock), so the
	// reads never share memory with the heartbeat writer.
	const rounds = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < rounds; i++ {
			rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, report)
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent heartbeat status %d: %s", rec.Code, rec.Body)
				return
			}
		}
	}()
	for i := 0; i < rounds; i++ {
		rec := do(t, srv, "GET", srv.cfg.PanelPath+"/api/cnmachines", nil, cookie, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("concurrent list status %d: %s", rec.Code, rec.Body)
		}
	}
	<-done

	// Final state: the machine survived the hammering with a fresh report and
	// still shows up online in the list.
	m := srv.cnReg.MachineByID(id)
	if m == nil {
		t.Fatal("machine missing after concurrent list + heartbeats")
	}
	if m.Report == nil || m.Report["agent_version"] != "test-1" {
		t.Errorf("report after concurrent runs = %v, want agent_version test-1", m.Report)
	}
	if seen, err := time.Parse(time.RFC3339, m.LastSeen); err != nil || time.Since(seen) >= 30*time.Second {
		t.Errorf("last_seen = %q, want fresh after the last beat", m.LastSeen)
	}
	list, _ := cnMachineList(t, srv, cookie)
	if len(list.Machines) != 1 || !list.Machines[0].Online {
		t.Fatalf("list after concurrent runs = %+v, want one online machine", list.Machines)
	}
}

// TestPinnedVersionOutranksAutoFollow pins the rollback/gray-release contract:
// a version pinned by the operator wins over auto-follow, so a machine keeps
// the requested build instead of being dragged onto the gateway's version on
// the next heartbeat.
func TestPinnedVersionOutranksAutoFollow(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0 // two beats in one test must not trip the limiter
	cookie, csrf := login(t, srv, key)
	id, token := enrollMachine(t, srv, "pinned-host")
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/auto-follow",
		map[string]any{"enabled": true}, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("auto-follow status %d: %s", rec.Code, rec.Body)
	}
	if err := srv.cnReg.SetPinnedVersion(id, "1.5.3"); err != nil {
		t.Fatal(err)
	}
	rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, heartbeatReport(srv, id))
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", rec.Code, rec.Body)
	}
	var reply cnagent.HeartbeatReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.TargetVersion != "1.5.3" {
		t.Fatalf("target_version = %q, want the pinned 1.5.3 (auto-follow must not override a pin)", reply.TargetVersion)
	}

	// Clearing the pin hands control back to auto-follow.
	if err := srv.cnReg.SetPinnedVersion(id, ""); err != nil {
		t.Fatal(err)
	}
	rec = postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, heartbeatReport(srv, id))
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.TargetVersion != AgentBinaryVersion() {
		t.Fatalf("target_version = %q, want the gateway version %q after clearing the pin", reply.TargetVersion, AgentBinaryVersion())
	}
}

// TestAutoFollowAdvancesAcrossGatewayVersions pins F2's core fix: AutoFollow
// used to write DesiredVersion once and then be treated as an operator pin on
// every later beat, freezing the machine on the first gateway version. The
// effective target must follow AgentBinaryVersion as the gateway advances.
func TestAutoFollowAdvancesAcrossGatewayVersions(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })

	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0
	cookie, csrf := login(t, srv, key)
	id, token := enrollMachine(t, srv, "follower")
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/auto-follow",
		map[string]any{"enabled": true}, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("auto-follow status %d: %s", rec.Code, rec.Body)
	}

	beat := func() cnagent.HeartbeatReply {
		rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, heartbeatReport(srv, id))
		if rec.Code != http.StatusOK {
			t.Fatalf("heartbeat status %d: %s", rec.Code, rec.Body)
		}
		var reply cnagent.HeartbeatReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}

	version.Version = "v1.6.0"
	if got := beat().TargetVersion; got != "1.6.0" {
		t.Fatalf("target_version = %q, want 1.6.0", got)
	}
	if m := srv.cnReg.MachineByID(id); m == nil || m.PinnedVersion != "" || m.DesiredVersion != "1.6.0" {
		t.Fatalf("machine after first beat = %+v, want computed desired 1.6.0 and no pin", m)
	}

	version.Version = "v1.7.0"
	if got := beat().TargetVersion; got != "1.7.0" {
		t.Fatalf("target_version after gateway upgrade = %q, want 1.7.0 (auto-follow must advance)", got)
	}
	reloaded, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || m.DesiredVersion != "1.7.0" || m.PinnedVersion != "" {
		t.Fatalf("persisted machine = %+v, want computed desired 1.7.0 with no pin", m)
	}
}

// TestPinSurvivesHeartbeats pins the other half: an operator pin must never be
// overwritten by a routine heartbeat, even while auto-follow is enabled.
func TestPinSurvivesHeartbeats(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "v1.7.0"

	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0
	cookie, csrf := login(t, srv, key)
	id, token := enrollMachine(t, srv, "pinned")
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/auto-follow",
		map[string]any{"enabled": true}, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("auto-follow status %d: %s", rec.Code, rec.Body)
	}
	if err := srv.cnReg.SetPinnedVersion(id, "1.5.3"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, heartbeatReport(srv, id))
		if rec.Code != http.StatusOK {
			t.Fatalf("beat %d status %d: %s", i, rec.Code, rec.Body)
		}
		var reply cnagent.HeartbeatReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.TargetVersion != "1.5.3" {
			t.Fatalf("beat %d target_version = %q, want the pinned 1.5.3", i, reply.TargetVersion)
		}
	}
	reloaded, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || m.PinnedVersion != "1.5.3" || m.DesiredVersion != "1.5.3" {
		t.Fatalf("persisted machine = %+v, want the pin and desired 1.5.3", m)
	}
}

// TestAutoFollowClearsPin pins the explicit override: turning auto-follow on
// drops a rollback pin and records why, so the machine starts following again.
func TestAutoFollowClearsPin(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.minBeatInterval = 0
	cookie, csrf := login(t, srv, key)
	id, _ := enrollMachine(t, srv, "unpin-follow")
	if err := srv.cnReg.SetPinnedVersion(id, "1.5.3"); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/auto-follow",
		map[string]any{"enabled": true}, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("auto-follow status %d: %s", rec.Code, rec.Body)
	}
	m := srv.cnReg.MachineByID(id)
	if m == nil || m.PinnedVersion != "" || !m.AutoFollow {
		t.Fatalf("machine = %+v, want auto-follow on with the pin cleared", m)
	}
	found := false
	for _, ev := range m.Events {
		if ev.Kind == "pin" && ev.Detail == "1.5.3" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("pin-cleared event missing from %+v", m.Events)
	}
}

// TestUpgradeCommandPinsVersion pins command-driven pinning: dispatching a
// self-upgrade naming an older cached build persists that choice, so the next
// heartbeat cannot drag the machine back to the gateway version.
func TestUpgradeCommandPinsVersion(t *testing.T) {
	srv, _ := newAgentPanel(t)
	srv.minBeatInterval = 0
	id, token := enrollMachine(t, srv, "command-pin")
	if err := srv.cnReg.QueueCommand(id, "self-upgrade", map[string]string{"version": "1.5.3"}); err != nil {
		t.Fatal(err)
	}
	rec := postAgentJSON(t, srv, srv.cfg.PanelPath+"/api/agent/heartbeat", token, heartbeatReport(srv, id))
	if rec.Code != http.StatusOK {
		t.Fatalf("beat status %d: %s", rec.Code, rec.Body)
	}
	var reply cnagent.HeartbeatReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.TargetVersion != "1.5.3" {
		t.Fatalf("target_version = %q, want 1.5.3 from the command", reply.TargetVersion)
	}
	reloaded, err := LoadCNRegistry(srv.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if m := reloaded.MachineByID(id); m == nil || m.PinnedVersion != "1.5.3" {
		t.Fatalf("persisted machine = %+v, want PinnedVersion 1.5.3", m)
	}
	// A follow-up routine beat keeps the pin.
	postAgentJSON(t, srv, "/api/agent/heartbeat", token, heartbeatReport(srv, id))
	if m := srv.cnReg.MachineByID(id); m == nil || m.PinnedVersion != "1.5.3" || m.DesiredVersion != "1.5.3" {
		t.Fatalf("machine after routine beat = %+v, want the pin preserved", m)
	}
}

// TestPinEndpointAndAudit pins the management endpoint the panel's "follow
// gateway" choice uses: a cached version pins, an empty version clears, an
// uncached version is rejected, and both outcomes are audited.
func TestPinEndpointAndAudit(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.scriptDir = t.TempDir()
	cacheAgentBuild(t, srv, "1.5.3", "amd64", []byte("old"))
	cookie, csrf := login(t, srv, key)
	id, _ := enrollMachine(t, srv, "pin-endpoint")

	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/pin",
		map[string]any{"version": "v1.5.3"}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("pin status %d: %s", rec.Code, rec.Body)
	}
	if m := srv.cnReg.MachineByID(id); m == nil || m.PinnedVersion != "1.5.3" {
		t.Fatalf("machine = %+v, want PinnedVersion 1.5.3", m)
	}

	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/pin",
		map[string]any{"version": ""}, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("clear pin status %d: %s", rec.Code, rec.Body)
	}
	if m := srv.cnReg.MachineByID(id); m == nil || m.PinnedVersion != "" || m.DesiredVersion != "" {
		t.Fatalf("machine after clearing = %+v, want no pin and no computed target", m)
	}

	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/pin",
		map[string]any{"version": "9.9.9"}, cookie, csrf); rec.Code != http.StatusBadRequest {
		t.Fatalf("uncached pin status %d, want 400", rec.Code)
	}
	if rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/deadbeef/pin",
		map[string]any{"version": ""}, cookie, csrf); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown machine pin status %d, want 404", rec.Code)
	}
	if items := srv.auditItems(50, "cn.pin"); len(items) < 2 {
		t.Errorf("cn.pin audit lines = %d, want at least 2 (set + clear)", len(items))
	}
}

// TestCNMachineRowNormalizesReportedVersion pins F11: version.String() used to
// report "v1.6.0 (sha)" while the desired target was "1.6.0", so the
// 待升级/已钉版本 badge lit up forever. The row must compare bare versions.
func TestCNMachineRowNormalizesReportedVersion(t *testing.T) {
	m := CNMachine{
		Report:         map[string]interface{}{"agent_version": "v1.6.0 (abc1234)"},
		DesiredVersion: "1.6.0",
	}
	row := newCNMachineRow(m, time.Now())
	if row.Version != "1.6.0" {
		t.Fatalf("row version = %q, want the normalized 1.6.0", row.Version)
	}
}

// TestUpgradeCommandRejectsUncachedVersion pins that an operator cannot point a
// machine at a build this gateway cannot serve (which would 404 forever).
func TestUpgradeCommandRejectsUncachedVersion(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.scriptDir = t.TempDir()
	cookie, csrf := login(t, srv, key)
	id, _ := enrollMachine(t, srv, "uncached-host")
	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/command",
		map[string]any{"action": "self-upgrade", "version": "0.0.1"}, cookie, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an uncached version (body %s)", rec.Code, rec.Body)
	}
}

// TestUpgradeCommandPinsCachedVersion pins the rollback path: choosing a cached
// older build queues a self-upgrade carrying that version.
func TestUpgradeCommandPinsCachedVersion(t *testing.T) {
	srv, key := newAgentPanel(t)
	srv.scriptDir = t.TempDir()
	cookie, csrf := login(t, srv, key)
	id, _ := enrollMachine(t, srv, "rollback-host")
	cacheAgentBuild(t, srv, "1.5.3", "amd64", []byte("old"))
	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/cnmachines/"+id+"/command",
		map[string]any{"action": "self-upgrade", "version": "1.5.3"}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	m := srv.cnReg.MachineByID(id)
	if m == nil || len(m.Pending) == 0 {
		t.Fatal("expected a queued self-upgrade command")
	}
	if got := m.Pending[0].Params["version"]; got != "1.5.3" {
		t.Fatalf("queued version = %q, want 1.5.3", got)
	}
}
