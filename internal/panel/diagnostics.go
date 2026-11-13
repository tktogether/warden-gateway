package panel

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/diagnostics"
)

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	checker := &diagnostics.Checker{
		Config:      func() *config.Config { return s.cfg },
		ConfigPath:  s.cfgPath,
		DataDir:     s.dataDir,
		ServiceName: s.serviceName,
		ServiceStatus: func() (string, string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "systemctl", "is-active", s.serviceName).Output()
			if err != nil {
				return strings.TrimSpace(string(out)), "未运行"
			}
			return strings.TrimSpace(string(out)), ""
		},
		Command: func(name string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		DNSState: func() diagnostics.DNSState {
			if s.dns == nil {
				return diagnostics.DNSState{}
			}
			st := s.dns.Status()
			return diagnostics.DNSState{Running: st.Running, Listen: st.Listen, Port: st.Port, Fallback: st.Fallback, Error: st.Error}
		},
		SingBox: s.singboxDiagState,
	}
	writeJSON(w, http.StatusOK, checker.Run(ctx))
}

// handleCNCommands renders the commands a CN machine should run. The gateway
// never applies these settings to itself, so this endpoint only generates text.
func (s *Server) handleCNCommands(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gateway := strings.TrimSpace(s.cfg.DNS.GatewayIP)
	host := s.cfg.PublicHost()
	if host == "" {
		host = gateway
	}
	if gateway == "" {
		gateway = resolveGateway(host)
	}
	origin := s.publicOrigin()
	scriptURL := origin + "/bootstrap.sh"
	info := s.dnsInfo()
	dnsReady := false
	if enabled, _ := info["enabled"].(bool); enabled {
		if running, _ := info["running"].(bool); running {
			if port53, _ := info["port53"].(bool); port53 {
				dnsReady = true
			}
		}
	}
	baseArgs := []string{"cn-setup"}
	if gateway != "" {
		baseArgs = append(baseArgs, "--gateway-ip", gateway)
	}
	if origin != "" {
		baseArgs = append(baseArgs, "--docker-mirror", origin, "--git-mirror", origin)
	}
	cnCommand := func(splitMode string) string {
		args := append(append([]string{}, baseArgs...), "--cn-mode", splitMode, "--yes")
		if origin == "" {
			return "sudo bash bootstrap.sh " + strings.Join(args, " ")
		}
		return "curl -fsSL " + scriptURL + " -o /tmp/wg-bootstrap.sh && sudo bash /tmp/wg-bootstrap.sh " + strings.Join(args, " ")
	}
	// v1.5.0: the CN machine no longer picks a split mode. "auto" lets the
	// bootstrap script write the managed hosts block and run a local dnsmasq
	// (198.18.0.1:53) fed from that same block; "none" is the explicit
	// downgrade that leaves splitting off entirely.
	commands := map[string]interface{}{}
	for _, splitMode := range []string{"auto", "none"} {
		commands[splitMode] = cnCommand(splitMode)
	}
	command := commands["auto"].(string)
	// Restore must not assume a bootstrap.sh in the current directory: either
	// download the script again, or reuse the copy that cn-setup registered as
	// warden-cn on that machine.
	restoreCommand := "sudo warden-cn cn-restore"
	if origin != "" {
		restoreCommand = "curl -fsSL " + scriptURL + " -o /tmp/wg-bootstrap.sh && sudo bash /tmp/wg-bootstrap.sh cn-restore"
	}
	// health_url points at the gateway's own /healthz on port 80 so the CN
	// machine can verify the split path end to end; check_command re-runs the
	// self-check on the CN machine with the same script the setup used.
	healthURL := ""
	checkCommand := "sudo warden-cn cn-check"
	if origin != "" {
		healthURL = origin + "/healthz"
		checkCommand = "curl -fsSL " + scriptURL + " -o /tmp/wg-bootstrap.sh && sudo bash /tmp/wg-bootstrap.sh cn-check"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of":           config.NowUTC(),
		"origin":          origin,
		"gateway_ip":      gateway,
		"panel_host":      host,
		"domains":         config.DefaultSplitDomains(),
		"script_url":      scriptURL,
		"command":         command,
		"commands":        commands,
		"restore_command": restoreCommand,
		"health_url":      healthURL,
		"check_command":   checkCommand,
		"cn_mode":         "auto",
		"dns":             info,
		"dns_ready":       dnsReady,
	})
}

// resolveGateway returns the address that other machines should use to reach
// this gateway. It prefers an IPv4 literal and falls back to resolving the
// panel host name.
func resolveGateway(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ip4 := addr.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}
