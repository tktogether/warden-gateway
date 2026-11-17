package panel

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
)

var cnTestNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func newCNRegistry(t *testing.T) (*CNRegistry, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := LoadCNRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func requireHex64(t *testing.T, name, value string) {
	t.Helper()
	if len(value) != 64 {
		t.Fatalf("%s: want 64 chars, got %d", name, len(value))
	}
	if _, err := hex.DecodeString(value); err != nil {
		t.Fatalf("%s: not hex: %v", name, err)
	}
}

func TestEnrollTokenSingleUse(t *testing.T) {
	r, _ := newCNRegistry(t)

	raw, err := r.NewEnrollToken(cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	requireHex64(t, "enroll token", raw)

	m, rawToken, err := r.Enroll(raw, "host-a", cnTestNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	requireHex64(t, "machine token", rawToken)
	if m == nil {
		t.Fatal("first enroll: nil machine")
	}
	if m.Hostname != "host-a" {
		t.Errorf("hostname = %q, want host-a", m.Hostname)
	}
	if m.TokenHash != config.HashAccessKey(rawToken) {
		t.Error("machine TokenHash is not the hash of the raw machine token")
	}

	// Second enroll with the same raw token must be rejected.
	_, _, err = r.Enroll(raw, "host-b", cnTestNow.Add(2*time.Minute))
	if !errors.Is(err, ErrInvalidEnroll) {
		t.Fatalf("second enroll err = %v, want ErrInvalidEnroll", err)
	}
}

func TestExpiredEnrollTokenRejected(t *testing.T) {
	r, _ := newCNRegistry(t)

	raw, err := r.NewEnrollToken(cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	late := cnTestNow.Add(cnEnrollTokenTTL + time.Minute)
	if _, _, err := r.Enroll(raw, "late-host", late); !errors.Is(err, ErrInvalidEnroll) {
		t.Fatalf("expired enroll err = %v, want ErrInvalidEnroll", err)
	}

	if _, _, err := r.Enroll("deadbeef", "host-c", cnTestNow); !errors.Is(err, ErrInvalidEnroll) {
		t.Fatalf("unknown token enroll err = %v, want ErrInvalidEnroll", err)
	}
	if len(r.Machines()) != 0 {
		t.Errorf("machines = %d, want 0 (no machine must be created)", len(r.Machines()))
	}
}

func TestRegistrySurvivesReload(t *testing.T) {
	r, dir := newCNRegistry(t)

	rawEnroll, err := r.NewEnrollToken(cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	m, rawToken, err := r.Enroll(rawEnroll, "edge-01", cnTestNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Beat(m.TokenHash, cnTestNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := r.QueueCommand(m.ID, "dns_set", map[string]string{"zone": "example.com"}); err != nil {
		t.Fatal(err)
	}

	// Simulate a gateway restart: fresh registry from the same data dir.
	r2, err := LoadCNRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	m2 := r2.MachineByID(m.ID)
	if m2 == nil {
		t.Fatal("machine lost after reload (Review Focus #1)")
	}
	if m2.Hostname != "edge-01" {
		t.Errorf("hostname = %q, want edge-01", m2.Hostname)
	}
	if m2.TokenHash != m.TokenHash || m2.TokenHash != config.HashAccessKey(rawToken) {
		t.Error("token hash not preserved across reload")
	}
	if m2.EnrolledAt != m.EnrolledAt {
		t.Errorf("EnrolledAt = %q, want %q", m2.EnrolledAt, m.EnrolledAt)
	}
	if m2.LastSeen != m.LastSeen {
		t.Errorf("LastSeen = %q, want %q", m2.LastSeen, m.LastSeen)
	}
	if len(m2.Pending) != 1 || m2.Pending[0].Action != "dns_set" || m2.Pending[0].Params["zone"] != "example.com" {
		t.Errorf("pending commands after reload = %+v, want queued dns_set", m2.Pending)
	}

	// The persisted machine token still authenticates after restart.
	if _, err := r2.Beat(m.TokenHash, cnTestNow.Add(3*time.Minute)); err != nil {
		t.Errorf("beat with persisted token: %v", err)
	}
	// The used enroll token stays used after reload.
	if _, _, err := r2.Enroll(rawEnroll, "edge-01", cnTestNow.Add(3*time.Minute)); !errors.Is(err, ErrInvalidEnroll) {
		t.Errorf("enroll token reusable after reload: err = %v, want ErrInvalidEnroll", err)
	}
}

func TestMachineTokenStoredHashed(t *testing.T) {
	r, dir := newCNRegistry(t)

	rawEnroll, err := r.NewEnrollToken(cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, rawToken, err := r.Enroll(rawEnroll, "edge-01", cnTestNow); err != nil {
		t.Fatal(err)
	} else {
		requireHex64(t, "machine token", rawToken)

		data, err := os.ReadFile(filepath.Join(dir, cnAgentsFileName))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if strings.Contains(content, rawToken) {
			t.Error("raw machine token leaked to cn-agents.json")
		}
		if strings.Contains(content, rawEnroll) {
			t.Error("raw enroll token leaked to cn-agents.json")
		}
		if !strings.Contains(content, config.HashAccessKey(rawToken)) {
			t.Error("machine token hash missing from cn-agents.json")
		}
		if !strings.Contains(content, config.HashAccessKey(rawEnroll)) {
			t.Error("enroll token hash missing from cn-agents.json")
		}

		info, err := os.Stat(filepath.Join(dir, cnAgentsFileName))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestBeatUnknownMachine(t *testing.T) {
	r, _ := newCNRegistry(t)
	if _, err := r.Beat(strings.Repeat("a", 64), cnTestNow); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("beat err = %v, want ErrUnknownMachine", err)
	}
}

func TestQueueCommandUnknownMachine(t *testing.T) {
	r, _ := newCNRegistry(t)
	if err := r.QueueCommand("no-such-machine", "dns_set", nil); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("queue err = %v, want ErrUnknownMachine", err)
	}
}

func TestRemoveUnknownMachine(t *testing.T) {
	r, _ := newCNRegistry(t)
	if err := r.Remove("no-such-machine"); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("remove err = %v, want ErrUnknownMachine", err)
	}
}

func TestRemoveAndLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	r, err := LoadCNRegistry(dir) // missing file = empty registry
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Machines()) != 0 {
		t.Fatalf("machines = %d, want 0", len(r.Machines()))
	}

	rawEnroll, err := r.NewEnrollToken(cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := r.Enroll(rawEnroll, "edge-02", cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.Machines()) != 0 {
		t.Fatalf("machines after remove = %d, want 0", len(r.Machines()))
	}
	r2, err := LoadCNRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Machines()) != 0 {
		t.Fatalf("machines after reload = %d, want 0", len(r2.Machines()))
	}
}

func TestEventCapAndOrder(t *testing.T) {
	r, _ := newCNRegistry(t)
	rawEnroll, err := r.NewEnrollToken(cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := r.Enroll(rawEnroll, "edge-01", cnTestNow)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{"dns_set", "dns_clear", "upgrade"}
	for i := 0; i < cnEventCap+10; i++ {
		if err := r.QueueCommand(m.ID, actions[i%len(actions)], nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.Events) != cnEventCap {
		t.Fatalf("events = %d, want cap %d", len(m.Events), cnEventCap)
	}
	// Newest first: the last queued command must be event 0.
	lastIdx := cnEventCap + 9
	if m.Events[0].Kind != "command_queued" || m.Events[0].Detail != "action "+actions[lastIdx%len(actions)] {
		t.Errorf("Events[0] = %+v, want newest command_queued with action %s", m.Events[0], actions[lastIdx%len(actions)])
	}
	if m.Events[1].Detail != "action "+actions[(lastIdx-1)%len(actions)] {
		t.Errorf("Events[1] = %+v, want previous command", m.Events[1])
	}
}

// TestEnrollSameHostReplaces pins the re-setup contract: enrolling the same
// hostname again replaces the old record instead of leaving a phantom offline
// machine (and its undeliverable queued commands) in the panel forever.
func TestEnrollSameHostReplaces(t *testing.T) {
	dir := t.TempDir()
	reg, err := LoadCNRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok1, err := reg.NewEnrollToken(now)
	if err != nil {
		t.Fatal(err)
	}
	first, tok1m, err := reg.Enroll(tok1, "cn-01", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.QueueCommand(first.ID, "self-check", nil); err != nil {
		t.Fatal(err)
	}

	tok2, err := reg.NewEnrollToken(now)
	if err != nil {
		t.Fatal(err)
	}
	second, tok2m, err := reg.Enroll(tok2, "cn-01", now)
	if err != nil {
		t.Fatal(err)
	}
	machines := reg.Machines()
	if len(machines) != 1 {
		t.Fatalf("machines = %d, want 1 (same host re-enrolled)", len(machines))
	}
	if machines[0].ID != second.ID {
		t.Fatalf("kept machine %s, want the new one %s", machines[0].ID, second.ID)
	}
	if len(machines[0].Pending) != 0 {
		t.Fatalf("old queued commands survived: %+v", machines[0].Pending)
	}
	// The replaced machine's token no longer authenticates.
	if _, err := reg.Beat(config.HashAccessKey(tok1m), now); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("old token Beat err = %v, want ErrUnknownMachine", err)
	}
	if _, err := reg.Beat(config.HashAccessKey(tok2m), now); err != nil {
		t.Fatalf("new token Beat err = %v", err)
	}

	// A different hostname is kept alongside it.
	tok3, err := reg.NewEnrollToken(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.Enroll(tok3, "cn-02", now); err != nil {
		t.Fatal(err)
	}
	if got := len(reg.Machines()); got != 2 {
		t.Fatalf("machines = %d, want 2 after a different host", got)
	}
}
