package cnagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// dockerSocketPath is where the docker daemon listens by default on the CN
// machines; the agent only ever talks to docker over the local unix socket.
const dockerSocketPath = "/var/run/docker.sock"

// coolifyLabelPrefix marks containers managed by Coolify: any label key with
// this prefix flags the workload as a Coolify app.
const coolifyLabelPrefix = "coolify."

// containersPath is the Docker Engine API endpoint that lists containers.
// The unversioned path lets the daemon negotiate the API version, and with
// no query parameters it lists running containers (the docker ps default),
// which is what the status report wants.
const containersPath = "/containers/json"

// dockerContainer is the subset of the Docker Engine API ContainerSummary the
// status report needs. Metadata only: no env, no config, no command, no
// mounts (spec §8.2 forbids collecting container configuration).
type dockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Created int64             `json:"Created"` // unix seconds
	Labels  map[string]string `json:"Labels"`
}

// DiscoverWorkloads enumerates the machine's docker containers over the unix
// socket at socketPath and maps them to wire workloads: the name is Names[0]
// with its leading slash trimmed (the container ID when the container has no
// name), plus image, state, start time as RFC3339 UTC, and a Coolify flag
// when any label key carries the coolify. prefix. httpClient is an injection
// point for tests; a nil client makes the function build one that dials
// socketPath directly. A missing socket means the machine has no docker and
// returns (nil, nil): discovery did not fail, there is just nothing to
// report.
func DiscoverWorkloads(ctx context.Context, socketPath string, httpClient *http.Client) ([]Workload, error) {
	if _, err := os.Stat(socketPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("workload: stat docker socket %s: %w", socketPath, err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Transport: &http.Transport{DialContext: dialUnix(socketPath)}}
	}

	// The URL host is a placeholder; the transport dials the unix socket
	// regardless of the host in the URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+containersPath, nil)
	if err != nil {
		return nil, fmt.Errorf("workload: build request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workload: list containers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("workload: list containers: unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("workload: read container list: %w", err)
	}
	var containers []dockerContainer
	if err := json.Unmarshal(data, &containers); err != nil {
		return nil, fmt.Errorf("workload: parse container list: %w", err)
	}

	workloads := make([]Workload, 0, len(containers))
	for _, c := range containers {
		workloads = append(workloads, Workload{
			Name:    containerName(c),
			Image:   c.Image,
			State:   c.State,
			Started: formatContainerStart(c.Created),
			Coolify: hasCoolifyLabel(c.Labels),
		})
	}
	return workloads, nil
}

// dialUnix returns a DialContext that always dials the unix socket at
// socketPath, ignoring the network and address carried in the request URL.
func dialUnix(socketPath string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
}

// containerName picks the primary container name: Names[0] with the leading
// slash trimmed; a container without a usable name falls back to its ID.
func containerName(c dockerContainer) string {
	if len(c.Names) > 0 {
		if name := strings.TrimPrefix(c.Names[0], "/"); name != "" {
			return name
		}
	}
	return c.ID
}

// hasCoolifyLabel reports whether any label key carries the coolify prefix.
func hasCoolifyLabel(labels map[string]string) bool {
	for key := range labels {
		if strings.HasPrefix(key, coolifyLabelPrefix) {
			return true
		}
	}
	return false
}

// formatContainerStart renders the container creation timestamp as RFC3339
// UTC, matching the other timestamps in the heartbeat report; a zero
// timestamp stays empty.
func formatContainerStart(created int64) string {
	if created == 0 {
		return ""
	}
	return time.Unix(created, 0).UTC().Format(time.RFC3339)
}
