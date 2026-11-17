package cnagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// upgradeHTTPTimeout bounds one release download. Binary downloads can be
// tens of megabytes on slow CN links — this is the one place where a longer
// timeout than the 8s heartbeat budget is correct.
const upgradeHTTPTimeout = 60 * time.Second

// supportedUpgradeArchs is the release-asset allow-list: the gateway only
// publishes warden-cn-agent-linux-{amd64|arm64|arm}.
var supportedUpgradeArchs = map[string]bool{
	"amd64": true,
	"arm64": true,
	"arm":   true,
}

// SelfUpgrade installs a new agent binary in place:
//
//  1. download {Origin}/api/agent/binary/{version}/warden-cn-agent-linux-{arch}
//     and its same-name .sha256 sidecar from the gateway that enrolled this
//     machine (bare hex hash, possibly with trailing whitespace/newline). The
//     gateway serves its own cached release build, so an upgrade never depends
//     on this machine reaching GitHub;
//  2. verify the sha256 (hex compare) BEFORE touching anything on disk —
//     a failed download or checksum mismatch leaves the local state
//     completely untouched;
//  3. write selfPath+".new" (0755), rename the current binary to
//     selfPath+".prev" (kept for the systemd ExecStartPre rollback guard),
//     then rename .new onto selfPath;
//  4. on any failure after the download, remove .new and restore the
//     original state.
//
// arch is passed explicitly (production callers pass runtime.GOARCH, tests
// inject a fixed value); version is used verbatim in the URL as the
// heartbeat's target_version delivered it (e.g. "v1.6.0"), minus its "v"
// prefix, matching the gateway's cached-build path. A nil httpClient gets
// cfg.HTTPClient(upgradeHTTPTimeout), so self-signed gateways marked
// insecure_tls can serve upgrades too.
func SelfUpgrade(ctx context.Context, cfg *Config, version, arch, selfPath string, httpClient *http.Client) error {
	if cfg == nil {
		return fmt.Errorf("cnagent upgrade: config is nil")
	}
	if !supportedUpgradeArchs[arch] {
		return fmt.Errorf("cnagent upgrade: unsupported arch %q (want amd64, arm64 or arm)", arch)
	}
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("cnagent upgrade: target version is required")
	}
	// The nil fallback must also honor insecure_tls: a self-signed gateway
	// serves both the binary and its sidecar over the same certificate. The
	// download budget stays the dedicated 60s upgrade timeout.
	if httpClient == nil {
		httpClient = cfg.HTTPClient(upgradeHTTPTimeout)
	}

	base := strings.TrimRight(cfg.Origin, "/") +
		"/api/agent/binary/" + strings.TrimPrefix(strings.TrimSpace(version), "v")
	asset := "warden-cn-agent-linux-" + arch

	body, err := downloadReleaseFile(ctx, httpClient, base+"/"+asset)
	if err != nil {
		return fmt.Errorf("cnagent upgrade: %w", err)
	}
	sidecar, err := downloadReleaseFile(ctx, httpClient, base+"/"+asset+".sha256")
	if err != nil {
		return fmt.Errorf("cnagent upgrade: %w", err)
	}

	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	want := strings.TrimSpace(string(sidecar))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("cnagent upgrade: checksum mismatch for %s: got %s, want %s (binary left untouched)", asset, got, want)
	}

	newPath := selfPath + ".new"
	prevPath := selfPath + ".prev"

	// From here on every failure path must remove .new and restore the
	// original state before returning (Review Focus #5).
	if err := os.WriteFile(newPath, body, 0o755); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("cnagent upgrade: write %s: %w", newPath, err)
	}
	if err := os.Rename(selfPath, prevPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("cnagent upgrade: stage previous binary as %s: %w", prevPath, err)
	}
	if err := os.Rename(newPath, selfPath); err != nil {
		// Activation failed: put the original binary back where systemd
		// expects it, then clean up.
		restoreErr := os.Rename(prevPath, selfPath)
		_ = os.Remove(newPath)
		if restoreErr != nil {
			return fmt.Errorf("cnagent upgrade: activate new binary: %w (and restoring %s from %s failed: %v)", err, selfPath, prevPath, restoreErr)
		}
		return fmt.Errorf("cnagent upgrade: activate new binary (original restored): %w", err)
	}

	// .prev is intentionally kept: Task 14's ExecStartPre rollback guard
	// consumes it to recover from a binary that fails to start.
	return nil
}

// downloadReleaseFile fetches url and returns the body, treating any non-2xx
// status as an error. SelfUpgrade resolves a nil client through
// Config.HTTPClient first (so insecure_tls applies); the fallback here only
// guards future direct callers of this unexported helper.
func downloadReleaseFile(ctx context.Context, httpClient *http.Client, url string) ([]byte, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: upgradeHTTPTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("download %s: read body: %w", url, err)
	}
	return data, nil
}
