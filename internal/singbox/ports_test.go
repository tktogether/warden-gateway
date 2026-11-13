package singbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// procTCPSample is a /proc/net/tcp table: one listening sing-box socket (inode
// 31417, port 4430), the Clash API listener (inode 31418, port 19090), an
// established connection that must be ignored, and a foreign listener.
const procTCPSample = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
	"   0: 00000000:114E 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 31417 1 0000000000000000 100 0 0 10 0\n" +
	"   1: 0100007F:4A92 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 31418 1 0000000000000000 100 0 0 10 0\n" +
	"   2: 0100007F:1F90 0100007F:D431 01 00000000:00000000 00:00000000 00000000     0        0 31419 1 0000000000000000 100 0 0 10 0\n" +
	"   3: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 99999 1 0000000000000000 100 0 0 10 0\n"

// procUDPSample is a /proc/net/udp table: one bound sing-box socket (inode
// 31420, port 12538) and a connected socket that must be ignored.
const procUDPSample = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n" +
	"   0: 00000000:30FA 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 31420 2 0000000000000000 0\n" +
	"   1: 00000000:30FB 0100007F:0035 01 00000000:00000000 00:00000000 00000000     0        0 31421 2 0000000000000000 0\n"

// writeProcTree builds a fake procfs with the given socket inodes owned by pid 4242.
func writeProcTree(t *testing.T, inodes []int) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	tables := map[string]string{
		"net/tcp":  procTCPSample,
		"net/tcp6": "  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n",
		"net/udp":  procUDPSample,
		"net/udp6": "  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n",
	}
	for name, body := range tables {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fdDir := filepath.Join(root, "4242", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, inode := range inodes {
		link := filepath.Join(fdDir, itoa(i))
		if err := os.Symlink("socket:["+itoa(inode)+"]", link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
	}
	return root
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	out := ""
	for v > 0 {
		out = string(rune('0'+v%10)) + out
		v /= 10
	}
	return out
}

const fixturePid = 4242

func withProcFixture(t *testing.T, root string) {
	t.Helper()
	previousRoot, previousInodes, previousPids := procRoot, processSocketInodesHook, unitMainPidsHook
	procRoot = root
	processSocketInodesHook = processSocketInodes
	unitMainPidsHook = func(string) ([]int, error) { return []int{fixturePid}, nil }
	t.Cleanup(func() {
		procRoot = previousRoot
		processSocketInodesHook = previousInodes
		unitMainPidsHook = previousPids
	})
}

func TestParseProcNetKeepsListeningSockets(t *testing.T) {
	got := parseProcNet(procTCPSample, "tcp")
	want := map[uint64]int{31417: 4430, 31418: 19090, 99999: 22}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tcp parse = %v, want %v", got, want)
	}
	got = parseProcNet(procUDPSample, "udp")
	want = map[uint64]int{31420: 12538}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("udp parse = %v, want %v", got, want)
	}
}

func TestParseProcNetRejectsGarbage(t *testing.T) {
	if got := parseProcNet("", "tcp"); len(got) != 0 {
		t.Fatalf("empty table produced %v", got)
	}
	if got := parseProcNet("sl local_address\nfoo\n", "tcp"); len(got) != 0 {
		t.Fatalf("header-only table produced %v", got)
	}
	if got := parseProcNet(procTCPSample, "sctp"); len(got) != 0 {
		t.Fatalf("unknown protocol produced %v", got)
	}
}

func TestListeningPortsMapsProcessInodes(t *testing.T) {
	root := writeProcTree(t, []int{31417, 31420, 31419})
	withProcFixture(t, root)
	m := New(Options{Root: t.TempDir(), DataDir: t.TempDir(), Service: "sing-box.service"})
	ports, err := m.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts: %v", err)
	}
	want := []int{4430, 12538}
	if !reflect.DeepEqual(ports, want) {
		t.Fatalf("ports = %v, want %v", ports, want)
	}
}

func TestListeningPortsIgnoresEstablishedAndForeign(t *testing.T) {
	root := writeProcTree(t, []int{31419, 31421})
	withProcFixture(t, root)
	m := New(Options{Root: t.TempDir(), DataDir: t.TempDir(), Service: "sing-box.service"})
	ports, err := m.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts: %v", err)
	}
	if len(ports) != 0 {
		t.Fatalf("ports = %v, want none", ports)
	}
}

func TestDriftPortsReportsMissingAndExtra(t *testing.T) {
	missing, extra := DriftPorts([]int{12538, 21621, 4430}, []int{4430, 12538, 21621, 18444})
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if !reflect.DeepEqual(extra, []int{18444}) {
		t.Fatalf("extra = %v, want [18444]", extra)
	}
	missing, extra = DriftPorts([]int{12538, 21080}, []int{12538})
	if !reflect.DeepEqual(missing, []int{21080}) {
		t.Fatalf("missing = %v, want [21080]", missing)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %v, want none", extra)
	}
	missing, extra = DriftPorts(nil, nil)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("empty drift: %v %v", missing, extra)
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
	if len(drift.Missing) != 0 || len(drift.Extra) != 0 {
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

func TestPortDriftReportsMissingPortsWhenRunning(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	writeConf(t, root, "Socks-21621.json", socksBody)
	procTree := writeProcTree(t, []int{31420})
	withProcFixture(t, procTree)
	drift := PortDrift{Supported: true, Running: true}
	listening, err := m.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts: %v", err)
	}
	drift.Listening = uniquePortList(listening)
	drift.Configured = uniquePortList([]int{12538, 21621})
	drift.Missing, drift.Extra = DriftPorts(drift.Configured, drift.Listening)
	if !reflect.DeepEqual(drift.Missing, []int{21621}) {
		t.Fatalf("missing = %v, want [21621]", drift.Missing)
	}
	if !strings.Contains(joinPortText(drift.Listening), "12538") {
		t.Fatalf("listening = %v, want 12538", drift.Listening)
	}
}

func joinPortText(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, itoa(port))
	}
	return strings.Join(parts, ",")
}
