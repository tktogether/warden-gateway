package singbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeCoreVersion(t *testing.T) {
	cases := map[string]string{
		"":          "latest",
		"latest":    "latest",
		" LATEST ":  "latest",
		"stable":    "latest",
		"v1.14.4":   "1.14.4",
		"1.14.4":    "1.14.4",
		" V1.14.4 ": "1.14.4",
	}
	for input, want := range cases {
		if got := NormalizeCoreVersion(input); got != want {
			t.Fatalf("NormalizeCoreVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidCoreVersion(t *testing.T) {
	good := []string{"1.14.4", "1.14", "1.14.4.1"}
	bad := []string{"", "latest", "v1.14.4", "1", "1..4", "1.14.x", strings.Repeat("1", 40)}
	for _, version := range good {
		if !validCoreVersion(version) {
			t.Fatalf("validCoreVersion(%q) = false, want true", version)
		}
	}
	for _, version := range bad {
		if validCoreVersion(version) {
			t.Fatalf("validCoreVersion(%q) = true, want false", version)
		}
	}
}

func TestCorePlatformMatchesSupportedTable(t *testing.T) {
	platform, err := CorePlatform()
	supported := runtime.GOOS == "linux"
	if supported {
		if err != nil {
			t.Fatalf("CorePlatform on linux: %v", err)
		}
		if !strings.HasPrefix(platform, "linux-") {
			t.Fatalf("CorePlatform = %q, want linux-*", platform)
		}
		return
	}
	if err == nil {
		t.Fatalf("CorePlatform on %s/%s should be unsupported, got %q", runtime.GOOS, runtime.GOARCH, platform)
	}
}

func TestCoreUnitTemplateIsMarkedAsManaged(t *testing.T) {
	unit := coreUnitTemplate("/etc/sing-box", "/etc/sing-box/config.json")
	if !strings.HasPrefix(unit, "# managed by warden-gateway\n") {
		t.Fatalf("unit must start with the managed marker: %q", unit)
	}
	want := "ExecStart=" + filepath.Join("/etc/sing-box", "bin", "sing-box") + " run -c /etc/sing-box/config.json -C " + filepath.Join("/etc/sing-box", "conf")
	if !strings.Contains(unit, want) {
		t.Fatalf("unit missing %q:\n%s", want, unit)
	}
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Fatalf("unit missing install section:\n%s", unit)
	}
}

func TestCoreUnitPathAppendsServiceSuffix(t *testing.T) {
	want := filepath.Join("/etc/systemd/system", "sing-box.service")
	if got := coreUnitPath("sing-box"); got != want {
		t.Fatalf("coreUnitPath without suffix = %q, want %q", got, want)
	}
	if got := coreUnitPath("sing-box.service"); got != want {
		t.Fatalf("coreUnitPath with suffix = %q, want %q", got, want)
	}
}

func TestCoreBackupsSortedNewestFirst(t *testing.T) {
	m, _ := newTestManager(t)
	bin := filepath.Join(m.root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	for _, name := range []string{"sing-box.bak-1.14.2", "sing-box.bak-1.14.4", "sing-box"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	backups := m.coreBackups()
	if len(backups) != 2 {
		t.Fatalf("coreBackups = %v, want two entries", backups)
	}
	if backups[0] != "sing-box.bak-1.14.4" || backups[1] != "sing-box.bak-1.14.2" {
		t.Fatalf("coreBackups not sorted newest first: %v", backups)
	}
}

func TestPreinstallStateRoundTrip(t *testing.T) {
	m, _ := newTestManager(t)
	st := preinstallState{
		At:             "2026-11-12T00:00:00Z",
		UnitPath:       filepath.Join("/etc/systemd/system", "sing-box.service"),
		UnitExisted:    true,
		UnitSaved:      filepath.Join(m.dataDir, "singbox-core", "preinstall", "sing-box.service"),
		BinaryPath:     filepath.Join(m.root, "bin", "sing-box"),
		BinaryExisted:  true,
		BinarySaved:    filepath.Join(m.dataDir, "singbox-core", "preinstall", "sing-box"),
		ServiceActive:  true,
		ServiceEnabled: true,
	}
	if err := m.savePreinstall(st); err != nil {
		t.Fatalf("savePreinstall: %v", err)
	}
	loaded, err := m.loadPreinstall()
	if err != nil {
		t.Fatalf("loadPreinstall: %v", err)
	}
	if loaded.BinaryPath != st.BinaryPath || !loaded.BinaryExisted || !loaded.UnitExisted || !loaded.ServiceActive || !loaded.ServiceEnabled {
		t.Fatalf("preinstall round trip mismatch: %+v", loaded)
	}
	if loaded.At != st.At {
		t.Fatalf("preinstall at = %q, want %q", loaded.At, st.At)
	}
}

func TestRollbackPreinstallWithoutSnapshotFails(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.RollbackPreinstall(context.Background()); err == nil {
		t.Fatal("RollbackPreinstall without a snapshot must fail")
	}
	if _, err := os.Stat(m.preinstallFile()); !os.IsNotExist(err) {
		t.Fatalf("rollback without snapshot must not create state: %v", err)
	}
}

func TestRollbackPreviousWithoutBackupsFails(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.RollbackPrevious(context.Background()); err == nil {
		t.Fatal("RollbackPrevious without backups must fail")
	}
}

func TestUninstallCoreRequiresServiceUnit(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.UninstallCore(context.Background()); err == nil {
		t.Fatal("UninstallCore without a service unit must fail")
	}
	if _, err := os.Stat(filepath.Join(m.dataDir, "singbox-core")); !os.IsNotExist(err) {
		t.Fatalf("failed uninstall must not create archive directories: %v", err)
	}
}

func TestCoreStatusWithoutLatestKeepsLocalFields(t *testing.T) {
	m, _ := newTestManager(t)
	if err := os.MkdirAll(filepath.Join(m.root, "bin"), 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(m.root, "bin", "sing-box.bak-1.14.3"), []byte("x"), 0o755); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	status := m.CoreLocalStatus()
	if status.Installed {
		t.Fatalf("CoreLocalStatus reports installed without a kernel: %+v", status)
	}
	if len(status.Backups) != 1 || status.Backups[0] != "sing-box.bak-1.14.3" {
		t.Fatalf("CoreLocalStatus backups = %v", status.Backups)
	}
	if status.Platform == "" {
		t.Fatalf("CoreLocalStatus must report a platform: %+v", status)
	}
}
