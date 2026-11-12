package panel

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/dnsproxy"
)

// handleDNSConfig toggles the gateway DNS listener. Enabling it probes the
// candidate addresses so the panel can report the address that is actually
// free instead of leaving the service to crash on a busy port.
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
	if strings.TrimSpace(body.GatewayIP) != "" {
		next.GatewayIP = strings.TrimSpace(body.GatewayIP)
	}
	if strings.TrimSpace(body.Listen) != "" {
		next.Listen = strings.TrimSpace(body.Listen)
	}
	if strings.TrimSpace(body.Upstream) != "" {
		next.Upstream = strings.TrimSpace(body.Upstream)
	}
	if len(body.Domains) > 0 {
		next.Domains = body.Domains
	}
	if len(next.Domains) == 0 {
		next.Domains = config.DefaultSplitDomains()
	}
	if strings.TrimSpace(next.GatewayIP) == "" {
		next.GatewayIP = resolveGateway(s.cfg.PublicHost())
	}
	next.Enabled = body.Enabled
	if next.Enabled && strings.TrimSpace(next.GatewayIP) == "" {
		writeError(w, http.StatusBadRequest, "启用 DNS 分流需要有效的 IPv4 网关地址")
		return
	}
	note := ""
	if next.Enabled && !s.cfg.DNS.Enabled {
		preferred := next.Listen
		chosen, err := dnsproxy.SelectListen(next)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("没有可用的 DNS 监听地址（首选 %s）：%v", preferred, err))
			return
		}
		next.Listen = chosen
		_, port, splitErr := net.SplitHostPort(chosen)
		if splitErr != nil {
			port = ""
		}
		if port != "53" {
			note = fmt.Sprintf("端口 53 已被占用，DNS 已回退到 %s；国内机需把 resolver 指向该地址，且 resolver 需支持自定义端口", chosen)
		}
		if chosen != preferred {
			reason := fmt.Sprintf("首选地址 %s 不可用，已改用 %s", preferred, chosen)
			if note != "" {
				reason = note + "；" + reason
			}
			note = reason
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
	s.audit("dns.config", state+" "+next.Listen, "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "restarting": true, "note": note, "dns": s.dnsInfo(),
	})
	s.scheduleRestart()
}

func (s *Server) handleDNSStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gateway := strings.TrimSpace(s.cfg.DNS.GatewayIP)
	if gateway == "" {
		gateway = resolveGateway(s.cfg.PublicHost())
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of":                config.NowUTC(),
		"dns":                  s.dnsInfo(),
		"domains":              config.DefaultSplitDomains(),
		"suggested_gateway_ip": gateway,
	})
}
