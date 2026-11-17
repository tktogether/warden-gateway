package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tktogether/warden-gateway/internal/version"
)

// agentAssetDirName is the directory under ScriptDir that holds cached CN agent
// builds. Layout: <ScriptDir>/agent/<version>/warden-cn-agent-linux-{arch} plus
// a bare-hex .sha256 sidecar. bootstrap.sh populates it at gateway install and
// upgrade time (keeping the most recent versions), so a CN machine can install,
// upgrade, roll back or stay pinned to an older build without ever reaching
// GitHub and without the gateway having to run that version itself.
const agentAssetDirName = "agent"

// agentArchs is the allow-list served by handleAgentBinary. It mirrors the
// release workflow's asset matrix and internal/cnagent's upgrade allow-list.
var agentArchs = map[string]bool{"amd64": true, "arm64": true, "arm": true}

// agentVersionPattern keeps the version path segment to something that can only
// ever be a release tag: no separators, no dots-dots, no empties.
var agentVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

// normalizeAgentVersion trims a tag's leading "v" so "v1.6.0" and "1.6.0"
// address the same cache directory. CI stamps version.Version with the tag,
// dev builds leave it bare.
func normalizeAgentVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// AgentBinaryVersion is the agent build version this gateway runs and therefore
// offers by default: its own release version, without the "v".
func AgentBinaryVersion() string {
	return normalizeAgentVersion(version.Version)
}

// handleAgentBinary serves a cached CN agent binary and its checksum sidecar.
//
// Path shape: /api/agent/binary/{version}/{arch}[.sha256]. The version segment
// addresses the per-version cache directory, so a machine can hold any build the
// operator has cached — including an older one it is being pinned back to.
//
// Unlike the heartbeat endpoint this is unauthenticated: bootstrap.sh fetches
// the binary before it holds a machine token, and the payload is a public
// release artifact. Both path elements are checked against allow-lists and the
// version against a strict pattern, so a crafted request cannot read arbitrary
// files.
func (s *Server) handleAgentBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/agent/binary/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	wantVersion, name := normalizeAgentVersion(parts[0]), parts[1]
	arch := strings.TrimSuffix(name, ".sha256")
	if !agentArchs[arch] || (name != arch && name != arch+".sha256") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !agentVersionPattern.MatchString(wantVersion) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	file := "warden-cn-agent-linux-" + arch
	withSidecar := strings.HasSuffix(name, ".sha256")
	if withSidecar {
		file += ".sha256"
	}
	raw, err := os.ReadFile(filepath.Join(s.scriptDir, agentAssetDirName, wantVersion, file))
	if err != nil {
		http.Error(w, "no cached agent build for "+wantVersion+" on this gateway (re-run the gateway installer, or pick a cached version in the panel)", http.StatusNotFound)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if withSidecar {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	_, _ = w.Write(raw)
}

// CachedAgentVersions lists the agent versions cached on this gateway, newest
// first, so the panel can offer them as pin/rollback targets.
func (s *Server) CachedAgentVersions() []string {
	entries, err := os.ReadDir(filepath.Join(s.scriptDir, agentAssetDirName))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if agentVersionPattern.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j], out[i]) })
	return out
}

// CachedAgentVersion reports whether a given version is served by this gateway.
func (s *Server) CachedAgentVersion(v string) bool {
	v = normalizeAgentVersion(v)
	if !agentVersionPattern.MatchString(v) {
		return false
	}
	for _, cached := range s.CachedAgentVersions() {
		if cached == v {
			return true
		}
	}
	return false
}

// versionLess compares dotted numeric versions ("1.10.0" > "1.9.0"); it falls
// back to string order for anything non-numeric so odd tags still sort stably.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aok := atoiSafe(as[i])
		bi, bok := atoiSafe(bs[i])
		if aok && bok {
			if ai != bi {
				return ai < bi
			}
			continue
		}
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

func atoiSafe(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0, false
		}
	}
	return n, true
}

// agentBinaryURL is the URL a CN machine downloads an agent build from. Empty
// origin yields an empty URL (callers then print a manual instruction).
func agentBinaryURL(origin, ver, arch string) string {
	if origin == "" {
		return ""
	}
	return strings.TrimRight(origin, "/") + "/api/agent/binary/" + normalizeAgentVersion(ver) + "/" + arch
}
