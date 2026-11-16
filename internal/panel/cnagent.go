package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/cnagent"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/version"
)

// cnBeatMinInterval is the minimum gap between two heartbeats of the same
// machine; faster repeats get 429. The agent beats every 10s (±20% jitter),
// so 8s leaves headroom for probe time and clock skew.
const cnBeatMinInterval = 8 * time.Second

// cnMaxDispatchCommands caps how many queued commands an agent receives per
// heartbeat so a burst never inflates one reply beyond control.
const cnMaxDispatchCommands = 5

// cnCmdSelfUpgrade is the command action that carries an agent upgrade; when
// one is dispatched the reply names the gateway version as target.
const cnCmdSelfUpgrade = "self-upgrade"

// cnCmdDeregister is the command that tells an agent this gateway no longer
// manages it; when it is dispatched (the agent's ack beat) the machine is
// removed from the registry and its token stops working.
const cnCmdDeregister = "deregister"

// cnAgentBodyMax bounds enroll and heartbeat request bodies.
const cnAgentBodyMax = 1 << 20

// hashDomains derives the split-domain list hash the gateway compares and
// pushes: SHA-256 over the newline-joined list (nil treated as empty, so the
// empty hash stays deterministic). Agents adopt the pushed hash verbatim,
// which is what makes the exchange converge after one push.
func hashDomains(list []string) string {
	sum := sha256.Sum256([]byte(strings.Join(list, "\n")))
	return hex.EncodeToString(sum[:])
}

// bearerToken extracts the machine token from the Authorization header.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// readAgentJSON decodes an agent request body bounded by cnAgentBodyMax.
// Unknown fields are kept: previous protocol versions may send fields this
// gateway does not know (spec §9.6 accepts the previous protocol).
func readAgentJSON(w http.ResponseWriter, r *http.Request, v interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, cnAgentBodyMax)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// allowBeat reports whether a beat from the given token hash may proceed.
// It is called before the registry is consulted; recordBeat stores the time
// only after the beat has been handled successfully, so a failed request can
// be retried immediately.
func (s *Server) allowBeat(tokenHash string) bool {
	s.beatMu.Lock()
	defer s.beatMu.Unlock()
	last, ok := s.lastBeat[tokenHash]
	return !ok || time.Since(last) >= s.minBeatInterval
}

func (s *Server) recordBeat(tokenHash string) {
	s.beatMu.Lock()
	defer s.beatMu.Unlock()
	if s.lastBeat == nil {
		s.lastBeat = make(map[string]time.Time)
	}
	s.lastBeat[tokenHash] = time.Now()
}

// forgetBeat drops the rate-limit record of a machine token so removed
// machines leave no stale limiter state behind.
func (s *Server) forgetBeat(tokenHash string) {
	if tokenHash == "" {
		return
	}
	s.beatMu.Lock()
	delete(s.lastBeat, tokenHash)
	s.beatMu.Unlock()
}

type cnEnrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	Hostname    string `json:"hostname"`
}

// handleAgentEnroll exchanges a one-time enroll token for machine
// credentials. Agents hold no panel session, so this endpoint performs no
// CSRF/session checks; the short-lived single-use token is the credential.
func (s *Server) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cnReg == nil {
		writeError(w, http.StatusInternalServerError, "agent registry unavailable")
		return
	}
	var body cnEnrollRequest
	if err := readAgentJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ip := auth.ClientIP(r)
	m, token, err := s.cnReg.Enroll(body.EnrollToken, body.Hostname, time.Now())
	if errors.Is(err, ErrInvalidEnroll) {
		// The raw token never reaches the audit log, only the outcome.
		s.audit("cn.enroll", "invalid enroll token", "fail", ip)
		writeError(w, http.StatusForbidden, "invalid or expired enroll token")
		return
	}
	if err != nil {
		s.audit("cn.enroll", body.Hostname, "fail", ip)
		writeError(w, http.StatusInternalServerError, "enroll failed")
		return
	}
	s.audit("cn.enroll", body.Hostname, "ok", ip)
	writeJSON(w, http.StatusOK, map[string]interface{}{"machine_id": m.ID, "token": token})
}

// handleAgentHeartbeat authenticates a CN machine by its Bearer token,
// records the report, hands out queued commands and pushes the split-domain
// list when the agent's hash is stale. No session/CSRF: the machine token is
// the credential.
func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cnReg == nil {
		writeError(w, http.StatusInternalServerError, "agent registry unavailable")
		return
	}
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	tokenHash := config.HashAccessKey(token)
	if !s.allowBeat(tokenHash) {
		writeError(w, http.StatusTooManyRequests, "heartbeat too frequent")
		return
	}
	machine, err := s.cnReg.Beat(tokenHash, time.Now())
	if err != nil {
		if errors.Is(err, ErrUnknownMachine) {
			writeError(w, http.StatusUnauthorized, "unknown machine token")
			return
		}
		writeError(w, http.StatusInternalServerError, "heartbeat failed")
		return
	}

	var report cnagent.Report
	if err := readAgentJSON(w, r, &report); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Spec §9.6: accept the current and the previous protocol version;
	// anything else is a client bug worth a 400.
	if report.Protocol != 0 && report.Protocol != cnagent.ProtocolVersion {
		writeError(w, http.StatusBadRequest, "unsupported protocol version")
		return
	}

	// machine is a live registry pointer: read-only here (Task 9 review).
	// Mutations go through registry methods.
	reportMap := map[string]interface{}{}
	if raw, err := json.Marshal(&report); err == nil {
		_ = json.Unmarshal(raw, &reportMap)
	}
	desired := ""
	if machine.AutoFollow {
		desired = version.Version
	}
	if err := s.cnReg.UpdateReport(machine.ID, reportMap, desired); err != nil {
		writeError(w, http.StatusInternalServerError, "persist report failed")
		return
	}

	dispatched := s.cnReg.PopCommands(machine.ID, cnMaxDispatchCommands)
	commands := make([]cnagent.Command, len(dispatched))
	targetVersion := desired
	deregistered := false
	for i, cmd := range dispatched {
		commands[i] = cnagent.Command{ID: cmd.ID, Action: cmd.Action, Params: cmd.Params}
		if cmd.Action == cnCmdSelfUpgrade {
			targetVersion = version.Version
		}
		if cmd.Action == cnCmdDeregister {
			deregistered = true
		}
	}

	reply := cnagent.HeartbeatReply{
		Commands:      commands,
		TargetVersion: targetVersion,
	}
	currentHash := hashDomains(s.cfg.DNS.Domains)
	if report.DomainsHash != currentHash {
		reply.Domains = &cnagent.DomainPayload{Hash: currentHash, Domains: append([]string(nil), s.cfg.DNS.Domains...)}
	}
	s.recordBeat(tokenHash)
	if deregistered {
		// The agent picked up the deregister command: revoke the machine now
		// so its token stops beating. The pop itself was already persisted, so
		// if this Remove fails the soft removal is silently dropped until the
		// operator re-queues it — the audit line below records the failure.
		if err := s.cnReg.Remove(machine.ID); err == nil {
			s.forgetBeat(tokenHash)
		} else {
			s.audit("cn.remove", machine.ID, "fail: "+err.Error(), auth.ClientIP(r))
		}
	}
	writeJSON(w, http.StatusOK, &reply)
}

// cnOnlineWindow is how fresh a machine's last heartbeat must be for the
// management list to call it online.
const cnOnlineWindow = 30 * time.Second

// cnListEventLimit caps the events echoed per machine in the list reply.
const cnListEventLimit = 20

// cnMachineActions is the allowlist of commands the panel may queue for a
// machine; anything else is rejected before touching the registry.
var cnMachineActions = map[string]bool{"sync-domains": true, "self-check": true, "self-upgrade": true}

// cnMachineRow is one machine entry of the GET /api/cnmachines reply.
type cnMachineRow struct {
	ID             string                 `json:"id"`
	Hostname       string                 `json:"hostname"`
	Online         bool                   `json:"online"`
	LastSeen       string                 `json:"last_seen"`
	EnrolledAt     string                 `json:"enrolled_at"`
	Version        string                 `json:"version"`
	DesiredVersion string                 `json:"desired_version"`
	AutoFollow     bool                   `json:"auto_follow"`
	Probe          map[string]interface{} `json:"probe"`
	System         map[string]interface{} `json:"system"`
	Workloads      interface{}            `json:"workloads"`
	Events         []map[string]string    `json:"events"`
	Pending        int                    `json:"pending"`
}

// newCNMachineRow builds a list row from a machine value copy (see
// CNRegistry.Snapshot): nothing here shares memory with the registry, so the
// read cannot race the heartbeat path and the report maps pass through
// verbatim from the copy.
func newCNMachineRow(m CNMachine, now time.Time) cnMachineRow {
	online := false
	if seen, err := time.Parse(time.RFC3339, m.LastSeen); err == nil {
		online = now.Sub(seen) < cnOnlineWindow
	}
	reportVersion := ""
	if v, ok := m.Report["agent_version"].(string); ok {
		reportVersion = v
	}
	probe, _ := m.Report["probe"].(map[string]interface{})
	system, _ := m.Report["system"].(map[string]interface{})
	events := make([]map[string]string, 0, len(m.Events))
	for i, ev := range m.Events {
		if i >= cnListEventLimit {
			break
		}
		events = append(events, map[string]string{"at": ev.At, "kind": ev.Kind, "detail": ev.Detail})
	}
	return cnMachineRow{
		ID: m.ID, Hostname: m.Hostname,
		Online: online, LastSeen: m.LastSeen, EnrolledAt: m.EnrolledAt,
		Version: reportVersion, DesiredVersion: m.DesiredVersion, AutoFollow: m.AutoFollow,
		Probe: probe, System: system, Workloads: m.Report["workloads"],
		Events: events, Pending: len(m.Pending),
	}
}

// handleCNMachines serves the session-guarded management list of CN machines.
func (s *Server) handleCNMachines(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cnReg == nil {
		writeError(w, http.StatusInternalServerError, "agent registry unavailable")
		return
	}
	now := time.Now()
	machines := s.cnReg.Snapshot()
	rows := make([]cnMachineRow, 0, len(machines))
	for _, m := range machines {
		rows = append(rows, newCNMachineRow(m, now))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"as_of": config.NowUTC(), "machines": rows})
}

// handleCNMachineItem dispatches /api/cnmachines/… sub-paths: the enroll-token
// mint and the per-machine command, auto-follow and remove actions. Machine
// IDs are hex without slashes, so a plain split is safe.
func (s *Server) handleCNMachineItem(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 1 && parts[0] == "enroll-token" {
		s.handleCNEnrollToken(w, r)
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid machine id")
		return
	}
	switch parts[1] {
	case "command":
		s.handleCNMachineCommand(w, r, id)
	case "auto-follow":
		s.handleCNMachineAutoFollow(w, r, id)
	case "remove":
		s.handleCNMachineRemove(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// handleCNEnrollToken mints a one-time enroll token for the panel user. Only
// the token's hash is stored; the raw value is shown once.
func (s *Server) handleCNEnrollToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cnReg == nil {
		writeError(w, http.StatusInternalServerError, "agent registry unavailable")
		return
	}
	now := time.Now()
	raw, err := s.cnReg.NewEnrollToken(now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mint enroll token failed")
		return
	}
	s.audit("cn.enroll_token", "", "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":      raw,
		"expires_at": now.UTC().Add(cnEnrollTokenTTL).Format(time.RFC3339),
	})
}

// handleCNMachineCommand queues an allowlisted command for one machine.
func (s *Server) handleCNMachineCommand(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !cnMachineActions[body.Action] {
		writeError(w, http.StatusBadRequest, "action must be sync-domains, self-check or self-upgrade")
		return
	}
	params := map[string]string{}
	if body.Action == cnCmdSelfUpgrade {
		// The gateway pins the upgrade to its own version.
		params["version"] = version.Version
	}
	if err := s.cnReg.QueueCommand(id, body.Action, params); err != nil {
		if errors.Is(err, ErrUnknownMachine) {
			writeError(w, http.StatusNotFound, "unknown machine")
			return
		}
		s.audit("cn.command", id+" "+body.Action, "fail", auth.ClientIP(r))
		writeError(w, http.StatusInternalServerError, "queue command failed")
		return
	}
	s.audit("cn.command", id+" "+body.Action, "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "queued": true, "action": body.Action})
}

// handleCNMachineAutoFollow toggles a machine's auto-follow flag.
func (s *Server) handleCNMachineAutoFollow(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.cnReg.SetAutoFollow(id, body.Enabled); err != nil {
		if errors.Is(err, ErrUnknownMachine) {
			writeError(w, http.StatusNotFound, "unknown machine")
			return
		}
		s.audit("cn.auto_follow", id, "fail", auth.ClientIP(r))
		writeError(w, http.StatusInternalServerError, "update auto-follow failed")
		return
	}
	state := "disabled"
	if body.Enabled {
		state = "enabled"
	}
	s.audit("cn.auto_follow", id+" "+state, "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "auto_follow": body.Enabled})
}

// handleCNMachineRemove revokes a machine outright (force) or queues a
// deregister command whose dispatch (the agent's ack beat) removes it.
func (s *Server) handleCNMachineRemove(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Force {
		tokenHash := ""
		if m := s.cnReg.MachineByID(id); m != nil {
			tokenHash = m.TokenHash
		}
		if err := s.cnReg.Remove(id); err != nil {
			if errors.Is(err, ErrUnknownMachine) {
				writeError(w, http.StatusNotFound, "unknown machine")
				return
			}
			s.audit("cn.remove", id+" force", "fail", auth.ClientIP(r))
			writeError(w, http.StatusInternalServerError, "remove machine failed")
			return
		}
		s.forgetBeat(tokenHash)
		s.audit("cn.remove", id+" force", "ok", auth.ClientIP(r))
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "removed": true})
		return
	}
	if err := s.cnReg.QueueCommand(id, cnCmdDeregister, nil); err != nil {
		if errors.Is(err, ErrUnknownMachine) {
			writeError(w, http.StatusNotFound, "unknown machine")
			return
		}
		s.audit("cn.remove", id+" deregister queued", "fail", auth.ClientIP(r))
		writeError(w, http.StatusInternalServerError, "queue deregister failed")
		return
	}
	s.audit("cn.remove", id+" deregister queued", "ok", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "queued": true})
}
