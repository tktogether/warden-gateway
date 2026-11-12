package panel

import (
	"net"
	"net/http"
	"strings"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/cn"
	"github.com/tktogether/warden-gateway/internal/config"
)

// handleDNSConfig turns the gateway DNS service on or off and saves the
// config. The process is restarted afterwards because the DNS listener is
// created at startup.
func (s *Server) handleDNSConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Enabled   bool     `json:"enabled"`
		GatewayIP string   `json:"gateway_ip"`
		Listen    string   `json:"listen"`
		Upstream  string   `json:"upstream"`
		Domains   []string `json:"domains"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	next := s.cfg.DNS
	if host := strings.TrimSpace(body.GatewayIP); host != "" {
		next.GatewayIP = host
	}
	if listen := strings.TrimSpace(body.Listen); listen != "" {
		next.Listen = listen
	}
	if upstream := strings.TrimSpace(body.Upstream); upstream != "" {
		next.Upstream = upstream
	}
	if domains := body.Domains; len(domains) > 0 {
		next.Domains = domains
	}
	if len(next.Domains) == 0 {
		next.Domains = cn.DefaultDomains()
	}
	if next.GatewayIP == "" {
		next.GatewayIP = resolveGateway(s.cfg.PublicHost())
	}
	next.Enabled = body.Enabled
	if next.Enabled {
		if ip := net.ParseIP(next.GatewayIP); ip == nil || ip.To4() == nil {
			writeError(w, http.StatusBadRequest, "启用 DNS 分流需要有效的 IPv4 网关地址")
			return
		}
	}
	previous := s.cfg.DNS
	s.cfg.DNS = next
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.DNS = previous
		writeError(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	state := "stopped"
	if next.Enabled {
		state = "started"
	}
	s.audit("dns.config", state, "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"restarting": true,
		"dns":        s.dnsInfo(),
	})
	s.scheduleRestart()
}

// handleDNSStatus reports the gateway DNS settings used by the settings page.
func (s *Server) handleDNSStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of":                config.NowUTC(),
		"dns":                  s.dnsInfo(),
		"domains":              cn.DefaultDomains(),
		"suggested_gateway_ip": resolveGateway(s.cfg.PublicHost()),
		"local_resolver_mode":  localResolverMode(),
	})
}

// localResolverMode reports how this machine redirects traffic today.
func localResolverMode() string {
	status, err := cn.New().Status()
	if err != nil {
		return "none"
	}
	return status.Mode()
}
