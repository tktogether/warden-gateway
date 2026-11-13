package singbox

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const ssSample = "tcp   LISTEN 0      8192                        127.0.0.1:19090           0.0.0.0:*    users:((\"sing-box\",pid=157461,fd=12))\n" +
	"tcp   LISTEN 0      8192                                *:21621               *:*    users:((\"sing-box\",pid=157461,fd=10))\n" +
	"tcp   LISTEN 0      8192                                *:4430                *:*    users:((\"sing-box\",pid=157461,fd=11))\n" +
	"udp   UNCONN 0      0                                   *:12538               *:*    users:((\"sing-box\",pid=157461,fd=9))\n" +
	"tcp   LISTEN 0      4096                        127.0.0.53%lo:53          0.0.0.0:*    users:((\"systemd-resolve\",pid=9660,fd=14))\n" +
	"tcp   LISTEN 0      8192                          127.0.0.1:8443            0.0.0.0:*    users:((\"warden-gateway\",pid=144987,fd=6))\n" +
	"State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"

func TestParseSingboxPortsKeepsOnlySingbox(t *testing.T) {
	got := parseSingboxPorts(ssSample)
	want := []int{4430, 12538, 19090, 21621}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
}

func TestParseSingboxPortsDedupAndSort(t *testing.T) {
	out := "udp UNCONN 0 0 *:9000 *:* users:((\"sing-box\",pid=1,fd=3))\n" +
		"tcp LISTEN 0 0 *:9000 *:* users:((\"sing-box\",pid=1,fd=4))\n" +
		"tcp LISTEN 0 0 [::]:8080 [::]:* users:((\"sing-box\",pid=1,fd=5))\n"
	got := parseSingboxPorts(out)
	want := []int{8080, 9000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
}

func TestParseSingboxPortsIgnoresGarbage(t *testing.T) {
	if got := parseSingboxPorts(""); len(got) != 0 {
		t.Fatalf("empty output produced %v", got)
	}
	if got := parseSingboxPorts("not an ss line\nfoo bar\n"); len(got) != 0 {
		t.Fatalf("garbage produced %v", got)
	}
}

func TestDriftPortsReportsMissingAndExtra(t *testing.T) {
	missing, extra := DriftPorts([]int{12538, 21621, 4430}, []int{4430, 12538, 21621, 18444})
	if !reflect.DeepEqual(missing, []int(nil)) && len(missing) != 0 {
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
	if len(drift.Missing) != 0 || len(drift.Extra) != 0 {
		t.Fatalf("drift should be empty without a listening probe: %#v", drift)
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
