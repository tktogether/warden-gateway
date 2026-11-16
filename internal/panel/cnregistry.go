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
