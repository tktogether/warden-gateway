package panel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
)

const (
	cnAgentsFileName = "cn-agents.json"
	cnAgentsFileMode = 0o600
	cnEventCap       = 50
	cnEnrollTokenTTL = 15 * time.Minute
)

var (
	ErrUnknownMachine = errors.New("unknown cn agent machine")
	ErrInvalidEnroll  = errors.New("invalid or expired cn agent enroll token")
)

// CNMachine is a CN agent registered with this gateway.
type CNMachine struct {
	ID, Hostname, TokenHash string
	EnrolledAt, LastSeen    string // RFC3339 UTC
	Report                  map[string]interface{}
	DesiredVersion          string
	AutoFollow              bool
	Deregistered            bool
	Pending                 []CNCommand
	Events                  []CNEvent // newest first, cap 50
}

// CNCommand is one operation queued for a CN agent.
type CNCommand struct {
	ID       string
	Action   string
	QueuedAt string
	Params   map[string]string
}

// CNEvent records something that happened to a machine.
type CNEvent struct {
	At     string
	Kind   string
	Detail string
}

// CNEnrollToken is a one-time enrollment credential stored by hash only.
type CNEnrollToken struct {
	TokenHash string
	ExpiresAt string
	Used      bool
}

// cnRegistryFile is the on-disk layout of cn-agents.json.
type cnRegistryFile struct {
	Machines     []*CNMachine     `json:"machines"`
	EnrollTokens []*CNEnrollToken `json:"enroll_tokens"`
}

// CNRegistry persists CN agent machines and enroll tokens in
// <dataDir>/cn-agents.json so registrations survive gateway restarts.
type CNRegistry struct {
	mu       sync.Mutex
	path     string
	machines []*CNMachine
	tokens   []*CNEnrollToken
}

// LoadCNRegistry loads the registry from dataDir. A missing file yields an
// empty registry.
func LoadCNRegistry(dataDir string) (*CNRegistry, error) {
	path := filepath.Join(dataDir, cnAgentsFileName)
	r := &CNRegistry{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return r, nil
		}
		return nil, fmt.Errorf("read cn agent registry: %w", err)
	}
	if len(data) == 0 {
		return r, nil
	}
	var f cnRegistryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse cn agent registry: %w", err)
	}
	r.machines = f.Machines
	r.tokens = f.EnrollTokens
	return r, nil
}

// Save writes the registry to disk (0600, temp file + rename).
func (r *CNRegistry) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saveLocked()
}

func (r *CNRegistry) saveLocked() error {
	data, err := json.MarshalIndent(cnRegistryFile{
		Machines:     r.machines,
		EnrollTokens: r.tokens,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cn agent registry: %w", err)
	}
	if err := cnWriteFileAtomic(r.path, data, cnAgentsFileMode); err != nil {
		return fmt.Errorf("write cn agent registry: %w", err)
	}
	return nil
}

// NewEnrollToken mints a one-time enroll token valid for 15 minutes. Only its
// hash is stored; the raw value is returned once for the panel user.
func (r *CNRegistry) NewEnrollToken(now time.Time) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := cnRandomHex(32)
	if err != nil {
		return "", err
	}
	r.tokens = append(r.tokens, &CNEnrollToken{
		TokenHash: config.HashAccessKey(raw),
		ExpiresAt: now.UTC().Add(cnEnrollTokenTTL).Format(time.RFC3339),
	})
	if err := r.saveLocked(); err != nil {
		return "", err
	}
	return raw, nil
}

// Enroll exchanges a valid, unused, unexpired raw enroll token for a new
// machine and machine token. The token is marked used; only the machine
// token's hash is stored. Raw tokens are returned to the caller once.
func (r *CNRegistry) Enroll(rawEnrollToken, hostname string, now time.Time) (*CNMachine, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	hash := config.HashAccessKey(rawEnrollToken)
	token := r.tokenByHashLocked(hash)
	if token == nil || token.Used || cnExpired(token.ExpiresAt, now) {
		return nil, "", ErrInvalidEnroll
	}
	rawMachineToken, err := cnRandomHex(32)
	if err != nil {
		return nil, "", err
	}
	id, err := r.newMachineIDLocked()
	if err != nil {
		return nil, "", err
	}
	token.Used = true
	nowStr := now.UTC().Format(time.RFC3339)
	m := &CNMachine{
		ID:         id,
		Hostname:   hostname,
		TokenHash:  config.HashAccessKey(rawMachineToken),
		EnrolledAt: nowStr,
		LastSeen:   nowStr,
	}
	r.machines = append(r.machines, m)
	r.appendEventLocked(m, "enrolled", "hostname "+hostname, now)
	if err := r.saveLocked(); err != nil {
		return nil, "", err
	}
	return m, rawMachineToken, nil
}

// Beat refreshes a machine's LastSeen using its machine token hash.
func (r *CNRegistry) Beat(tokenHash string, now time.Time) (*CNMachine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.machineByTokenHashLocked(tokenHash)
	if m == nil {
		return nil, ErrUnknownMachine
	}
	m.LastSeen = now.UTC().Format(time.RFC3339)
	return m, nil
}

// QueueCommand appends a command to a machine's pending queue. id is the
// machine ID; the command ID is generated here.
func (r *CNRegistry) QueueCommand(id, action string, params map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.machineByIDLocked(id)
	if m == nil {
		return ErrUnknownMachine
	}
	cmdID, err := cnRandomHex(8)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	m.Pending = append(m.Pending, CNCommand{
		ID:       cmdID,
		Action:   action,
		QueuedAt: now.Format(time.RFC3339),
		Params:   params,
	})
	r.appendEventLocked(m, "command_queued", "action "+action, now)
	if err := r.saveLocked(); err != nil {
		return err
	}
	return nil
}

// PopCommands returns up to max commands from the machine's pending queue and
// clears them, appending a "dispatched" event per command and persisting the
// change. Unknown machines and empty queues yield an empty slice. If the save
// fails the in-memory queue is restored so the commands are handed out again
// on the next beat instead of being lost or silently re-dispatched after a
// restart.
func (r *CNRegistry) PopCommands(id string, max int) []CNCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.machineByIDLocked(id)
	if m == nil || len(m.Pending) == 0 {
		return []CNCommand{}
	}
	n := len(m.Pending)
	if max > 0 && max < n {
		n = max
	}
	out := make([]CNCommand, n)
	rest := make([]CNCommand, len(m.Pending)-n)
	copy(out, m.Pending[:n])
	copy(rest, m.Pending[n:])
	for i := range out {
		if out[i].Params != nil {
			out[i].Params = cnCopyParams(out[i].Params)
		}
	}
	now := time.Now().UTC()
	for _, cmd := range out {
		r.appendEventLocked(m, "dispatched", "action "+cmd.Action, now)
	}
	saved := m.Pending
	m.Pending = rest
	if err := r.saveLocked(); err != nil {
		m.Pending = saved
		m.Events = m.Events[len(out):]
		return []CNCommand{}
	}
	return out
}

// UpdateReport stores the machine's latest heartbeat report and desired agent
// version, then persists both.
func (r *CNRegistry) UpdateReport(id string, report map[string]interface{}, desiredVersion string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.machineByIDLocked(id)
	if m == nil {
		return ErrUnknownMachine
	}
	m.Report = report
	// AutoFollow keeps the machine trailing the gateway's own version; without
	// it DesiredVersion is whatever the operator pinned (an older build for a
	// rollback, or empty to leave the machine alone). A routine heartbeat never
	// clears an explicit pin.
	if desiredVersion != "" {
		m.DesiredVersion = desiredVersion
	}
	return r.saveLocked()
}

// SetDesiredVersion pins the agent build a machine should run. An empty version
// clears the pin: the machine keeps whatever it has unless AutoFollow is on.
func (r *CNRegistry) SetDesiredVersion(machineID, ver string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.machineByIDLocked(machineID)
	if m == nil {
		return ErrUnknownMachine
	}
	m.DesiredVersion = ver
	detail := ver
	if detail == "" {
		detail = "cleared"
	}
	r.appendEventLocked(m, "desired_version", detail, time.Now().UTC())
	return r.saveLocked()
}

// SetAutoFollow toggles a machine's auto-follow flag and persists the change.
// An auto-following machine adopts the gateway version as its desired agent
// version on the next heartbeat.
func (r *CNRegistry) SetAutoFollow(machineID string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.machineByIDLocked(machineID)
	if m == nil {
		return ErrUnknownMachine
	}
	m.AutoFollow = enabled
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	r.appendEventLocked(m, "auto_follow", state, time.Now().UTC())
	return r.saveLocked()
}

// MachineByID returns the machine with the given ID, or nil.
func (r *CNRegistry) MachineByID(id string) *CNMachine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.machineByIDLocked(id)
}

// Machines returns a snapshot of all registered machines.
func (r *CNRegistry) Machines() []*CNMachine {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*CNMachine, len(r.machines))
	copy(out, r.machines)
	return out
}

// Snapshot returns value copies of every registered machine so callers can
// render or filter them without touching memory the heartbeat path mutates
// under the registry lock. Machines() keeps returning live pointers for
// internal read-only uses; anything handed to handlers must come from here.
func (r *CNRegistry) Snapshot() []CNMachine {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CNMachine, 0, len(r.machines))
	for _, m := range r.machines {
		out = append(out, cnCopyMachine(m))
	}
	return out
}

// cnCopyMachine deep-copies one machine record: the report map including its
// nested probe/system objects and the workloads slice, the event slice and
// the pending commands with their params maps.
func cnCopyMachine(m *CNMachine) CNMachine {
	c := *m
	if m.Report != nil {
		c.Report = make(map[string]interface{}, len(m.Report))
		for k, v := range m.Report {
			switch val := v.(type) {
			case map[string]interface{}:
				c.Report[k] = cnCopyMap(val)
			case []interface{}:
				c.Report[k] = cnCopySliceOfMaps(val)
			default:
				c.Report[k] = v
			}
		}
	}
	if m.Events != nil {
		c.Events = make([]CNEvent, len(m.Events))
		copy(c.Events, m.Events)
	}
	if m.Pending != nil {
		c.Pending = make([]CNCommand, len(m.Pending))
		for i, cmd := range m.Pending {
			c.Pending[i] = cmd
			if cmd.Params != nil {
				c.Pending[i].Params = cnCopyParams(cmd.Params)
			}
		}
	}
	return c
}

// cnCopyMap clones one level of a report object; the leaves the agent reports
// are JSON scalars, which are safe to share by value.
func cnCopyMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// cnCopySliceOfMaps clones a workloads-style slice and each element map.
func cnCopySliceOfMaps(in []interface{}) []interface{} {
	out := make([]interface{}, len(in))
	for i, v := range in {
		if m, ok := v.(map[string]interface{}); ok {
			out[i] = cnCopyMap(m)
		} else {
			out[i] = v
		}
	}
	return out
}

// cnCopyParams clones a command params map.
func cnCopyParams(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Remove deletes a machine and its pending queue.
func (r *CNRegistry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := -1
	for i, m := range r.machines {
		if m.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrUnknownMachine
	}
	r.machines = append(r.machines[:idx], r.machines[idx+1:]...)
	if err := r.saveLocked(); err != nil {
		return err
	}
	return nil
}

func (r *CNRegistry) machineByIDLocked(id string) *CNMachine {
	for _, m := range r.machines {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (r *CNRegistry) machineByTokenHashLocked(tokenHash string) *CNMachine {
	for _, m := range r.machines {
		if m.TokenHash == tokenHash {
			return m
		}
	}
	return nil
}

func (r *CNRegistry) tokenByHashLocked(hash string) *CNEnrollToken {
	for _, t := range r.tokens {
		if t.TokenHash == hash {
			return t
		}
	}
	return nil
}

func (r *CNRegistry) newMachineIDLocked() (string, error) {
	for {
		id, err := cnRandomHex(8)
		if err != nil {
			return "", err
		}
		if r.machineByIDLocked(id) == nil {
			return id, nil
		}
	}
}

func (r *CNRegistry) appendEventLocked(m *CNMachine, kind, detail string, now time.Time) {
	ev := CNEvent{At: now.UTC().Format(time.RFC3339), Kind: kind, Detail: detail}
	m.Events = append([]CNEvent{ev}, m.Events...)
	if len(m.Events) > cnEventCap {
		m.Events = m.Events[:cnEventCap]
	}
}

func cnExpired(expiresAt string, now time.Time) bool {
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return true
	}
	return !exp.After(now)
}

// cnRandomHex returns the hex encoding of n random bytes (2n hex chars).
func cnRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// cnWriteFileAtomic writes data via a temp file + rename so a crash never
// leaves a half-written registry behind.
func cnWriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
