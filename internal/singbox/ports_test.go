package singbox

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// ssSample is the output of "ss -H -lntu" on a host that runs sing-box nodes,
// the panel and unrelated services, including IPv6 and scoped addresses.
const ssSample = "udp UNCONN 0 0 208.75.133.177:53 0.0.0.0:*\n" +
	"udp UNCONN 0 0 127.0.0.53%lo:53 0.0.0.0:*\n" +
	"udp UNCONN 0 0 *:12538 *:*\n" +
	"udp UNCONN 0 0 [fe80::be24:11ff:fec4:26]%eth0:546 [::]:*\n" +
	"tcp LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:*\n" +
	"tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n" +
	"tcp LISTEN 0 4096 *:21621 *:*\n" +
	"tcp LISTEN 0 4096 *:4430 *:*\n" +
	"tcp LISTEN 0 4096 127.0.0.1:19090 0.0.0.0:*\n" +
	"tcp LISTEN 0 4096 [::]:8443 [::]:*\n"

func TestParseListenerPortsReadsEveryListeners(t *testing.T) {
	got := parseListenerPorts(ssSample)
	want := []int{22, 53, 546, 4430, 8443, 12538, 19090, 21621}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
}

func TestParseListenerPortsDedupesSamePort(t *testing.T) {
	raw := "tcp LISTEN 0 4096 0.0.0.0:53 0.0.0.0:*\n" +
		"tcp LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:*\n" +
		"udp UNCONN 0 0 208.75.133.177:53 0.0.0.0:*\n"
	got := parseListenerPorts(raw)
	if len(got) != 1 || got[0] != 53 {
		t.Fatalf("ports = %v, want [53]", got)
	}
}

func TestParseListenerPortsIgnoresGarbage(t *testing.T) {
	raw := "State Recv-Q\n" +
		"foo bar baz qux quux\n" +
		"tcp LISTEN 0 4096 0.0.0.0: 0.0.0.0:*\n" +
		"tcp LISTEN 0 4096 norelay 0.0.0.0:*\n"
	if got := parseListenerPorts(raw); len(got) != 0 {
		t.Fatalf("garbage produced %v", got)
	}
	if got := parseListenerPorts(""); len(got) != 0 {
		t.Fatalf("empty table produced %v", got)
	}
}

func TestPortDriftReportsMissingPortsWhenRunning(t *testing.T) {
	drift := PortDrift{Supported: true, Running: true}
	drift.Configured = uniquePortList([]int{12538, 21621, 4430})
	drift.Listening = uniquePortList([]int{21621, 4430})
	drift.Missing = missingPorts(drift.Configured, drift.Listening)
	if !reflect.DeepEqual(drift.Missing, []int{12538}) {
		t.Fatalf("missing = %v, want [12538]", drift.Missing)
	}
}

func TestPortDriftCollectsConfiguredPorts(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	writeConf(t, root, "Socks-21621.json", socksBody)
	drift := m.PortDrift()
	want := []int{12538, 21621}
	if !reflect.DeepEqual(drift.Configured, want) {
		t.Fatalf("configured = %v, want %v", drift.Configured, want)
	}
	if drift.Running {
		t.Fatal("fixture service must not look running")
	}
	if !drift.Supported {
		t.Fatalf("supported = false: %s", drift.Error)
	}
	if len(drift.Missing) != 0 || drift.Pending {
		t.Fatalf("a stopped unit must not report drift: %+v", drift)
	}
}

func TestPortDriftSkipsDisabledNodes(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	writeConf(t, root, "Socks-21621.json.off", socksBody)
	drift := m.PortDrift()
	if !reflect.DeepEqual(drift.Configured, []int{12538}) {
		t.Fatalf("configured = %v, want [12538]", drift.Configured)
	}
}

func TestPortDriftExcludesClashController(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	main := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:19090","secret":"x"}}}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(main), 0o600); err != nil {
		t.Fatalf("write main: %v", err)
	}
	drift := m.PortDrift()
	if !reflect.DeepEqual(drift.Configured, []int{12538}) {
		t.Fatalf("configured = %v, want [12538]", drift.Configured)
	}
}

// touchFixture stamps both the main configuration and the node file, so a test
// can place the whole configuration before the fake unit start. The main
// configuration is written during setup and would otherwise carry the current
// time and look newer than any past start.
func touchFixture(root string, at time.Time) error {
	paths := []string{
		filepath.Join(root, "config.json"),
		filepath.Join(root, "conf", "Hysteria2-12538.json"),
	}
	for _, path := range paths {
		if err := os.Chtimes(path, at, at); err != nil {
			return err
		}
	}
	return nil
}

func TestPendingRestartDetectsNewerNodeFile(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	started := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	previousStart := unitStartedAtHook
	unitStartedAtHook = func(string) (time.Time, error) { return started, nil }
	t.Cleanup(func() { unitStartedAtHook = previousStart })
	old := started.Add(-time.Hour)
	if err := touchFixture(root, old); err != nil {
		t.Skipf("chtimes unsupported: %v", err)
	}
	pending, _, _ := m.pendingRestart()
	if pending {
		t.Fatal("an unchanged node file must not look pending")
	}
	newer := started.Add(time.Minute)
	if err := os.Chtimes(filepath.Join(root, "conf", "Hysteria2-12538.json"), newer, newer); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	pending, changedAt, startedAt := m.pendingRestart()
	if !pending {
		t.Fatal("a node file changed after start must look pending")
	}
	if changedAt == "" || startedAt == "" {
		t.Fatalf("timestamps missing: %q %q", changedAt, startedAt)
	}
}

func TestPendingRestartIgnoresClockSkew(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	started := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	previousStart := unitStartedAtHook
	unitStartedAtHook = func(string) (time.Time, error) { return started, nil }
	t.Cleanup(func() { unitStartedAtHook = previousStart })
	near := started.Add(time.Second)
	if err := touchFixture(root, near); err != nil {
		t.Skipf("chtimes unsupported: %v", err)
	}
	if pending, _, _ := m.pendingRestart(); pending {
		t.Fatal("a file touched within the start second must not look pending")
	}
}

func TestParseUnitTimestamp(t *testing.T) {
	seconds, err := parseUnitTimestamp("1763020800")
	if err != nil || seconds != 1763020800 {
		t.Fatalf("epoch parse = %d %v", seconds, err)
	}
	seconds, err = parseUnitTimestamp("Thu 2026-11-13 08:00:00 UTC")
	if err != nil || seconds == 0 {
		t.Fatalf("readable parse = %d %v", seconds, err)
	}
	if _, err = parseUnitTimestamp(""); err != nil {
		t.Fatalf("empty must not fail: %v", err)
	}
	if _, err = parseUnitTimestamp("n/a"); err == nil {
		t.Fatal("garbage timestamp must fail")
	}
}
