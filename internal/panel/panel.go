package panel

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/backup"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/dnsproxy"
	"github.com/tktogether/warden-gateway/internal/gateway"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/singbox"
	"github.com/tktogether/warden-gateway/internal/telemetry"
	"github.com/tktogether/warden-gateway/internal/version"
)

//go:embed web/*
var webFS embed.FS

const (
	defaultService = "warden-gateway.service"
	maxAuditBytes  = 5 << 20
	auditKeep      = 14
	sessionBodyMax = 1 << 20
)

type Options struct {
	Config      *config.Config
	ConfigPath  string
	DataDir     string
	ScriptDir   string
	ServiceName string
	Auth        *auth.Manager
	Backups     *backup.Manager
	Stats       *telemetry.Store
	Log         *logging.Logger
	Started     time.Time
	DNS         *dnsproxy.Proxy
	SingBox     *singbox.Manager
}

type Server struct {
	cfg         *config.Config
	cfgPath     string
	dataDir     string
	scriptDir   string
	serviceName string
	auth        *auth.Manager
	backups     *backup.Manager
	stats       *telemetry.Store
	log         *logging.Logger
	started     time.Time
	host        *hostMetrics
	dns         *dnsproxy.Proxy
	singbox     *singbox.Manager
	auditPath   string
	sampleMu    sync.Mutex
	restartMu   sync.Mutex
}

func New(opts Options) (*Server, error) {
	if opts.Config == nil {
		return nil, errors.New("config is required")
	}
	if opts.Auth == nil {
		return nil, errors.New("auth manager is required")
	}
	if opts.DataDir == "" {
		opts.DataDir = config.DefaultDataDir
	}
	if opts.ScriptDir == "" {
		opts.ScriptDir = config.DefaultScriptDir
	}
	if opts.ServiceName == "" {
		opts.ServiceName = defaultService
	}
	if opts.Log == nil {
		opts.Log = &logging.Logger{}
	}
	if opts.Stats == nil {
		opts.Stats = telemetry.NewStore()
	}
	if opts.Started.IsZero() {
		opts.Started = time.Now()
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Server{
		cfg: opts.Config, cfgPath: opts.ConfigPath, dataDir: opts.DataDir, scriptDir: opts.ScriptDir,
		serviceName: opts.ServiceName, auth: opts.Auth, backups: opts.Backups,
		stats: opts.Stats, log: opts.Log, started: opts.Started,
		host: newHostMetrics(hostSampleCapacity), dns: opts.DNS, singbox: opts.SingBox,
		auditPath: filepath.Join(opts.DataDir, "audit.log"),
	}
	s.host.Sample()
	return s, nil
}

func (s *Server) Start(ctx context.Context) {
	go s.sampleLoop(ctx)
}

// hostSampleSeconds is how often the panel refreshes host level metrics. The
// live pages rely on it, so it stays short enough to feel immediate while
// keeping the /proc reads negligible.
const hostSampleSeconds = 5

// hostSampleCapacity keeps 24 hours of host samples in the memory ring.
const hostSampleCapacity = 24 * 60 * 60 / hostSampleSeconds

func (s *Server) sampleLoop(ctx context.Context) {
	ticker := time.NewTicker(hostSampleSeconds * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.host.Sample()
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if r.URL.Path == s.cfg.PanelPath {
		target := s.cfg.PanelPath + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if strings.HasPrefix(r.URL.Path, s.cfg.PanelPath+"/") {
		s.route(w, r, strings.TrimPrefix(r.URL.Path, s.cfg.PanelPath))
		return
	}
	s.route(w, r, r.URL.Path)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, p string) {
	if p == "" {
		p = "/"
	}
	if p == "/" || p == "/index.html" {
		s.serveAsset(w, "index.html", "text/html; charset=utf-8", true)
		return
	}
	switch p {
	case "/bootstrap.sh":
		s.serveScript(w, r, "bootstrap.sh")
		return
	case "/toolbox.sh":
		s.serveScript(w, r, "toolbox.sh")
		return
	}
	switch p {
	case "/app.js":
		s.serveAsset(w, "app.js", "application/javascript; charset=utf-8", false)
		return
	case "/style.css":
		s.serveAsset(w, "style.css", "text/css; charset=utf-8", false)
		return
	case "/favicon.svg":
		s.serveAsset(w, "favicon.svg", "image/svg+xml", false)
		return
	}
	if !strings.HasPrefix(p, "/api/") {
		http.NotFound(w, r)
		return
	}
	switch p {
	case "/api/health":
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "version": version.Version})
		return
	case "/api/session":
		s.handleSession(w, r)
		return
	case "/api/login":
		s.handleLogin(w, r)
		return
	case "/api/logout":
		s.handleLogout(w, r)
		return
	case "/api/overview":
		s.handleOverview(w, r)
		return
	case "/api/metrics":
		s.handleMetrics(w, r)
		return
	case "/api/proxies":
		s.handleProxies(w, r)
		return
	case "/api/proxies/test":
		s.handleProbe(w, r)
		return
	case "/api/effect":
		s.handleEffect(w, r)
		return
	case "/api/services":
		s.handleServices(w, r)
		return
	case "/api/config", "/api/settings":
		s.handleConfig(w, r)
		return
	case "/api/cert":
		s.handleCert(w, r)
		return
	case "/api/cert/renew":
		s.handleCertRenew(w, r)
		return
	case "/api/key/change":
		s.handleKeyChange(w, r)
		return
	case "/api/backups":
		s.handleBackups(w, r)
		return
	case "/api/audit":
		s.handleAudit(w, r)
		return
	case "/api/diagnostics":
		s.handleDiagnostics(w, r)
		return
	case "/api/cn":
		s.handleCNCommands(w, r)
		return
	case "/api/dns":
		s.handleDNSStatus(w, r)
		return
	case "/api/dns/config":
		s.handleDNSConfig(w, r)
		return
	case "/api/singbox":
		s.handleSingbox(w, r)
		return
	case "/api/singbox/nodes":
		s.handleSingboxCreate(w, r)
		return
	case "/api/singbox/action":
		s.handleSingboxAction(w, r)
		return
	case "/api/singbox/core":
		s.handleSingboxCore(w, r)
		return
	case "/api/singbox/config":
		s.handleSingboxConfig(w, r)
		return
	case "/api/singbox/qr":
		s.handleSingboxQR(w, r)
		return
	case "/api/singbox/connections":
		s.handleSingboxConnections(w, r)
		return
	case "/api/singbox/log":
		s.handleSingboxLog(w, r)
		return
	}
	if strings.HasPrefix(p, "/api/services/") {
		s.handleServiceAction(w, r, strings.TrimPrefix(p, "/api/services/"))
		return
	}
	if strings.HasPrefix(p, "/api/backups/") {
		s.handleBackupItem(w, r, strings.TrimPrefix(p, "/api/backups/"))
		return
	}
	http.NotFound(w, r)
}

func (s *Server) serveAsset(w http.ResponseWriter, name, contentType string, noStore bool) {
	raw, err := fs.ReadFile(webFS, "web/"+name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if name == "index.html" {
		raw = s.rewriteIndex(raw)
	}
	w.Header().Set("Content-Type", contentType)
	if noStore {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	_, _ = w.Write(raw)
}

// rewriteIndex pins the panel base path into the served document so that a
// custom --panel-path keeps resolving its assets and API calls. The placeholder
// in the embedded file is the default path.
func (s *Server) rewriteIndex(raw []byte) []byte {
	base := html.EscapeString(s.cfg.PanelPath)
	if base == "" {
		base = html.EscapeString(config.DefaultPanelPath)
	}
	doc := strings.Replace(string(raw), "data-base=\""+config.DefaultPanelPath+"\"", "data-base=\""+base+"\"", 1)
	return []byte(doc)
}

// serveScript serves the installer and toolbox scripts hosted on this node so
// that mainland hosts can fetch them without reaching GitHub.
func (s *Server) serveScript(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := filepath.Join(s.scriptDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

type sessionPayload struct {
	Authenticated  bool   `json:"authenticated"`
	Version        string `json:"version"`
	BasePath       string `json:"base_path"`
	PublicURL      string `json:"public_url"`
	CSRF           string `json:"csrf,omitempty"`
	IssuedAt       int64  `json:"issued_at,omitempty"`
	ExpiresAt      int64  `json:"expires_at,omitempty"`
	KeyFingerprint string `json:"key_fingerprint"`
}

func (s *Server) sessionPayload(r *http.Request) sessionPayload {
	out := s.basePayload()
	sess, ok := s.auth.Verify(r)
	if !ok {
		return out
	}
	return s.payloadFor(sess)
}

func (s *Server) basePayload() sessionPayload {
	return sessionPayload{
		Version: version.Version, BasePath: s.cfg.PanelPath, PublicURL: s.cfg.PanelURL(),
		KeyFingerprint: fingerprint(s.cfg.AccessKeySHA256),
	}
}

func (s *Server) payloadFor(sess auth.Session) sessionPayload {
	out := s.basePayload()
	out.Authenticated = true
	out.CSRF = sess.CSRF
	out.IssuedAt = sess.Issued
	out.ExpiresAt = sess.Expires
	return out
}

func fingerprint(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionPayload(r))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !auth.OriginAllowed(r, s.publicOrigin()) {
		writeError(w, http.StatusForbidden, "origin is not allowed")
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ip := auth.ClientIP(r)
	limiter := s.auth.Limiter()
	if !limiter.Allow(ip) {
		s.audit("login", "rate limited", "fail", ip)
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	if !s.auth.VerifyAccessKey(body.Key) {
		limiter.Fail(ip)
		s.audit("login", "invalid access key", "fail", ip)
		writeError(w, http.StatusUnauthorized, "access key is incorrect")
		return
	}
	limiter.Reset(ip)
	token, sess, err := s.auth.Issue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue session failed")
		return
	}
	secure := s.cfg.PublicScheme() == "https"
	http.SetCookie(w, s.auth.Cookie(token, secure))
	s.audit("login", "success", "ok", ip)
	writeJSON(w, http.StatusOK, s.payloadFor(sess))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if sess, ok := s.auth.Verify(r); ok {
		if !s.checkCSRF(r, sess) {
			writeError(w, http.StatusForbidden, "invalid csrf token")
			return
		}
		s.audit("logout", "session closed", "ok", auth.ClientIP(r))
	}
	http.SetCookie(w, s.auth.ClearCookie())
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	sess, ok := s.auth.Verify(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return auth.Session{}, false
	}
	return sess, true
}

func (s *Server) checkCSRF(r *http.Request, sess auth.Session) bool {
	if !auth.OriginAllowed(r, s.publicOrigin()) {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) == 1
}

func (s *Server) guard(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	sess, ok := s.requireAuth(w, r)
	if !ok {
		return auth.Session{}, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if !s.checkCSRF(r, sess) {
			writeError(w, http.StatusForbidden, "invalid csrf token")
			return auth.Session{}, false
		}
	}
	return sess, true
}

func (s *Server) publicOrigin() string {
	u, err := url.Parse(s.cfg.PublicURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host := hostInfo()
	latest, _ := s.host.Latest()
	host["cpu_percent"] = latest.CPUPercent
	host["mem_total"] = latest.MemTotal
	host["mem_used"] = latest.MemUsed
	host["mem_available"] = latest.MemAvailable
	host["swap_total"] = latest.SwapTotal
	host["swap_used"] = latest.SwapUsed
	host["net_rx_rate"] = latest.NetRxRate
	host["net_tx_rate"] = latest.NetTxRate
	host["disk_read_rate"] = latest.DiskReadRate
	host["disk_write_rate"] = latest.DiskWriteRate
	host["tcp_connections"] = latest.TCPConnections
	host["established"] = latest.Established
	host["time_wait"] = latest.TimeWait
	host["listen"] = latest.Listen
	host["conntrack"] = latest.Conntrack
	host["processes"] = latest.Processes
	host["load1"] = latest.Load1
	host["load5"] = latest.Load5
	host["load15"] = latest.Load15
	host["ifaces"] = s.host.Ifaces()
	backups, _ := s.backups.List()
	latestBackup := ""
	if len(backups) > 0 {
		latestBackup = backups[0].ID
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of":           config.NowUTC(),
		"refresh_seconds": hostSampleSeconds,
		"host":            host,
		"certificate":     s.certificateSummary(),
		"services":        s.serviceRows(),
		"proxies":         s.proxyOverview(false), // overview keeps the payload small
		"backups":         map[string]interface{}{"count": len(backups), "latest": latestBackup},
		"panel":           s.panelInfo(),
		"dns":             s.dnsInfo(),
		"sni":             s.sniInfo(),
		"audit":           s.auditItems(8, ""),
	})
}

func (s *Server) panelInfo() map[string]interface{} {
	return map[string]interface{}{
		"name": "Warden Gateway", "version": version.Version,
		"public_url": s.cfg.PanelURL(), "base_path": s.cfg.PanelPath,
		"started_at":      s.started.UTC().Format(time.RFC3339),
		"uptime_seconds":  int64(time.Since(s.started).Seconds()),
		"key_fingerprint": fingerprint(s.cfg.AccessKeySHA256),
		"session_ttl":     "12h",
		"config_path":     s.cfgPath, "data_dir": s.dataDir,
	}
}

func (s *Server) dnsInfo() map[string]interface{} {
	info := map[string]interface{}{
		"enabled": s.cfg.DNS.Enabled, "listen": s.cfg.DNS.Listen,
		"gateway_ip": s.cfg.DNS.GatewayIP, "domains": s.cfg.DNS.Domains,
		"upstream": s.cfg.DNS.Upstream,
	}
	if s.dns != nil {
		st := s.dns.Status()
		info["enabled"] = st.Enabled
		info["running"] = st.Running
		info["active_listen"] = st.Listen
		info["active_port"] = st.Port
		info["fallback"] = st.Fallback
		info["port53"] = st.Port == "53"
		if st.Error != "" {
			info["error"] = st.Error
		}
	} else {
		info["running"] = false
		info["active_listen"] = ""
		info["active_port"] = ""
		info["fallback"] = false
		info["port53"] = false
	}
	return info
}

func (s *Server) sniInfo() map[string]interface{} {
	return map[string]interface{}{
		"enabled": s.cfg.SNI.Enabled, "panel_domain": s.cfg.SNI.PanelDomain,
		"passthrough": s.cfg.SNI.Passthrough, "entry": s.cfg.Listen.Entry,
	}
}

func (s *Server) certificateSummary() map[string]interface{} {
	addr := s.cfg.Listen.HTTPS
	if addr == "" {
		addr = ":443"
	}
	serverName := s.cfg.Domain
	if serverName == "" {
		serverName = s.cfg.SNI.PanelDomain
	}
	sum, err := gateway.CertificateSummary(addr, serverName)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error(), "domain": serverName}
	}
	return sum
}

// metricsRanges maps the accepted range names to their duration in minutes.
var metricsRanges = map[string]int{"1h": 60, "6h": 360, "24h": 1440}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "1h"
	}
	minutes, ok := metricsRanges[rangeName]
	if !ok {
		writeError(w, http.StatusBadRequest, "range must be 1h, 6h or 24h")
		return
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	series := s.host.Series(since)
	if len(series) > 360 {
		step := (len(series) + 359) / 360
		reduced := make([]HostSample, 0, 360)
		for i := 0; i < len(series); i += step {
			reduced = append(reduced, series[i])
		}
		series = reduced
	}
	latest, _ := s.host.Latest()
	step := metricsStep(minutes)
	total, per := s.stats.Series(minutes, step)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"range": rangeName, "as_of": config.NowUTC(), "points": series,
		"sample_seconds": hostSampleSeconds, "refresh_seconds": hostSampleSeconds,
		"latest": latest, "host": hostInfo(), "ifaces": s.host.Ifaces(),
		"counters": s.stats.Counters(), "traffic": total,
		"traffic_total": total, "proxy_series": per, "step_seconds": step,
	})
}

// metricsStep picks an aggregation step that keeps the chart series readable.
func metricsStep(minutes int) int {
	step := minutes * 60 / 120
	if step < telemetry.BucketSeconds {
		step = telemetry.BucketSeconds
	}
	return step / telemetry.BucketSeconds * telemetry.BucketSeconds
}

func (s *Server) proxyOverview(withSeries bool) []map[string]interface{} {
	details := s.stats.Detail(60, withSeries)
	byName := map[string]telemetry.ProxyDetail{}
	for _, d := range details {
		byName[d.Name] = d
	}
	minute := map[string]telemetry.ProxyDetail{}
	for _, d := range s.stats.Detail(1, false) {
		minute[d.Name] = d
	}
	rows := []map[string]interface{}{}
	add := func(name, label, path string, enabled bool) {
		d := byName[name]
		m := minute[name]
		series := d.Series
		if withSeries && len(series) == 0 {
			// Channels that saw no traffic yet still need an aligned axis.
			series = telemetry.EmptySeries(60, telemetry.BucketSeconds)
		}
		rows = append(rows, map[string]interface{}{
			"last_minute": map[string]interface{}{
				"requests": m.Requests, "failures": m.Failures,
				"bytes_in": m.BytesIn, "bytes_out": m.BytesOut,
				"in_per_sec": round1(float64(m.BytesIn) / 60), "out_per_sec": round1(float64(m.BytesOut) / 60),
				"requests_per_min": m.Requests, "failures_per_min": m.Failures,
			},
			"name": name, "label": label, "path": path, "enabled": enabled,
			"requests": d.Requests, "failures": d.Failures,
			"bytes_in": d.BytesIn, "bytes_out": d.BytesOut,
			"last_status": d.LastStatus, "last_at": d.LastAt, "last_error": d.LastError,
			"success_rate": round1(d.SuccessRate), "in_flight": d.InFlight,
			"status_classes": d.StatusClasses, "window": d.Window,
			"rates": d.Rates, "latency": d.Latency, "targets": d.Targets,
			"series": series,
		})
	}
	add("docker", "Docker 镜像代理", "/v2/", s.cfg.Docker.Enabled)
	add("github", "GitHub 下载代理", "/https://", s.cfg.GitHub.Enabled)
	add("dns", "DNS 分流", s.cfg.DNS.Listen, s.cfg.DNS.Enabled)
	add("sni", "SNI 入口", s.cfg.Listen.Entry, s.cfg.SNI.Enabled)
	return rows
}

// round1 keeps one decimal place for ratios rendered by the panel.
func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func (s *Server) handleProxies(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of": config.NowUTC(), "proxies": s.proxyOverview(true),
		"recent": s.stats.Recent(100),
	})
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result := s.probe(body.Name)
	s.audit("probe", body.Name+" "+fmt.Sprint(result["ok"]), "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) probe(name string) map[string]interface{} {
	start := time.Now()
	fail := func(err error) map[string]interface{} {
		return map[string]interface{}{"name": name, "ok": false, "latency_ms": time.Since(start).Milliseconds(), "error": err.Error()}
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	base := "http://" + s.cfg.Listen.HTTP
	switch name {
	case "docker":
		resp, err := client.Get(base + "/v2/")
		if err != nil {
			return fail(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return map[string]interface{}{"name": name, "ok": resp.StatusCode < 500, "status": resp.StatusCode, "latency_ms": time.Since(start).Milliseconds()}
	case "github":
		resp, err := client.Get(base + "/https://github.com/robots.txt")
		if err != nil {
			return fail(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return map[string]interface{}{"name": name, "ok": resp.StatusCode == 200, "status": resp.StatusCode, "latency_ms": time.Since(start).Milliseconds()}
	case "panel":
		resp, err := client.Get(base + s.cfg.PanelPath + "/api/session")
		if err != nil {
			return fail(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return map[string]interface{}{"name": name, "ok": resp.StatusCode == 200, "status": resp.StatusCode, "latency_ms": time.Since(start).Milliseconds()}
	case "sni":
		conn, err := net.DialTimeout("tcp", s.cfg.Listen.Entry, 5*time.Second)
		if err != nil {
			return fail(err)
		}
		_ = conn.Close()
		return map[string]interface{}{"name": name, "ok": true, "latency_ms": time.Since(start).Milliseconds()}
	case "dns":
		if !s.cfg.DNS.Enabled {
			return map[string]interface{}{"name": name, "ok": false, "error": "dns is disabled"}
		}
		conn, err := net.DialTimeout("tcp", s.cfg.DNS.Listen, 5*time.Second)
		if err != nil {
			return fail(err)
		}
		_ = conn.Close()
		return map[string]interface{}{"name": name, "ok": true, "latency_ms": time.Since(start).Milliseconds()}
	default:
		return map[string]interface{}{"name": name, "ok": false, "error": "unknown target"}
	}
}

type serviceRow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Unit        string   `json:"unit"`
	Active      string   `json:"active"`
	Sub         string   `json:"sub"`
	Description string   `json:"description"`
	LoadState   string   `json:"load_state"`
	Actions     []string `json:"actions"`
}

var managedServices = []struct {
	ID, Name, Unit string
}{
	{"gateway", "Warden Gateway", defaultService},
}

func (s *Server) serviceRows() []serviceRow {
	rows := make([]serviceRow, 0, len(managedServices))
	for _, svc := range managedServices {
		row := s.serviceRow(svc.ID, svc.Name, svc.Unit)
		if row.LoadState == "not-found" {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *Server) serviceRow(id, name, unit string) serviceRow {
	row := serviceRow{ID: id, Name: name, Unit: unit, Actions: []string{"start", "stop", "restart"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--no-page", "--property=ActiveState,SubState,Description,LoadState").Output()
	if err != nil {
		row.LoadState = "not-found"
		return row
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			row.Active = v
		case "SubState":
			row.Sub = v
		case "Description":
			row.Description = v
		case "LoadState":
			row.LoadState = v
		}
	}
	if row.Active == "" {
		row.Active = "unknown"
	}
	return row
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"as_of": config.NowUTC(), "services": s.serviceRows()})
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request, rest string) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "action" {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	allowed := map[string]bool{"start": true, "stop": true, "restart": true}
	if !allowed[body.Action] {
		writeError(w, http.StatusBadRequest, "action must be start, stop or restart")
		return
	}
	unit := ""
	for _, svc := range managedServices {
		if svc.ID == id {
			unit = svc.Unit
			break
		}
	}
	if unit == "" {
		writeError(w, http.StatusNotFound, "unknown service")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", body.Action, unit).CombinedOutput()
	result := "ok"
	if err != nil {
		result = "fail"
	}
	s.audit("service."+body.Action, unit+" "+result, result, auth.ClientIP(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, strings.TrimSpace(string(out))+" "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "unit": unit, "action": body.Action, "output": strings.TrimSpace(string(out))})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.safeConfig())
}

func (s *Server) safeConfig() map[string]interface{} {
	return map[string]interface{}{
		"version": s.cfg.Version, "public_url": s.cfg.PanelURL(), "base_path": s.cfg.PanelPath,
		"domain": s.cfg.Domain, "email": s.cfg.Email,
		"tls":             map[string]interface{}{"mode": s.cfg.TLS.Mode, "cache_dir": s.cfg.TLS.CacheDir},
		"listen":          s.cfg.Listen,
		"docker":          map[string]interface{}{"enabled": s.cfg.Docker.Enabled, "default_registry": s.cfg.Docker.DefaultRegistry, "referrers_stub": s.cfg.Docker.ReferrersStub},
		"github":          map[string]interface{}{"enabled": s.cfg.GitHub.Enabled, "allow_hosts": s.cfg.GitHub.AllowHosts, "max_body_bytes": s.cfg.GitHub.MaxBodyBytes},
		"sni":             s.sniInfo(),
		"dns":             s.dnsInfo(),
		"backup":          map[string]interface{}{"dir": s.cfg.Backup.Dir, "retain": s.cfg.Backup.Retain},
		"key_fingerprint": fingerprint(s.cfg.AccessKeySHA256),
		"config_path":     s.cfgPath, "data_dir": s.dataDir,
	}
}

func (s *Server) handleCert(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.certificateSummary())
}

func (s *Server) handleCertRenew(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result := map[string]interface{}{"ok": true, "message": "证书由内置 ACME 自动续期，无需手动操作", "checked_at": config.NowUTC()}
	s.audit("cert.renew", "requested", "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleKeyChange(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.guard(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		CurrentKey string `json:"current_key"`
		NewKey     string `json:"new_key"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.auth.VerifyAccessKey(body.CurrentKey) {
		s.audit("key.change", "current key is incorrect", "fail", auth.ClientIP(r))
		writeError(w, http.StatusUnauthorized, "当前访问密钥不正确")
		return
	}
	if err := config.ValidateAccessKey(body.NewKey); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	newHash := config.HashAccessKey(body.NewKey)
	oldHash := s.cfg.AccessKeySHA256
	s.cfg.AccessKeySHA256 = newHash
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.AccessKeySHA256 = oldHash
		writeError(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	s.auth.SetKeyHash(newHash)
	token, newSess, err := s.auth.Issue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue session failed")
		return
	}
	secure := s.cfg.PublicScheme() == "https"
	http.SetCookie(w, s.auth.Cookie(token, secure))
	s.audit("key.change", "access key updated", "ok", auth.ClientIP(r))
	_ = sess
	writeJSON(w, http.StatusOK, s.payloadFor(newSess))
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.backups.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"backups": list, "as_of": config.NowUTC()})
	case http.MethodPost:
		man, err := s.backups.Create()
		if err != nil {
			s.audit("backup.create", err.Error(), "fail", auth.ClientIP(r))
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit("backup.create", man.ID, "ok", auth.ClientIP(r))
		writeJSON(w, http.StatusOK, man)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBackupItem(w http.ResponseWriter, r *http.Request, rest string) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid backup id")
		return
	}
	if len(parts) == 2 && parts[1] == "download" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path, err := s.backups.ArchivePath(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+id+".tar.gz\"")
		http.ServeFile(w, r, path)
		s.audit("backup.download", id, "ok", auth.ClientIP(r))
		return
	}
	if len(parts) == 2 && parts[1] == "restore" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		man, err := s.backups.Restore(id)
		if err != nil {
			s.audit("backup.restore", id+" "+err.Error(), "fail", auth.ClientIP(r))
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit("backup.restore", id, "ok", auth.ClientIP(r))
		s.scheduleRestart()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "backup": man, "restarting": true})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := s.backups.Delete(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit("backup.delete", id, "ok", auth.ClientIP(r))
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "id": id})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) scheduleRestart() {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	go func() {
		time.Sleep(700 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// --no-block returns before systemd stops this unit: without it the
		// client process runs in the service cgroup and is killed by the very
		// stop it requested, which leaves the unit stopped instead of restarted.
		out, err := exec.CommandContext(ctx, "systemctl", "restart", "--no-block", s.serviceName).CombinedOutput()
		if err != nil {
			s.log.Errorf("restart request failed: %v %s", err, strings.TrimSpace(string(out)))
		}
	}()
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": s.auditItems(limit, r.URL.Query().Get("q"))})
}

type auditItem struct {
	At     string `json:"ts"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	Result string `json:"result"`
	IP     string `json:"ip"`
}

func (s *Server) audit(action, detail, result, ip string) {
	entry := auditItem{At: config.NowUTC(), Action: action, Detail: detail, Result: result, IP: ip}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if info, err := os.Stat(s.auditPath); err == nil && info.Size() >= maxAuditBytes {
		_ = os.Rename(s.auditPath, s.auditPath+".1")
	}
	f, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(raw, '\n'))
	_ = f.Close()
}

func (s *Server) auditItems(limit int, q string) []auditItem {
	if limit <= 0 {
		limit = 100
	}
	raw, err := os.ReadFile(s.auditPath)
	if err != nil {
		return []auditItem{}
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]auditItem, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var item auditItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(item.Action+" "+item.Detail+" "+item.IP), q) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, sessionBodyMax)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}
