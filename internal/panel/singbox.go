package panel

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/diagnostics"
	"github.com/tktogether/warden-gateway/internal/singbox"
)

// singboxPayload is everything the sing-box view renders in one round trip.
func (s *Server) singboxPayload() map[string]interface{} {
	summary := s.singbox.Summary()
	nodes := make([]map[string]interface{}, 0, len(summary.Nodes))
	for _, node := range summary.Nodes {
		entry := nodeJSON(node)
		if node.Error == "" {
			if _, links, err := s.singbox.ShareLinks(node.ID, ""); err == nil {
				entry["links"] = links
			}
		}
		nodes = append(nodes, entry)
	}
	return map[string]interface{}{
		"as_of":       config.NowUTC(),
		"installed":   summary.Installed,
		"version":     summary.Version,
		"binary":      summary.Binary,
		"root":        summary.Root,
		"config":      summary.Config,
		"service":     summary.Service,
		"state":       summary.State,
		"running":     summary.Running,
		"host":        s.singbox.Host(),
		"nodes":       nodes,
		"types":       singbox.NodeTypes(),
		"backups":     s.singbox.Backups(),
		"check":       checkJSON(summary.Check),
		"warnings":    summary.Warnings,
		"connections": s.singboxTraffic(),
		"stats":       statsJSON(s.singbox.StatsStatus()),
		"core":        coreJSON(s.singbox.CoreLocalStatus()),
		"drift":       driftJSON(s.singbox.PortDrift()),
	}
}

func nodeJSON(node singbox.Node) map[string]interface{} {
	users := make([]map[string]interface{}, 0, len(node.Users))
	for _, user := range node.Users {
		users = append(users, map[string]interface{}{
			"name":     user.Name,
			"uuid":     user.UUID,
			"username": user.Username,
			"flow":     user.Flow,
		})
	}
	entry := map[string]interface{}{
		"id":        node.ID,
		"file":      node.File,
		"name":      node.Name(),
		"tag":       node.Tag,
		"type":      node.Type,
		"listen":    node.Listen,
		"port":      node.Port,
		"enabled":   node.Enabled,
		"transport": node.Transport,
		"tls_type":  node.TLSType,
		"users":     users,
		"error":     node.Error,
	}
	if !node.Modified.IsZero() {
		entry["modified"] = node.Modified.UTC().Format("2006-01-02T15:04:05Z")
	}
	if node.TLS != nil {
		entry["tls"] = map[string]interface{}{
			"enabled":     node.TLS.Enabled,
			"server_name": node.TLS.ServerName,
			"reality":     node.TLS.Reality,
			"public_key":  node.TLS.PublicKey,
			"short_id":    node.TLS.ShortID,
			"insecure":    node.TLS.Insecure,
		}
	}
	return entry
}

func checkJSON(check singbox.Check) map[string]interface{} {
	out := map[string]interface{}{
		"ok":       check.OK,
		"output":   check.Output,
		"duration": check.Duration.Milliseconds(),
	}
	if !check.Checked.IsZero() {
		out["checked_at"] = check.Checked.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

// statsJSON reports whether the Clash API the panel reads live counters from is
// configured, so the view can offer the one-click switch instead of asking the
// operator to edit the main configuration by hand.
func statsJSON(status singbox.StatsStatus) map[string]interface{} {
	return map[string]interface{}{
		"enabled":    status.Enabled,
		"controller": status.Controller,
		"secret":     status.Secret,
	}
}

// singboxTraffic returns live connection counters or an unavailable block.
func (s *Server) singboxTraffic() map[string]interface{} {
	connections, traffic, _ := s.singbox.Connections()
	return map[string]interface{}{
		"available":      traffic.Available,
		"upload_total":   traffic.UploadTotal,
		"download_total": traffic.DownloadTotal,
		"connections":    traffic.Connections,
		"error":          traffic.Error,
		"items":          connections,
	}
}

func (s *Server) handleSingbox(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.singboxPayload())
}

type singboxCreateBody struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Port   int    `json:"port"`
	Server string `json:"server"`
	Method string `json:"method"`
}

func (s *Server) handleSingboxCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body singboxCreateBody
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	node, check, err := s.singbox.Create(singbox.CreateRequest{
		Type:   body.Type,
		Name:   body.Name,
		Port:   body.Port,
		Server: body.Server,
		Method: body.Method,
	})
	if err != nil {
		s.audit("singbox.create", body.Type+" "+body.Name, "fail", auth.ClientIP(r))
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entry := nodeJSON(node)
	if _, links, linkErr := s.singbox.ShareLinks(node.ID, ""); linkErr == nil {
		entry["links"] = links
	}
	s.audit("singbox.create", node.ID, "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "node": entry, "check": checkJSON(check)})
}

type singboxActionBody struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

func (s *Server) handleSingboxAction(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body singboxActionBody
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := strings.TrimSpace(body.ID)
	action := strings.TrimSpace(body.Action)
	ip := auth.ClientIP(r)
	switch action {
	case "enable", "disable":
		node, err := s.singbox.SetEnabled(id, action == "enable")
		if err != nil {
			s.audit("singbox."+action, id, "fail", ip)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.audit("singbox."+action, id, "ok", ip)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "node": nodeJSON(node)})
	case "reset":
		node, err := s.singbox.Reset(id)
		if err != nil {
			s.audit("singbox.reset", id, "fail", ip)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		entry := nodeJSON(node)
		if _, links, linkErr := s.singbox.ShareLinks(node.ID, ""); linkErr == nil {
			entry["links"] = links
		}
		s.audit("singbox.reset", id, "ok", ip)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "node": entry})
	case "delete":
		if err := s.singbox.Delete(id); err != nil {
			s.audit("singbox.delete", id, "fail", ip)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.audit("singbox.delete", id, "ok", ip)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	case "restart":
		if err := s.singbox.Restart(); err != nil {
			s.audit("singbox.restart", "", "fail", ip)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.audit("singbox.restart", "", "ok", ip)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	case "check":
		check := s.singbox.Check()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": check.OK, "check": checkJSON(check)})
	case "stats-enable", "stats-disable":
		var status singbox.StatsStatus
		var err error
		if action == "stats-enable" {
			status, err = s.singbox.EnableStats("")
		} else {
			status, err = s.singbox.DisableStats()
		}
		if err != nil {
			s.audit("singbox."+action, "", "fail", ip)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.audit("singbox."+action, "", "ok", ip)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "stats": statsJSON(status)})
	default:
		writeError(w, http.StatusBadRequest, "不支持的操作："+action)
	}
}

func (s *Server) handleSingboxConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	switch r.Method {
	case http.MethodGet:
		if id == "" {
			writeError(w, http.StatusBadRequest, "缺少节点标识")
			return
		}
		text, err := s.singbox.ConfigText(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "text": text})
	case http.MethodPost:
		var body struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		check, err := s.singbox.SaveConfig(body.ID, body.Text)
		if err != nil {
			s.audit("singbox.config", body.ID, "fail", auth.ClientIP(r))
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error(), "check": checkJSON(check)})
			return
		}
		s.audit("singbox.config", body.ID, "ok", auth.ClientIP(r))
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "check": checkJSON(check)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSingboxQR renders one share link as a PNG QR code.
func (s *Server) handleSingboxQR(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	size := 320
	if v := r.URL.Query().Get("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			size = n
		}
	}
	userIndex := 0
	if v := r.URL.Query().Get("user"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			userIndex = n
		}
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	_, links, err := s.singbox.ShareLinks(id, host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	uri := ""
	for _, link := range links {
		if link.User == userIndex && link.URI != "" {
			uri = link.URI
			break
		}
	}
	if uri == "" && len(links) > 0 {
		uri = links[0].URI
	}
	if uri == "" {
		http.Error(w, "no share link", http.StatusNotFound)
		return
	}
	png, err := singbox.QRCodePNG(uri, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) handleSingboxConnections(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"as_of": config.NowUTC(), "data": s.singboxTraffic()})
}

// handleSingboxLog returns the tail of the sing-box unit journal.
func (s *Server) handleSingboxLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lines := 120
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			lines = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"as_of": config.NowUTC(), "lines": s.singboxLog(lines)})
}

// singboxLog returns the tail of the sing-box journal.
func (s *Server) singboxLog(lines int) []string {
	if s.singbox == nil {
		return []string{"sing-box 管理未启用"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "journalctl", "-u", s.singbox.Service(), "-n", strconv.Itoa(lines), "--no-pager", "-o", "cat").CombinedOutput()
	if err != nil {
		return []string{strings.TrimSpace(string(out))}
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return []string{}
	}
	return strings.Split(text, "\n")
}

// singboxDisabled writes a clear message when the feature is switched off.
func (s *Server) singboxDisabled(w http.ResponseWriter) bool {
	if s.singbox == nil {
		writeError(w, http.StatusServiceUnavailable, "sing-box 管理未启用，请先在配置中打开 singbox.enabled")
		return true
	}
	return false
}

// driftJSON exposes the configured/process port comparison to the node view.
func driftJSON(drift singbox.PortDrift) map[string]interface{} {
	return map[string]interface{}{
		"supported":  drift.Supported,
		"running":    drift.Running,
		"configured": drift.Configured,
		"listening":  drift.Listening,
		"missing":    drift.Missing,
		"extra":      drift.Extra,
		"error":      drift.Error,
	}
}

// singboxDiagState feeds the diagnostics report with the live node state.
func (s *Server) singboxDiagState() diagnostics.SingBoxState {
	if s.singbox == nil {
		return diagnostics.SingBoxState{Installed: false}
	}
	summary := s.singbox.Summary()
	state := diagnostics.SingBoxState{
		Installed:      summary.Installed,
		Version:        summary.Version,
		Binary:         summary.Binary,
		Service:        summary.Service,
		Running:        summary.Running,
		State:          summary.State,
		NodeCount:      len(summary.Nodes),
		CheckOK:        summary.Check.OK,
		CheckOutput:    summary.Check.Output,
		CheckSupported: summary.Installed,
	}
	for _, node := range summary.Nodes {
		if node.Enabled {
			state.EnabledNodes++
		}
	}
	drift := s.singbox.PortDrift()
	if drift.Supported {
		state.Configured = drift.Configured
		state.Listening = drift.Listening
	} else {
		state.Error = drift.Error
	}
	return state
}
