package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tktogether/warden-gateway/internal/version"
)

// agentBinaryPanel returns a panel whose scriptDir holds a fake cached agent
// build, mirroring what cache_agent_binaries leaves behind at install time.
func agentBinaryPanel(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv, key := newPanel(t)
	srv.scriptDir = dir
	if err := os.MkdirAll(filepath.Join(dir, agentAssetDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	return srv, key
}

// TestAgentBinaryServesCachedBuild pins the self-hosted distribution path:
// /api/agent/binary/{version}/{arch}[.sha256] serves the cached release asset
// without authentication (bootstrap.sh fetches it before it holds a token),
// while any other version, arch or path shape is refused so a crafted request
// cannot read arbitrary files.
func TestAgentBinaryServesCachedBuild(t *testing.T) {
	srv, _ := agentBinaryPanel(t)
	assetDir := filepath.Join(srv.scriptDir, agentAssetDirName)
	ver := AgentBinaryVersion()
	binary := []byte("\x7fELF fake agent build")
	if err := os.WriteFile(filepath.Join(assetDir, "warden-cn-agent-linux-amd64"), binary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "warden-cn-agent-linux-amd64.sha256"), []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"binary", "/api/agent/binary/" + ver + "/amd64", http.StatusOK, string(binary)},
		{"sidecar", "/api/agent/binary/" + ver + "/amd64.sha256", http.StatusOK, "abc123\n"},
		{"trailing slash", "/api/agent/binary/" + ver + "/amd64/", http.StatusOK, string(binary)},
		{"unknown version", "/api/agent/binary/9.9.9/amd64", http.StatusNotFound, ""},
		{"uncached arch", "/api/agent/binary/" + ver + "/riscv64", http.StatusNotFound, ""},
		{"path traversal", "/api/agent/binary/" + ver + "/../../../../etc/passwd", http.StatusNotFound, ""},
		{"missing version", "/api/agent/binary/amd64", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, srv, "GET", tc.path, nil, nil, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestAgentBinaryRejectsNonGET covers the method gate: the endpoint is public,
// so it must not accept anything that could mutate state.
func TestAgentBinaryRejectsNonGET(t *testing.T) {
	srv, _ := agentBinaryPanel(t)
	rec := do(t, srv, "POST", "/api/agent/binary/"+AgentBinaryVersion()+"/amd64", nil, nil, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestAgentBinaryMissingCacheIsExplicit pins the operator-facing failure: a
// gateway installed before the cache existed (or with --no-agent) says so
// instead of returning an empty 200 that the agent would reject as a corrupt
// download.
func TestAgentBinaryMissingCacheIsExplicit(t *testing.T) {
	srv, _ := agentBinaryPanel(t)
	rec := do(t, srv, "GET", "/api/agent/binary/"+AgentBinaryVersion()+"/amd64", nil, nil, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Body.String(); got == "" || !contains(got, "not cached") {
		t.Fatalf("body = %q, want an explanation mentioning the missing cache", got)
	}
}

// TestAgentBinaryVersionHasNoVPrefix pins the single-v normalization the URL
// and both downloaders rely on: CI stamps "v1.6.0", dev builds "1.6.0", and
// the served path segment is always the bare version.
func TestAgentBinaryVersionHasNoVPrefix(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	for _, stamp := range []string{"v9.9.9", "9.9.9"} {
		version.Version = stamp
		if got := AgentBinaryVersion(); got != "9.9.9" {
			t.Fatalf("AgentBinaryVersion() with stamp %q = %q, want 9.9.9", stamp, got)
		}
	}
}

// TestAgentBinaryURLShape pins the URL the panel advertises and the agent's
// own SelfUpgrade requests, so the two can never drift apart.
func TestAgentBinaryURLShape(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "v9.9.9"
	if got, want := agentBinaryURL("http://gw.example.com/", "arm64"), "http://gw.example.com/api/agent/binary/9.9.9/arm64"; got != want {
		t.Fatalf("agentBinaryURL = %q, want %q", got, want)
	}
	if got := agentBinaryURL("", "amd64"); got != "" {
		t.Fatalf("empty origin should yield empty URL, got %q", got)
	}
}

// TestCNCommandsCarriesSelfHostedAgentURL pins that /api/cn advertises the
// self-hosted path rather than a GitHub release URL.
func TestCNCommandsCarriesSelfHostedAgentURL(t *testing.T) {
	srv, key := agentBinaryPanel(t)
	cookie, csrf := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/cn", nil, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var payload struct {
		AgentBinaryURL     string `json:"agent_binary_url"`
		AgentBinaryVersion string `json:"agent_binary_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:18080/api/agent/binary/" + AgentBinaryVersion() + "/amd64"
	if payload.AgentBinaryURL != want {
		t.Fatalf("agent_binary_url = %q, want %q", payload.AgentBinaryURL, want)
	}
	if payload.AgentBinaryVersion != AgentBinaryVersion() {
		t.Fatalf("agent_binary_version = %q, want %q", payload.AgentBinaryVersion, AgentBinaryVersion())
	}
}

// contains is a tiny helper so this file needs no strings import for one call.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
