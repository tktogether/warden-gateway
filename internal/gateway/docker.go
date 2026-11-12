package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/daocloud/crproxy"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

const emptyOCIIndex = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`

type DockerHandler struct {
	cfg   config.DockerConfig
	proxy *crproxy.CRProxy
	stats *telemetry.Store
	log   *logging.Logger
}

func NewDockerHandler(cfg config.DockerConfig, stats *telemetry.Store, log *logging.Logger) (*DockerHandler, error) {
	h := &DockerHandler{cfg: cfg, stats: stats, log: log}
	if !cfg.Enabled {
		return h, nil
	}
	proxy, err := crproxy.NewCRProxy(
		crproxy.WithDefaultRegistry(cfg.DefaultRegistry),
		crproxy.WithLogger(log),
		crproxy.WithRetry(2, time.Second),
		crproxy.WithDomainAlias(map[string]string{
			"docker.io": "registry-1.docker.io",
			"ollama.ai": "registry.ollama.ai",
		}),
		crproxy.WithPathInfoModifyFunc(func(info *crproxy.ImageInfo) *crproxy.ImageInfo {
			if info.Host == "docker.io" && !strings.Contains(info.Name, "/") {
				info.Name = "library/" + info.Name
			}
			if info.Host == "ollama.ai" && !strings.Contains(info.Name, "/") {
				info.Name = "library/" + info.Name
			}
			return info
		}),
	)
	if err != nil {
		return nil, err
	}
	h.proxy = proxy
	return h, nil
}

func (h *DockerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.proxy == nil {
		http.Error(w, "docker proxy is disabled", http.StatusServiceUnavailable)
		return
	}
	target := dockerTarget(r.URL.Path)
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/") && h.cfg.ReferrersStub {
		call := h.stats.Begin("docker")
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.Header().Set("Content-Length", strconv.Itoa(len(emptyOCIIndex)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(emptyOCIIndex))
		}
		call.DoneTarget(target, http.StatusOK, 0, int64(len(emptyOCIIndex)), "", r.RemoteAddr, r.Method, r.URL.Path)
		return
	}
	call := h.stats.Begin("docker")
	rec := &recordingWriter{ResponseWriter: w}
	h.proxy.ServeHTTP(rec, r)
	call.DoneTarget(target, rec.statusCode(), r.ContentLength, rec.bytes, rec.errText(), r.RemoteAddr, r.Method, r.URL.Path)
}

// dockerTarget reduces a registry path to the image reference it addresses so
// the panel can show which images actually use the mirror.
func dockerTarget(path string) string {
	p := strings.TrimPrefix(path, "/v2/")
	if p == path || p == "" {
		return ""
	}
	for _, marker := range []string{"/manifests/", "/blobs/", "/referrers/", "/tags/"} {
		if i := strings.Index(p, marker); i > 0 {
			p = p[:i]
			break
		}
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), "/")
	if p == "" || len(p) > 90 {
		return ""
	}
	return p
}

type recordingWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	err    error
}

func (w *recordingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}

func (w *recordingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *recordingWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *recordingWriter) errText() string {
	if w.err == nil {
		return ""
	}
	return w.err.Error()
}

func (h *DockerHandler) AuthToken(w http.ResponseWriter, r *http.Request) {
	if h.proxy == nil {
		http.Error(w, "docker proxy is disabled", http.StatusServiceUnavailable)
		return
	}
	h.proxy.AuthToken(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
