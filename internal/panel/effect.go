package panel

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/config"
)

// EffectRow compares one upstream resource fetched directly by the gateway host
// with the same fetch performed through the local proxy path. It answers the
// only question the panel can answer honestly: how much does the proxy add or
// save on this host, right now.
type EffectRow struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Detail     string `json:"detail"`
	DirectMs   int64  `json:"direct_ms"`
	ProxiedMs  int64  `json:"proxied_ms"`
	DeltaMs    int64  `json:"delta_ms"`
	DirectErr  string `json:"direct_error,omitempty"`
	ProxiedErr string `json:"proxied_error,omitempty"`
	OK         bool   `json:"ok"`
}

// handleEffect runs the direct versus proxied comparison on demand. It is a
// POST because it performs outbound requests from the gateway host.
func (s *Server) handleEffect(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows := s.effectRows()
	s.audit("effect", "compared "+strconv.Itoa(len(rows)), "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of": config.NowUTC(), "rows": rows,
		"explain": "直连为网关主机直接访问上游的耗时，经代理为同一请求经本机代理转发后的耗时；差值反映代理链路开销与代理转发收益。",
	})
}

func (s *Server) effectRows() []EffectRow {
	rows := make([]EffectRow, 4)
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); rows[0] = s.effectDocker() }()
	go func() { defer wg.Done(); rows[1] = s.effectGitHub() }()
	go func() { defer wg.Done(); rows[2] = s.effectDNS() }()
	go func() { defer wg.Done(); rows[3] = s.effectSNI() }()
	wg.Wait()
	for i := range rows {
		if rows[i].DirectErr == "" && rows[i].ProxiedErr == "" {
			rows[i].OK = true
			rows[i].DeltaMs = rows[i].ProxiedMs - rows[i].DirectMs
		}
	}
	return rows
}

func (s *Server) httpTiming(rawURL string, tlsConf *tls.Config, timeout time.Duration) (int64, int, error) {
	transport := &http.Transport{
		TLSClientConfig:     tlsConf,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer transport.CloseIdleConnections()
	start := time.Now()
	resp, err := client.Get(rawURL)
	if err != nil {
		return time.Since(start).Milliseconds(), 0, err
	}
	defer resp.Body.Close()
	ms := time.Since(start).Milliseconds()
	return ms, resp.StatusCode, nil
}

func (s *Server) effectDocker() EffectRow {
	row := EffectRow{Name: "docker", Label: "Docker 镜像代理", Detail: "registry-1.docker.io/v2/"}
	if !s.cfg.Docker.Enabled {
		row.DirectErr, row.ProxiedErr = "docker proxy is disabled", "docker proxy is disabled"
		return row
	}
	var directMs int64
	var directStatus int
	var directErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		directMs, directStatus, directErr = s.httpTiming("https://registry-1.docker.io/v2/", &tls.Config{MinVersion: tls.VersionTLS12}, 8*time.Second)
	}()
	var proxiedMs int64
	var proxiedStatus int
	var proxiedErr error
	go func() {
		defer wg.Done()
		proxiedMs, proxiedStatus, proxiedErr = s.httpTiming("http://"+s.cfg.Listen.HTTP+"/v2/", nil, 8*time.Second)
	}()
	wg.Wait()
	row.DirectMs, row.ProxiedMs = directMs, proxiedMs
	if directErr != nil {
		row.DirectErr = dockerDirectText(directErr)
	} else {
		row.Detail = "registry-1.docker.io/v2/ · 直连 HTTP " + strconv.Itoa(directStatus) + " · 代理 HTTP " + strconv.Itoa(proxiedStatus)
	}
	if proxiedErr != nil {
		row.ProxiedErr = proxiedErr.Error()
	}
	return row
}

// dockerDirectText keeps upstream TLS errors short enough for the panel table.
func dockerDirectText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if i := strings.Index(text, ": "); i > 0 && len(text) > 90 {
		text = text[i+2:]
	}
	if len(text) > 90 {
		text = text[:90]
	}
	return text
}

func (s *Server) effectGitHub() EffectRow {
	row := EffectRow{Name: "github", Label: "GitHub 下载代理", Detail: "github.com/robots.txt"}
	if !s.cfg.GitHub.Enabled {
		row.DirectErr, row.ProxiedErr = "github proxy is disabled", "github proxy is disabled"
		return row
	}
	var wg sync.WaitGroup
	wg.Add(2)
	var directMs int64
	var directStatus int
	var directErr error
	go func() {
		defer wg.Done()
		directMs, directStatus, directErr = s.httpTiming("https://github.com/robots.txt", &tls.Config{MinVersion: tls.VersionTLS12}, 8*time.Second)
	}()
	var proxiedMs int64
	var proxiedStatus int
	var proxiedErr error
	go func() {
		defer wg.Done()
		proxiedMs, proxiedStatus, proxiedErr = s.httpTiming("http://"+s.cfg.Listen.HTTP+"/https://github.com/robots.txt", nil, 8*time.Second)
	}()
	wg.Wait()
	row.DirectMs, row.ProxiedMs = directMs, proxiedMs
	if directErr != nil {
		row.DirectErr = dockerDirectText(directErr)
	} else {
		row.Detail = "github.com/robots.txt · 直连 HTTP " + strconv.Itoa(directStatus) + " · 代理 HTTP " + strconv.Itoa(proxiedStatus)
	}
	if proxiedErr != nil {
		row.ProxiedErr = proxiedErr.Error()
	}
	return row
}

func (s *Server) effectDNS() EffectRow {
	row := EffectRow{Name: "dns", Label: "DNS 分流", Detail: "github.com A 记录"}
	if !s.cfg.DNS.Enabled {
		row.DirectErr, row.ProxiedErr = "dns is disabled", "dns is disabled"
		return row
	}
	msg := new(dns.Msg)
	msg.SetQuestion("github.com.", dns.TypeA)
	var wg sync.WaitGroup
	wg.Add(2)
	var directMs int64
	var directErr error
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		start := time.Now()
		resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 4 * time.Second}
			return d.DialContext(ctx, network, address)
		}}
		_, err := resolver.LookupHost(ctx, "github.com")
		directMs = time.Since(start).Milliseconds()
		directErr = err
	}()
	var proxiedMs int64
	var proxiedErr error
	go func() {
		defer wg.Done()
		client := &dns.Client{Net: "udp", Timeout: 4 * time.Second}
		start := time.Now()
		_, _, err := client.Exchange(msg, s.cfg.DNS.Listen)
		proxiedMs = time.Since(start).Milliseconds()
		proxiedErr = err
	}()
	wg.Wait()
	row.DirectMs, row.ProxiedMs = directMs, proxiedMs
	if directErr != nil {
		row.DirectErr = directErr.Error()
	}
	if proxiedErr != nil {
		row.ProxiedErr = proxiedErr.Error()
	}
	return row
}

func (s *Server) effectSNI() EffectRow {
	row := EffectRow{Name: "sni", Label: "SNI 入口", Detail: "TLS 握手 github.com"}
	if !s.cfg.SNI.Enabled {
		row.DirectErr, row.ProxiedErr = "sni is disabled", "sni is disabled"
		return row
	}
	var wg sync.WaitGroup
	wg.Add(2)
	var directMs int64
	var directErr error
	go func() {
		defer wg.Done()
		start := time.Now()
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 6 * time.Second}, "tcp", "github.com:443", &tls.Config{ServerName: "github.com", MinVersion: tls.VersionTLS12})
		if err == nil {
			_ = conn.Close()
		}
		directMs = time.Since(start).Milliseconds()
		directErr = err
	}()
	var proxiedMs int64
	var proxiedErr error
	go func() {
		defer wg.Done()
		start := time.Now()
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 6 * time.Second}, "tcp", s.cfg.Listen.Entry, &tls.Config{ServerName: "github.com", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
		if err == nil {
			_ = conn.Close()
		}
		proxiedMs = time.Since(start).Milliseconds()
		proxiedErr = err
	}()
	wg.Wait()
	row.DirectMs, row.ProxiedMs = directMs, proxiedMs
	if directErr != nil {
		row.DirectErr = directErr.Error()
	}
	if proxiedErr != nil {
		row.ProxiedErr = proxiedErr.Error()
	}
	return row
}
