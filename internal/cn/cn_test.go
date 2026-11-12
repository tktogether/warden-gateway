package cn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"/etc/apt", "/etc/docker", "/var/backups/warden-gateway-cn"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "/etc/hosts"), []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "/etc/resolv.conf"), []byte("nameserver 9.9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "/etc/apt/sources.list"), []byte("deb http://archive.ubuntu.com/ubuntu jammy main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Manager{
		Root:       root,
		BackupRoot: filepath.Join(root, "var/backups/warden-gateway-cn"),
		Exec: func(name string, args ...string) (string, error) {
			if name == "sysctl" {
				return "", nil
			}
			return "", nil
		},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplyWritesHostsAndDNSFirst(t *testing.T) {
	m := testManager(t)
	report, err := m.Apply(Options{GatewayIP: "203.0.113.10", Hosts: true, DNS: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, step := range report.Steps {
		if !step.OK {
			t.Fatalf("step %s failed: %s", step.Name, step.Detail)
		}
	}
	hosts := readFile(t, filepath.Join(m.Root, "/etc/hosts"))
	if !strings.Contains(hosts, hostsBegin) || !strings.Contains(hosts, "203.0.113.10 github.com api.github.com") {
		t.Fatalf("hosts content: %s", hosts)
	}
	if !strings.Contains(hosts, "127.0.0.1 localhost") {
		t.Fatalf("hosts lost original entry: %s", hosts)
	}
	resolv := readFile(t, filepath.Join(m.Root, "/etc/resolv.conf"))
	if !strings.HasPrefix(resolv, hostsBegin) {
		t.Fatalf("managed DNS must come first: %s", resolv)
	}
	if !strings.Contains(resolv, "nameserver 203.0.113.10") || !strings.Contains(resolv, "nameserver 9.9.9.9") {
		t.Fatalf("resolv content: %s", resolv)
	}
}

func TestApplyReplacesGitMirror(t *testing.T) {
	m := testManager(t)
	opts := Options{GatewayIP: "203.0.113.10", Git: true, GitMirror: "http://203.0.113.10"}
	if _, err := m.Apply(opts); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	opts.GitMirror = "http://203.0.113.20"
	if _, err := m.Apply(opts); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	conf := readFile(t, filepath.Join(m.Root, "/etc/gitconfig"))
	if strings.Count(conf, "/https://github.com/\"]") != 1 {
		t.Fatalf("git config should hold one mirror: %s", conf)
	}
	if !strings.Contains(conf, "203.0.113.20") || strings.Contains(conf, "203.0.113.10") {
		t.Fatalf("git config not replaced: %s", conf)
	}
	status, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Git.Mirrors) != 1 || status.Git.Mirrors[0] != "http://203.0.113.20" {
		t.Fatalf("git status mirrors: %+v", status.Git.Mirrors)
	}
}

func TestApplyRejectsBadGateway(t *testing.T) {
	m := testManager(t)
	if _, err := m.Apply(Options{GatewayIP: "not-an-ip", Hosts: true}); err == nil {
		t.Fatal("expected error for invalid gateway")
	}
}

func TestRestoreBringsFilesBack(t *testing.T) {
	m := testManager(t)
	origHosts := readFile(t, filepath.Join(m.Root, "/etc/hosts"))
	report, err := m.Apply(Options{GatewayIP: "203.0.113.10", Hosts: true, APT: true, APTMirror: "https://mirrors.aliyun.com"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.BackupID == "" {
		t.Fatal("missing backup id")
	}
	if _, err := m.Restore("latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, filepath.Join(m.Root, "/etc/hosts")); got != origHosts {
		t.Fatalf("hosts not restored: %q", got)
	}
}

func TestStatusReportsDockerFromSocket(t *testing.T) {
	m := testManager(t)
	if err := os.MkdirAll(filepath.Join(m.Root, "/var/run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Root, "/var/run/docker.sock"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Docker.Installed {
		t.Fatal("docker socket should mark docker installed")
	}
}
