package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/backup"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

func newPanel(t *testing.T) (*Server, string) {
	t.Helper()
	return newPanelAt(t, "/warden")
}

func newPanelAt(t *testing.T, panelPath string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.PanelPath = panelPath
	cfg.PublicURL = "http://127.0.0.1:18080" + panelPath
	cfg.Domain = ""
	cfg.TLS.Mode = "self"
	cfg.Listen.HTTP = "127.0.0.1:18080"
	cfg.Listen.HTTPS = "127.0.0.1:18443"
	cfg.Listen.Entry = ""
	cfg.Listen.ACME = ""
	cfg.SNI.Enabled = false
	cfg.SessionSecret = strings.Repeat("s", 48)
	key := "0123456789"
	cfg.AccessKeySHA256 = config.HashAccessKey(key)
	cfg.Backup.Dir = filepath.Join(dir, "backups")
	cfg.Normalize()
	cfgPath := filepath.Join(dir, "config.json")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	mgr, err := auth.NewManager(cfg.SessionSecret, cfg.AccessKeySHA256, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Config: &cfg, ConfigPath: cfgPath, DataDir: dataDir,
		ServiceName: "warden-gateway.service", Auth: mgr,
		Backups: backup.New(cfg.Backup.Dir, cfgPath, dataDir, filepath.Join(dir, "svc"), 3),
		Stats:   telemetry.NewStore(), Started: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, key
}

func do(t *testing.T, srv *Server, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestLoginAndBackup(t *testing.T) {
	srv, key := newPanel(t)
	rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": key}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login status %d: %s", rec.Code, rec.Body)
	}
	var login struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if !login.Authenticated || login.CSRF == "" {
		t.Fatalf("bad login payload: %+v", login)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wg_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}
	if rec := do(t, srv, "GET", "/warden/api/session", nil, cookie, ""); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("authenticated")) {
		t.Fatalf("session after login: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "POST", "/warden/api/backups", map[string]any{}, cookie, login.CSRF)
	if rec.Code != http.StatusOK {
		t.Fatalf("backup status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "GET", "/warden/api/backups", nil, cookie, "")
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("backup-")) {
		t.Fatalf("backup list: %d %s", rec.Code, rec.Body)
	}
}

func TestKeyRotationInvalidatesSession(t *testing.T) {
	srv, key := newPanel(t)
	rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": key}, nil, "")
	var login struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wg_session" {
			cookie = c
		}
	}
	newKey := "changed-key-987654"
	rec = do(t, srv, "POST", "/warden/api/key/change", map[string]string{"current_key": key, "new_key": newKey}, cookie, login.CSRF)
	if rec.Code != http.StatusOK {
		t.Fatalf("key change status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "GET", "/warden/api/session", nil, cookie, "")
	var after struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Authenticated {
		t.Fatal("old session still valid after key change")
	}
	if rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": newKey}, nil, ""); rec.Code != 200 {
		t.Fatalf("new key login status %d", rec.Code)
	}
	if rec := do(t, srv, "POST", "/warden/api/login", map[string]string{"key": key}, nil, ""); rec.Code != 401 {
		t.Fatalf("old key login status %d", rec.Code)
	}
}

func TestEffectOverheadUsesOnlyUpstreamSamples(t *testing.T) {
	srv, key := newPanel(t)
	cookie, _ := login(t, srv, key)
	// An SNI request records latency but never an upstream wait.
	sni := srv.stats.Begin("sni")
	time.Sleep(12 * time.Millisecond)
	sni.Done(200, 10, 10, "", "127.0.0.1", "CONNECT", "github.com:443")
	// A Docker request records both latency and its upstream wait.
	docker := srv.stats.Begin("docker")
	time.Sleep(12 * time.Millisecond)
	docker.DoneUpstream("/v2/library/alpine/manifests/latest", 4, 200, 100, 200, "", "127.0.0.1", "GET", "/v2/library/alpine/manifests/latest")
	rec := do(t, srv, "GET", "/warden/api/effect", nil, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("effect status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Overhead struct {
			AvgLatencyMs    int64 `json:"avg_latency_ms"`
			AvgUpstreamMs   int64 `json:"avg_upstream_ms"`
			GatewayMs       int64 `json:"gateway_ms"`
			UpstreamSamples int64 `json:"upstream_samples"`
		} `json:"overhead"`
		Channels []struct {
			Name          string `json:"name"`
			UpstreamCount int64  `json:"upstream_count"`
			GatewayMs     int64  `json:"gateway_ms"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Overhead.UpstreamSamples != 1 {
		t.Fatalf("upstream samples = %d, want 1", body.Overhead.UpstreamSamples)
	}
	if body.Overhead.AvgLatencyMs <= 0 || body.Overhead.AvgUpstreamMs != 4 {
		t.Fatalf("overhead latency/upstream = %d/%d", body.Overhead.AvgLatencyMs, body.Overhead.AvgUpstreamMs)
	}
	if body.Overhead.GatewayMs < 0 || body.Overhead.GatewayMs >= body.Overhead.AvgLatencyMs {
		t.Fatalf("gateway ms = %d, avg latency = %d", body.Overhead.GatewayMs, body.Overhead.AvgLatencyMs)
	}
	for _, ch := range body.Channels {
		if ch.Name == "sni" && (ch.UpstreamCount != 0 || ch.GatewayMs != 0) {
			t.Fatalf("sni channel upstream/gateway = %d/%d, want 0/0", ch.UpstreamCount, ch.GatewayMs)
		}
	}
}

// login performs a login and returns the session cookie plus CSRF token.
func login(t *testing.T, srv *Server, key string) (*http.Cookie, string) {
	t.Helper()
	rec := do(t, srv, "POST", srv.cfg.PanelPath+"/api/login", map[string]string{"key": key}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login status %d: %s", rec.Code, rec.Body)
	}
	var login struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wg_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}
	return cookie, login.CSRF
}

func TestMetricsExposeExtendedHostFields(t *testing.T) {
	srv, key := newPanel(t)
	cookie, _ := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/metrics?range=1h", nil, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status %d: %s", rec.Code, rec.Body)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"range", "as_of", "points", "latest", "host", "ifaces", "counters", "traffic", "traffic_total", "proxy_series", "step_seconds"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("metrics payload missing %q", key)
		}
	}
	var latest map[string]json.RawMessage
	if err := json.Unmarshal(payload["latest"], &latest); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cpu_percent", "load1", "load5", "load15", "mem_total", "mem_used", "mem_available", "swap_total", "swap_used", "net_rx_rate", "net_tx_rate", "disk_read_rate", "disk_write_rate", "tcp_connections", "established", "time_wait", "listen", "processes", "conntrack"} {
		if _, ok := latest[key]; !ok {
			t.Fatalf("latest sample missing %q", key)
		}
	}
	if rec := do(t, srv, "GET", "/warden/api/metrics?range=2h", nil, cookie, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad range status %d", rec.Code)
	}
}

func TestProxiesExposeLatencyAndRates(t *testing.T) {
	srv, key := newPanel(t)
	cookie, _ := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/proxies", nil, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("proxies status %d: %s", rec.Code, rec.Body)
	}
	var payload struct {
		Proxies []map[string]json.RawMessage `json:"proxies"`
		Recent  []map[string]json.RawMessage `json:"recent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Proxies) != 4 {
		t.Fatalf("want four proxy rows, got %d", len(payload.Proxies))
	}
	names := map[string]bool{}
	for _, row := range payload.Proxies {
		var name string
		if err := json.Unmarshal(row["name"], &name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
		for _, key := range []string{"name", "label", "path", "enabled", "requests", "failures", "bytes_in", "bytes_out", "success_rate", "in_flight", "status_classes", "latency", "rates", "window", "last_minute", "series"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("proxy row %q missing %q", name, key)
			}
		}
		var series []map[string]json.RawMessage
		if err := json.Unmarshal(row["series"], &series); err != nil {
			t.Fatal(err)
		}
		if len(series) < 2 {
			t.Fatalf("proxy %q series too short: %d", name, len(series))
		}
	}
	for _, want := range []string{"docker", "github", "dns", "sni"} {
		if !names[want] {
			t.Fatalf("missing proxy %q in %v", want, names)
		}
	}
}

func TestEffectReturnsSingleNodeMetrics(t *testing.T) {
	srv, key := newPanel(t)
	cookie, csrf := login(t, srv, key)
	if rec := do(t, srv, "POST", "/warden/api/effect", []byte("{}"), cookie, csrf); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/effect status %d", rec.Code)
	}
	rec := do(t, srv, "GET", "/warden/api/effect?minutes=5", nil, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/effect status %d: %s", rec.Code, rec.Body)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"as_of", "window_minutes", "totals", "overhead", "dns", "channels", "series"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("effect payload missing %q", field)
		}
	}
	var channels []map[string]json.RawMessage
	if err := json.Unmarshal(payload["channels"], &channels); err != nil {
		t.Fatal(err)
	}
	if len(channels) != 4 {
		t.Fatalf("effect channels %d, want 4", len(channels))
	}
	for _, field := range []string{"name", "requests", "success_rate", "avg_latency_ms", "gateway_ms", "upstream_ms", "in_per_sec", "targets"} {
		if _, ok := channels[0][field]; !ok {
			t.Fatalf("effect channel missing %q", field)
		}
	}
}

func TestDNSEndpointsExposeStatus(t *testing.T) {
	srv, key := newPanel(t)
	cookie, _ := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/dns", nil, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dns status %d: %s", rec.Code, rec.Body)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"as_of", "dns", "domains", "suggested_gateway_ip"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("dns payload missing %q", field)
		}
	}
	if _, ok := payload["local_resolver_mode"]; ok {
		t.Fatal("dns payload should not expose local_resolver_mode")
	}
	var dns map[string]json.RawMessage
	if err := json.Unmarshal(payload["dns"], &dns); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"enabled", "listen", "gateway_ip", "domains", "running", "active_listen", "fallback", "port53"} {
		if _, ok := dns[field]; !ok {
			t.Fatalf("dns status missing %q", field)
		}
	}
}

func TestPanelPathRedirectsToTrailingSlash(t *testing.T) {
	srv, _ := newPanel(t)
	rec := do(t, srv, "GET", "/warden?tab=metrics", nil, nil, "")
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/warden/?tab=metrics" {
		t.Fatalf("location %q", loc)
	}
	rec = do(t, srv, "GET", "/warden/", nil, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-base="/warden"`) {
		t.Fatal("index does not carry the default base path")
	}
	if rec := do(t, srv, "GET", "/warden/style.css", nil, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("style status %d", rec.Code)
	}
}

func TestPanelPathRewritesIndexBaseAndAssets(t *testing.T) {
	srv, key := newPanelAt(t, "/console")
	if rec := do(t, srv, "GET", "/console/style.css", nil, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("assets must be served under the custom panel path, got %d", rec.Code)
	}
	rec := do(t, srv, "GET", "/console", nil, nil, "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/console/" {
		t.Fatalf("redirect %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = do(t, srv, "GET", "/console/", nil, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `data-base="/console"`) {
		t.Fatalf("index base path not rewritten: %s", body)
	}
	cookie, _ := login(t, srv, key)
	if rec := do(t, srv, "GET", "/console/api/session", nil, cookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("session under custom path status %d: %s", rec.Code, rec.Body)
	}
}

func TestCNEndpointExposesCommandsOnly(t *testing.T) {
	srv, key := newPanel(t)
	cookie, _ := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/cn", nil, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cn status %d: %s", rec.Code, rec.Body)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"as_of", "gateway_ip", "domains", "script_url", "command", "commands", "restore_command", "cn_mode", "dns", "dns_ready", "health_url", "check_command"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("cn payload missing %q", field)
		}
	}
	for _, field := range []string{"local_mode", "resolver_mode", "backup_root", "status"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("cn payload should not expose %q", field)
		}
	}
	var commands map[string]string
	if err := json.Unmarshal(payload["commands"], &commands); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"auto", "none"} {
		if commands[mode] == "" {
			t.Fatalf("cn commands missing mode %q", mode)
		}
	}
	for _, mode := range []string{"hosts", "dns", "both"} {
		if _, ok := commands[mode]; ok {
			t.Fatalf("cn commands should not expose the retired mode %q", mode)
		}
	}
	if !strings.Contains(commands["auto"], "--cn-mode auto") {
		t.Fatalf("auto command %q does not pass --cn-mode auto", commands["auto"])
	}
	if !strings.Contains(commands["none"], "--cn-mode none") {
		t.Fatalf("none command %q does not pass --cn-mode none", commands["none"])
	}
	var cnMode, command, healthURL, checkCommand string
	for field, dst := range map[string]*string{
		"cn_mode":       &cnMode,
		"command":       &command,
		"health_url":    &healthURL,
		"check_command": &checkCommand,
	} {
		if err := json.Unmarshal(payload[field], dst); err != nil {
			t.Fatal(err)
		}
	}
	if cnMode != "auto" {
		t.Fatalf("cn_mode = %q, want auto", cnMode)
	}
	if command != commands["auto"] {
		t.Fatalf("command %q must equal commands[auto] %q", command, commands["auto"])
	}
	if !strings.HasSuffix(healthURL, "/healthz") {
		t.Fatalf("health_url %q must end with /healthz", healthURL)
	}
	if checkCommand == "" {
		t.Fatal("check_command must not be empty")
	}
	if !strings.Contains(checkCommand, "cn-check") {
		t.Fatalf("check_command %q must invoke cn-check", checkCommand)
	}
	for _, path := range []string{"/api/cn/apply", "/api/cn/restore"} {
		if rec := do(t, srv, "POST", "/warden"+path, []byte("{}"), cookie, "x"); rec.Code != http.StatusNotFound {
			t.Fatalf("%s status %d, want 404", path, rec.Code)
		}
	}
}
