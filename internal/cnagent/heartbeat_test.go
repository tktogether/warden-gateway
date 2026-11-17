package cnagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHeartbeatSendsBearerAndParsesReply pins the wire contract with the
// gateway: POST {Origin}/api/agent/heartbeat, Authorization Bearer token,
// JSON body with protocol:1 plus snake_case report fields, and a reply that
// parses into commands / domains / target_version.
func TestHeartbeatSendsBearerAndParsesReply(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotContentType string
	var body map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode heartbeat body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"commands": []map[string]any{
				{"id": "cmd-1", "action": "sync-domains", "params": map[string]string{"source": "panel"}},
			},
			"domains":        map[string]any{"hash": "abc123", "domains": []string{"a.example.com", "b.example.com"}},
			"target_version": "v1.6.0",
		})
	}))
	defer ts.Close()

	cfg := testConfig(t)
	cfg.Origin = ts.URL
	report := &Report{
		MachineID:     "test-machine",
		Hostname:      "cn-host",
		AgentVersion:  "v1.6.0",
		BootTime:      "2026-11-17T08:00:00Z",
		UptimeSeconds: 12345,
		Probe:         Probe{Result: ProbeOK, CheckedAt: "2026-11-17T08:30:00Z"},
		DNS:           DNS{Listen: DefaultDNSListen, Upstream: DefaultUpstream},
		DomainsHash:   "deadbeef",
		System:        System{DiskUsedPct: 42.5, MemUsedPct: 61.25, Load1: 0.37},
		Workloads:     []Workload{{Name: "app", Image: "nginx:1.27", State: "running", Started: "2026-11-16T10:00:00Z", Coolify: true}},
		LastCommand:   &CommandResult{ID: "cmd-0", Action: "self-check", OK: true},
	}

	reply, err := SendHeartbeat(context.Background(), cfg, report, ts.Client())
	if err != nil {
		t.Fatalf("SendHeartbeat: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/agent/heartbeat" {
		t.Errorf("path = %q, want /api/agent/heartbeat", gotPath)
	}
	if want := "Bearer " + cfg.Token; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	if body == nil {
		t.Fatal("request body missing or not a JSON object")
	}
	if got := body["protocol"]; got != float64(ProtocolVersion) {
		t.Errorf("body protocol = %v, want %d", got, ProtocolVersion)
	}
	if got := body["machine_id"]; got != "test-machine" {
		t.Errorf("body machine_id = %v, want test-machine", got)
	}
	if got := body["hostname"]; got != "cn-host" {
		t.Errorf("body hostname = %v, want cn-host", got)
	}
	if got := body["boot_time"]; got != "2026-11-17T08:00:00Z" {
		t.Errorf("body boot_time = %v, want RFC3339 UTC string", got)
	}
	if got := body["uptime_seconds"]; got != float64(12345) {
		t.Errorf("body uptime_seconds = %v, want 12345", got)
	}
	if got := body["domains_hash"]; got != "deadbeef" {
		t.Errorf("body domains_hash = %v, want deadbeef", got)
	}
	probe, ok := body["probe"].(map[string]any)
	if !ok {
		t.Fatalf("body probe = %v, want object", body["probe"])
	}
	if got := probe["result"]; got != ProbeOK {
		t.Errorf("probe.result = %v, want %q", got, ProbeOK)
	}
	dns, ok := body["dns"].(map[string]any)
	if !ok {
		t.Fatalf("body dns = %v, want object", body["dns"])
	}
	if got := dns["listen"]; got != DefaultDNSListen {
		t.Errorf("dns.listen = %v, want %q", got, DefaultDNSListen)
	}
	system, ok := body["system"].(map[string]any)
	if !ok {
		t.Fatalf("body system = %v, want object", body["system"])
	}
	if got := system["disk_used_pct"]; got != 42.5 {
		t.Errorf("system.disk_used_pct = %v, want 42.5", got)
	}
	workloads, ok := body["workloads"].([]any)
	if !ok || len(workloads) != 1 {
		t.Fatalf("body workloads = %v, want one entry", body["workloads"])
	}
	wl, ok := workloads[0].(map[string]any)
	if !ok {
		t.Fatalf("workloads[0] = %v, want object", workloads[0])
	}
	if got := wl["name"]; got != "app" {
		t.Errorf("workloads[0].name = %v, want app", got)
	}
	if got := wl["coolify"]; got != true {
		t.Errorf("workloads[0].coolify = %v, want true", got)
	}
	last, ok := body["last_command"].(map[string]any)
	if !ok {
		t.Fatalf("body last_command = %v, want object", body["last_command"])
	}
	if got := last["action"]; got != "self-check" {
		t.Errorf("last_command.action = %v, want self-check", got)
	}
	if got := last["ok"]; got != true {
		t.Errorf("last_command.ok = %v, want true", got)
	}

	if len(reply.Commands) != 1 {
		t.Fatalf("reply.Commands = %+v, want one command", reply.Commands)
	}
	cmd := reply.Commands[0]
	if cmd.ID != "cmd-1" || cmd.Action != "sync-domains" {
		t.Errorf("command = %q/%q, want cmd-1/sync-domains", cmd.ID, cmd.Action)
	}
	if got := cmd.Params["source"]; got != "panel" {
		t.Errorf("command params[source] = %q, want panel", got)
	}
	if reply.Domains == nil {
		t.Fatal("reply.Domains = nil, want payload")
	}
	if reply.Domains.Hash != "abc123" {
		t.Errorf("domains hash = %q, want abc123", reply.Domains.Hash)
	}
	if len(reply.Domains.Domains) != 2 || reply.Domains.Domains[0] != "a.example.com" {
		t.Errorf("domains list = %v, want [a.example.com b.example.com]", reply.Domains.Domains)
	}
	if reply.TargetVersion != "v1.6.0" {
		t.Errorf("target_version = %q, want v1.6.0", reply.TargetVersion)
	}
}

// TestHeartbeatInsecureTLS pins the self-signed/IP-only gateway path
// (tls.mode=self): the default verifying client must reject the untrusted
// certificate, while InsecureTLS makes both the explicit client and the nil
// fallback accept it. Without this, enrollment succeeds (curl -k) but every
// heartbeat fails with x509 and the migration times out.
func TestHeartbeatInsecureTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commands":[],"target_version":""}`))
	}))
	defer ts.Close()

	report := &Report{MachineID: "self-signed-machine"}

	secure := testConfig(t)
	secure.Origin = ts.URL
	if _, err := SendHeartbeat(context.Background(), secure, report, secure.HTTPClient(time.Second)); err == nil {
		t.Fatal("SendHeartbeat with InsecureTLS=false succeeded against a self-signed server, want a certificate error")
	} else if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Fatalf("SendHeartbeat with InsecureTLS=false = %v, want a certificate verification failure", err)
	}

	insecure := testConfig(t)
	insecure.Origin = ts.URL
	insecure.InsecureTLS = true
	for name, client := range map[string]*http.Client{
		"explicit":     insecure.HTTPClient(5 * time.Second),
		"nil-fallback": nil,
	} {
		reply, err := SendHeartbeat(context.Background(), insecure, report, client)
		if err != nil {
			t.Errorf("SendHeartbeat (%s client) with InsecureTLS=true: %v", name, err)
			continue
		}
		if reply == nil {
			t.Errorf("SendHeartbeat (%s client) returned a nil reply", name)
		}
	}
}

func TestHeartbeat401IsStatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	cfg := testConfig(t)
	cfg.Origin = ts.URL
	_, err := SendHeartbeat(context.Background(), cfg, &Report{MachineID: "m"}, ts.Client())

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *StatusError", err, err)
	}
	if se.Code != http.StatusUnauthorized {
		t.Errorf("Code = %d, want 401", se.Code)
	}
}

func TestHeartbeat429IsStatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	cfg := testConfig(t)
	cfg.Origin = ts.URL
	_, err := SendHeartbeat(context.Background(), cfg, &Report{MachineID: "m"}, ts.Client())

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *StatusError", err, err)
	}
	if se.Code != http.StatusTooManyRequests {
		t.Errorf("Code = %d, want 429", se.Code)
	}
}

// TestTransportErrorIsNotStatusError pins the other half of the contract:
// callers use errors.As on *StatusError to tell HTTP rejections (401/429/5xx)
// apart from network failures, so a transport error must not be one.
func TestTransportErrorIsNotStatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close() // nothing is listening anymore

	cfg := testConfig(t)
	cfg.Origin = url
	_, err := SendHeartbeat(context.Background(), cfg, &Report{MachineID: "m"}, nil)
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
	var se *StatusError
	if errors.As(err, &se) {
		t.Fatalf("transport error classified as StatusError{%d}", se.Code)
	}
}
