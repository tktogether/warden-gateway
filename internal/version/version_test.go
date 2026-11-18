package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionMatchesVersionFile pins the release hygiene bug that shipped in
// v1.6.0 (ec98a7c) and again in v1.6.3: the VERSION file and CHANGELOG were
// bumped while version.Version stayed behind. Release binaries are stamped by
// CI's -ldflags, so the release itself looked fine, but every source build —
// `go build`, `bootstrap.sh --binary`, a hand-installed binary — reported the
// old number. That number is what the panel's AgentBinaryVersion() and the
// heartbeat target_version are derived from, so a machine could be told to
// "upgrade" to the build it already ran, or told to downgrade.
func TestVersionMatchesVersionFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		t.Fatal("VERSION is empty")
	}
	if Version != want {
		t.Errorf("version.Version = %q but VERSION says %q; bump internal/version/version.go alongside the VERSION file (CI stamps release binaries, source builds report this literal)", Version, want)
	}
}

// TestString pins the rendering both wire shapes depend on: a source build
// (Commit "dev") reports the bare version, a release build appends the commit.
func TestString(t *testing.T) {
	origVersion, origCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = origVersion, origCommit })

	Version, Commit = "1.2.3", "dev"
	if got := String(); got != "1.2.3" {
		t.Errorf("source build String() = %q, want %q", got, "1.2.3")
	}
	Version, Commit = "1.2.3", "abc123"
	if got := String(); got != "1.2.3 (abc123)" {
		t.Errorf("release build String() = %q, want %q", got, "1.2.3 (abc123)")
	}
}
