package cnagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// releaseFixture is the payload a release server hands out: the new agent
// binary and the bare-hex sha256 sidecar written verbatim (trailing
// whitespace/newline allowed, mirroring `sha256sum` output).
type releaseFixture struct {
	binary  []byte
	sidecar string
}

// startReleaseServer serves the Task 7 release layout,
// /github.com/{Repo}/releases/download/{version}/warden-cn-agent-linux-{arch}
// plus the .sha256 sidecar, and counts requests so tests can assert how much
// network traffic a call made.
func startReleaseServer(t *testing.T, version string, fix releaseFixture) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	mux := http.NewServeMux()
	base := "/github.com/" + DefaultRepo + "/releases/download/" + version + "/"
	mux.HandleFunc(base+"warden-cn-agent-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(fix.binary)
	})
	mux.HandleFunc(base+"warden-cn-agent-linux-amd64.sha256", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(fix.sidecar))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// hexOf returns the lowercase hex sha256 of data.
func hexOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeSelf plants a fake "currently running binary" at path.
func writeSelf(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatalf("plant fake self binary: %v", err)
	}
}

// assertNoResidue fails the test when stale .new or .prev files are left next
// to selfPath after a FAILED upgrade (a successful upgrade intentionally
// keeps .prev for the rollback guard).
func assertNoResidue(t *testing.T, selfPath string) {
	t.Helper()
	for _, suffix := range []string{".new", ".prev"} {
		if _, err := os.Stat(selfPath + suffix); err == nil {
			t.Errorf("%s%s exists, want no residue after the failed upgrade", selfPath, suffix)
		}
	}
}

// assertNoNewFile fails the test when a stale .new file is left next to
// selfPath (used on the success path, where .prev must survive).
func assertNoNewFile(t *testing.T, selfPath string) {
	t.Helper()
	if _, err := os.Stat(selfPath + ".new"); err == nil {
		t.Errorf("%s.new exists, want it renamed away after the successful upgrade", selfPath)
	}
}

// TestSelfUpgradeReplacesBinary covers the happy path: the fixture binary and
// its correct sha256 sidecar are downloaded, verified, and installed — the
// current file is replaced with the downloaded content, the previous content
// survives in .prev for the ExecStartPre rollback guard, and no .new residue
// is left behind.
func TestSelfUpgradeReplacesBinary(t *testing.T) {
	oldBinary := []byte("old warden-cn-agent binary v1.5.0")
	newBinary := []byte("new warden-cn-agent binary v1.6.0 with a different length")
	srv, hits := startReleaseServer(t, "v1.6.0", releaseFixture{
		binary:  newBinary,
		sidecar: hexOf(newBinary) + "\n", // trailing newline, like `sha256sum > file.sha256`
	})

	cfg := testConfig(t)
	cfg.Origin = srv.URL
	selfPath := filepath.Join(t.TempDir(), "warden-cn-agent")
	writeSelf(t, selfPath, oldBinary)

	if err := SelfUpgrade(context.Background(), cfg, "v1.6.0", "amd64", selfPath, nil); err != nil {
		t.Fatalf("SelfUpgrade: %v", err)
	}

	got, err := os.ReadFile(selfPath)
	if err != nil {
		t.Fatalf("read upgraded binary: %v", err)
	}
	if string(got) != string(newBinary) {
		t.Errorf("self binary after upgrade = %q, want the downloaded fixture %q", got, newBinary)
	}
	prev, err := os.ReadFile(selfPath + ".prev")
	if err != nil {
		t.Fatalf("read .prev rollback copy: %v", err)
	}
	if string(prev) != string(oldBinary) {
		t.Errorf(".prev content = %q, want the previous binary %q", prev, oldBinary)
	}
	assertNoNewFile(t, selfPath) // .new must be gone after the swap; .prev is kept by design
	if info, statErr := os.Stat(selfPath); statErr == nil && runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("upgraded binary mode = %o, want the owner execute bit (0755)", info.Mode().Perm())
		}
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("release server requests = %d, want 2 (binary + sha256 sidecar)", n)
	}
}

// TestSelfUpgradeBadChecksumLeavesBinaryUntouched pins Review Focus #5: a
// checksum mismatch must abort before touching the local binary — content
// unchanged, no .new/.prev residue, and an error returned.
func TestSelfUpgradeBadChecksumLeavesBinaryUntouched(t *testing.T) {
	oldBinary := []byte("old warden-cn-agent binary v1.5.0")
	newBinary := []byte("tampered-or-mismatched payload")
	srv, hits := startReleaseServer(t, "v1.6.0", releaseFixture{
		binary:  newBinary,
		sidecar: hexOf([]byte("some other release entirely")), // valid-looking hex, wrong hash
	})

	cfg := testConfig(t)
	cfg.Origin = srv.URL
	selfPath := filepath.Join(t.TempDir(), "warden-cn-agent")
	writeSelf(t, selfPath, oldBinary)

	err := SelfUpgrade(context.Background(), cfg, "v1.6.0", "amd64", selfPath, nil)
	if err == nil {
		t.Fatal("SelfUpgrade with a wrong sha256 sidecar = nil, want an error")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error = %v, want it to mention the checksum mismatch", err)
	}

	got, readErr := os.ReadFile(selfPath)
	if readErr != nil {
		t.Fatalf("read binary after failed upgrade: %v", readErr)
	}
	if string(got) != string(oldBinary) {
		t.Errorf("binary content changed after failed upgrade = %q, want %q", got, oldBinary)
	}
	assertNoResidue(t, selfPath)
	if n := hits.Load(); n != 2 {
		t.Errorf("release server requests = %d, want 2 (both files fetched before verification)", n)
	}
}

// TestSelfUpgradeUnsupportedArch pins the arch allow-list: an arch outside
// amd64|arm64|arm is rejected before any network call — the release server
// must see zero requests.
func TestSelfUpgradeUnsupportedArch(t *testing.T) {
	newBinary := []byte("payload that must never be fetched")
	srv, hits := startReleaseServer(t, "v1.6.0", releaseFixture{
		binary:  newBinary,
		sidecar: hexOf(newBinary),
	})

	cfg := testConfig(t)
	cfg.Origin = srv.URL
	selfPath := filepath.Join(t.TempDir(), "warden-cn-agent")
	writeSelf(t, selfPath, []byte("old binary"))

	err := SelfUpgrade(context.Background(), cfg, "v1.6.0", "riscv64", selfPath, nil)
	if err == nil {
		t.Fatal("SelfUpgrade with arch riscv64 = nil, want an error")
	}
	if !strings.Contains(err.Error(), "arch") {
		t.Errorf("error = %v, want it to mention the unsupported arch", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("release server requests = %d, want 0 (arch validated before any download)", n)
	}
	got, readErr := os.ReadFile(selfPath)
	if readErr != nil {
		t.Fatalf("read binary after rejected upgrade: %v", readErr)
	}
	if string(got) != "old binary" {
		t.Errorf("binary content changed after rejected upgrade = %q, want untouched", got)
	}
	assertNoResidue(t, selfPath)
}
