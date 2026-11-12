package panel

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/cn"
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
	}
	writeJSON(w, http.StatusOK, checker.Run(ctx))
}

func (s *Server) handleCNStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, err := cn.New().Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	gateway := status.Hosts.IP
	if gateway == "" && s.cfg.DNS.Enabled {
		gateway = s.cfg.DNS.GatewayIP
	}
	host := s.cfg.PublicHost()
	if host == "" {
		host = gateway
	}
	if gateway == "" {
		gateway = resolveGateway(host)
	}
	origin := s.publicOrigin()
	scriptURL := origin + "/bootstrap.sh"
	toolboxURL := origin + "/toolbox.sh"
	localMode := status.Mode()
	mode := localMode
	if mode == "none" {
		if s.cfg.DNS.Enabled {
			mode = "dns"
		} else {
			mode = "hosts"
		}
	}
	baseArgs := []string{"cn-setup"}
	if gateway != "" {
		baseArgs = append(baseArgs, "--gateway-ip", gateway)
	}
	if origin != "" {
		baseArgs = append(baseArgs, "--docker-mirror", origin, "--git-mirror", origin)
	}
	cnCommand := func(script, localName, tmpName, splitMode string) string {
		args := append(append([]string{}, baseArgs...), "--cn-mode", splitMode, "--yes")
		if origin == "" {
			return "sudo bash " + localName + " " + strings.Join(args, " ")
		}
		return "curl -fsSL " + script + " -o " + tmpName + " && sudo bash " + tmpName + " " + strings.Join(args, " ")
	}
	commands := map[string]interface{}{}
	toolboxCommands := map[string]interface{}{}
	for _, splitMode := range []string{"hosts", "dns", "both"} {
		commands[splitMode] = cnCommand(scriptURL, "bootstrap.sh", "/tmp/wg-bootstrap.sh", splitMode)
		toolboxCommands[splitMode] = cnCommand(toolboxURL, "toolbox.sh", "/tmp/wg-toolbox.sh", splitMode)
	}
	command := commands[mode].(string)
	toolboxCommand := toolboxCommands[mode].(string)
	restoreCommand := "sudo bash bootstrap.sh cn-restore"
	if origin != "" {
		restoreCommand = "curl -fsSL " + scriptURL + " -o /tmp/wg-bootstrap.sh && sudo bash /tmp/wg-bootstrap.sh cn-restore"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of":            config.NowUTC(),
		"origin":           origin,
		"gateway_ip":       gateway,
		"panel_host":       host,
		"domains":          cn.DefaultDomains(),
		"script_url":       scriptURL,
		"toolbox_url":      toolboxURL,
		"command":          command,
		"commands":         commands,
		"toolbox_command":  toolboxCommand,
		"toolbox_commands": toolboxCommands,
		"restore_command":  restoreCommand,
		"cn_mode":          mode,
		"local_mode":       localMode,
		"dns":              s.dnsInfo(),
		"resolver_mode":    localMode,
		"backup_root":      status.BackupRoot,
		"latest_backup":    status.LatestBackup,
		"backups":          status.Backups,
		"status":           status,
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

func (s *Server) handleCNApply(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		APTMirror    string   `json:"apt_mirror"`
		DockerMirror string   `json:"docker_mirror"`
		GitMirror    string   `json:"git_mirror"`
		GatewayIP    string   `json:"gateway_ip"`
		Domains      []string `json:"domains"`
		Mode         string   `json:"mode"`
		Hosts        bool     `json:"hosts"`
		DNS          bool     `json:"dns"`
		APT          bool     `json:"apt"`
		BBR          bool     `json:"bbr"`
		Docker       bool     `json:"docker"`
		Git          bool     `json:"git"`
		APTUpdate    bool     `json:"apt_update"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	manager := cn.New()
	gateway := strings.TrimSpace(body.GatewayIP)
	if gateway == "" {
		gateway = manager.DetectIP()
	}
	opts := cn.Options{
		APTMirror: body.APTMirror, DockerMirror: body.DockerMirror, GitMirror: body.GitMirror,
		GatewayIP: gateway, Domains: body.Domains, Mode: body.Mode,
		Hosts: body.Hosts, DNS: body.DNS, APT: body.APT, BBR: body.BBR, Docker: body.Docker, Git: body.Git,
		APTUpdate: body.APTUpdate,
	}
	report, err := manager.Apply(opts)
	if err != nil {
		s.audit("cn.apply", err.Error(), "fail", auth.ClientIP(r))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit("cn.apply", "backup="+report.BackupID, "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleCNRestore(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	report, err := cn.New().Restore(body.ID)
	if err != nil {
		s.audit("cn.restore", err.Error(), "fail", auth.ClientIP(r))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit("cn.restore", report.BackupID, "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, report)
}
