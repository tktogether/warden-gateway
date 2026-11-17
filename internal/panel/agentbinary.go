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

// normalizeReportedAgentVersion defensively turns a heartbeat agent_version
// into the bare version the panel compares against pins. New agents already
// send "1.6.0"; older ones sent version.String() ("v1.6.0 (sha)"), which used
// to make the "待升级/已钉版本" badge compare unequal forever.
func normalizeReportedAgentVersion(v string) string {
	if fields := strings.Fields(v); len(fields) > 0 {
		return normalizeAgentVersion(fields[0])
	}
	return ""
}

// AgentBinaryVersion is the agent build version this gateway runs and therefore
// offers by default: its own release version, without the "v".
func AgentBinaryVersion() string {
	return normalizeAgentVersion(version.Version)
}

// agentBinaryAssetArch resolves the second path segment of the agent binary
// endpoint to a supported arch. Both shapes clients use are accepted:
//
//	{arch}                         (short form kept for existing installs)
//	warden-cn-agent-linux-{arch}   (bootstrap.sh, SelfUpgrade and the panel URL)
//
// each with an optional .sha256 suffix. Anything else is rejected so a crafted
// request cannot reach a different file.
func agentBinaryAssetArch(name string) (arch string, sidecar bool, ok bool) {
	sidecar = strings.HasSuffix(name, ".sha256")
	if sidecar {
		name = strings.TrimSuffix(name, ".sha256")
	}
	arch = strings.TrimPrefix(name, "warden-cn-agent-linux-")
	if !agentArchs[arch] {
		return "", false, false
	}
	return arch, sidecar, true
}

// handleAgentBinary serves a cached CN agent binary and its checksum sidecar.
//
// Path shape: /api/agent/binary/{version}/{arch}[.sha256] or
// /api/agent/binary/{version}/warden-cn-agent-linux-{arch}[.sha256] — the
// release-asset spelling is what bootstrap.sh, internal/cnagent's SelfUpgrade
// and the panel-advertised URL use; the short form is kept working for older
// installs. The version segment addresses the per-version cache directory, so
// a machine can hold any build the operator has cached — including an older
// one it is being pinned back to.
// path is the ROUTED path (panel prefix already stripped by route), never
// r.URL.Path: the prefix exists in production URLs and re-trimming the raw URL
// here made the endpoint 404 for every real request.
//
// Unlike the heartbeat endpoint this is unauthenticated: bootstrap.sh fetches
// the binary before it holds a machine token, and the payload is a public
// release artifact. Both path elements are checked against allow-lists and the
// version against a strict pattern, so a crafted request cannot read arbitrary
// files.
func (s *Server) handleAgentBinary(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(path, "/api/agent/binary/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	wantVersion, name := normalizeAgentVersion(parts[0]), parts[1]
	arch, withSidecar, ok := agentBinaryAssetArch(name)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !agentVersionPattern.MatchString(wantVersion) || wantVersion == "latest" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	file := "warden-cn-agent-linux-" + arch
	if withSidecar {
		file += ".sha256"
	}
	filePath := filepath.Join(s.scriptDir, agentAssetDirName, wantVersion, file)
	f, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "no cached agent build for "+wantVersion+" on this gateway (re-run the gateway installer, or pick a cached version in the panel)", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
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
	// ServeContent streams the file (no whole-file read per request), handles
	// HEAD/Range, and sets Last-Modified; the explicit Content-Type above is
	// preserved.
	http.ServeContent(w, r, file, info.ModTime(), f)
}

// cachedAgentBuildExists reports whether dir holds at least one servable
// build: a warden-cn-agent-linux-<arch> binary from the allow-list whose
// .sha256 sidecar is present too. bootstrap.sh creates agent/<ver>/ before the
// downloads, so an empty (or half-populated) directory must not be offered as
// a pin target — such a version would 404 forever.
func cachedAgentBuildExists(dir string) bool {
	for arch := range agentArchs {
		bin := filepath.Join(dir, "warden-cn-agent-linux-"+arch)
		if info, err := os.Stat(bin); err != nil || info.IsDir() {
			continue
		}
		if info, err := os.Stat(bin + ".sha256"); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// CachedAgentVersions lists the agent versions cached on this gateway, newest
// first, so the panel can offer them as pin/rollback targets.
func (s *Server) CachedAgentVersions() []string {
	root := filepath.Join(s.scriptDir, agentAssetDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "latest" {
			continue
		}
		if !agentVersionPattern.MatchString(e.Name()) {
			continue
		}
		if !cachedAgentBuildExists(filepath.Join(root, e.Name())) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j], out[i]) })
	return out
}

// CachedAgentVersion reports whether a given version is served by this gateway.
func (s *Server) CachedAgentVersion(v string) bool {
	v = normalizeAgentVersion(v)
	if v == "" || v == "latest" || !agentVersionPattern.MatchString(v) {
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

// agentBinaryURL is the URL a CN machine downloads an agent build from. It
// uses the same release-asset shape bootstrap.sh and SelfUpgrade request
// (warden-cn-agent-linux-<arch>), and the server accepts this and the short
// <arch> form. Empty origin yields an empty URL (callers then print a manual
// instruction).
func agentBinaryURL(origin, ver, arch string) string {
	if origin == "" {
		return ""
	}
	return strings.TrimRight(origin, "/") + "/api/agent/binary/" + normalizeAgentVersion(ver) + "/warden-cn-agent-linux-" + arch
}
