package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tktogether/warden-gateway/internal/version"
)

// agentAssetDirName is the directory under ScriptDir that holds the CN agent
// binaries this gateway serves to its machines. bootstrap.sh populates it at
// install time (one file per supported arch plus a bare-hex .sha256 sidecar),
// so CN machines never have to reach GitHub to install or upgrade: they ask
// the gateway that enrolled them.
const agentAssetDirName = "agent"

// agentArchs is the allow-list served by handleAgentBinary. It mirrors the
// release workflow's asset matrix and internal/cnagent's upgrade allow-list.
var agentArchs = map[string]bool{"amd64": true, "arm64": true, "arm": true}

// handleAgentBinary serves the CN agent binary and its checksum sidecar from
// the locally cached release assets.
//
// Path shape: /api/agent/binary/{version}/{arch}[.sha256]. The version is part
// of the path so a machine can never mistake one build for another: only the
// version cached on this gateway is served, and anything else is a 404 that
// the agent treats as a failed upgrade (leaving the running binary alone).
//
// Unlike the heartbeat endpoint this is unauthenticated: bootstrap.sh fetches
// the binary before it holds a machine token, and the payload is a public
// release artifact. Both path elements are checked against allow-lists, so a
// crafted request cannot read arbitrary files.
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
	wantVersion, name := parts[0], parts[1]
	arch := strings.TrimSuffix(name, ".sha256")
	if !agentArchs[arch] || (name != arch && name != arch+".sha256") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if wantVersion != AgentBinaryVersion() {
		http.Error(w, "no cached agent build for version "+wantVersion, http.StatusNotFound)
		return
	}
	file := "warden-cn-agent-linux-" + arch
	withSidecar := strings.HasSuffix(name, ".sha256")
	if withSidecar {
		file += ".sha256"
	}
	raw, err := os.ReadFile(filepath.Join(s.scriptDir, agentAssetDirName, file))
	if err != nil {
		http.Error(w, "agent binary not cached on this gateway (re-run the gateway installer to populate it)", http.StatusNotFound)
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

// AgentBinaryVersion is the agent build version this gateway serves: its own
// release version with the tag's "v" prefix stripped, so the path segment
// reads v1.6.0 exactly once.
func AgentBinaryVersion() string {
	return strings.TrimPrefix(version.Version, "v")
}

// agentBinaryURL is the URL a CN machine downloads an agent build from. Empty
// origin yields an empty URL (callers then print a manual instruction).
func agentBinaryURL(origin, arch string) string {
	if origin == "" {
		return ""
	}
	return strings.TrimRight(origin, "/") + "/api/agent/binary/" + AgentBinaryVersion() + "/" + arch
}
