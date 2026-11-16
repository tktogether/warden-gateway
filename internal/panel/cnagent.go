package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	for i, cmd := range dispatched {
		commands[i] = cnagent.Command{ID: cmd.ID, Action: cmd.Action, Params: cmd.Params}
		if cmd.Action == cnCmdSelfUpgrade {
			targetVersion = version.Version
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
	writeJSON(w, http.StatusOK, &reply)
}
