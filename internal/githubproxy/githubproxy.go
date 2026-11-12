package githubproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

var hopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

type Handler struct {
	cfg     config.GitHubConfig
	matcher *hostMatcher
	client  *http.Client
	stats   *telemetry.Store
	log     *logging.Logger
}

func New(cfg config.GitHubConfig, stats *telemetry.Store, log *logging.Logger) *Handler {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
	}
	matcher := newHostMatcher(cfg.AllowHosts)
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if !matcher.Match(req.URL.Hostname()) {
				return fmt.Errorf("redirect host %q is not allowed", req.URL.Hostname())
			}
			return nil
		},
	}
	h := &Handler{
		cfg:     cfg,
		matcher: matcher,
		stats:   stats,
		log:     log,
		client:  client,
	}
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		http.Error(w, "GitHub proxy is disabled", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/" || r.URL.Path == "" {
		http.Error(w, "usage: /https://github.com/owner/repo", http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/http://") && !strings.HasPrefix(r.URL.Path, "/https://") {
		http.Error(w, "target must start with /https:// or /http://", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost && r.Method != http.MethodOptions {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target, err := parseTarget(r.URL.Path, r.URL.RawQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.matcher.Match(target.Hostname()) {
		http.Error(w, "target host is not allowed", http.StatusForbidden)
		return
	}
	if r.ContentLength > h.cfg.MaxBodyBytes {
		http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(h.cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	var body io.Reader
	if r.Body != nil {
		body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), body)
	if err != nil {
		http.Error(w, "invalid upstream request", http.StatusBadRequest)
		return
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Set("Host", target.Host)
	req.Host = target.Host
	req.Header.Set("X-Forwarded-Host", r.Host)
	req.Header.Set("X-Forwarded-For", clientIP(r))
	call := h.stats.Begin("github")
	targetHost := target.Hostname()
	started := time.Now()
	upstreamMs := int64(0)
	stopUpstream := func() {
		if upstreamMs == 0 {
			upstreamMs = time.Since(started).Milliseconds()
		}
	}
	resp, err := h.client.Do(req)
	if err == nil {
		stopUpstream()
	}
	if err != nil {
		if ctx.Err() != nil {
			http.Error(w, "upstream request timed out", http.StatusGatewayTimeout)
			call.DoneTarget(targetHost, http.StatusGatewayTimeout, r.ContentLength, 0, err.Error(), r.RemoteAddr, r.Method, r.URL.Path)
			return
		}
		h.log.Debugf("github proxy: %s %s: %v", r.Method, target.Redacted(), err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		call.DoneTarget(targetHost, http.StatusBadGateway, r.ContentLength, 0, err.Error(), r.RemoteAddr, r.Method, r.URL.Path)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	n, copyErr := io.CopyBuffer(w, resp.Body, make([]byte, 32*1024))
	errText := ""
	if copyErr != nil {
		errText = copyErr.Error()
	}
	call.DoneUpstream(targetHost, upstreamMs, resp.StatusCode, r.ContentLength, n, errText, r.RemoteAddr, r.Method, r.URL.Path)
	h.log.Debugf("github proxy: %s %s -> %d %dB in %dms", r.Method, target.Redacted(), resp.StatusCode, n, time.Since(started).Milliseconds())
}

func parseTarget(path, rawQuery string) (*url.URL, error) {
	raw := strings.TrimPrefix(path, "/")
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid target URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("target scheme must be http or https")
	}
	if u.User != nil {
		return nil, errors.New("target URL must not contain user info")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("target host is required")
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return nil, errors.New("target port must be 80 or 443")
	}
	u.RawQuery = rawQuery
	u.Fragment = ""
	return u, nil
}

func copyHeaders(dst, src http.Header) {
	for k, values := range src {
		ck := http.CanonicalHeaderKey(k)
		if _, skip := hopHeaders[ck]; skip {
			continue
		}
		for _, v := range values {
			dst.Add(ck, v)
		}
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

type hostMatcher struct {
	exact    map[string]struct{}
	suffixes []string
}

func newHostMatcher(patterns []string) *hostMatcher {
	m := &hostMatcher{exact: map[string]struct{}{}}
	for _, raw := range patterns {
		p := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "*.") {
			s := strings.TrimPrefix(p, "*.")
			if s != "" {
				m.suffixes = append(m.suffixes, s)
			}
			continue
		}
		m.exact[p] = struct{}{}
	}
	return m
}

func (m *hostMatcher) Match(host string) bool {
	h := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	if h == "" {
		return false
	}
	if _, ok := m.exact[h]; ok {
		return true
	}
	for _, s := range m.suffixes {
		if strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
