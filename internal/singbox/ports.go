package singbox

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// ListeningPorts returns the ports the running sing-box process listens on,
// read from ss(8). The list is what the kernel currently exposes, which is not
// necessarily what the configuration files describe: sing-box keeps a removed
// inbound alive until the unit is restarted, and that difference is exactly what
// the panel needs to report.
func (m *Manager) ListeningPorts() ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ss", "-H", "-lntup").CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("读取监听端口失败：%s", message)
	}
	return parseSingboxPorts(string(out)), nil
}

// DriftPorts compares the ports the configuration asks for with the ports the
// running process actually listens on. A port that is configured but missing
// means the unit has not been restarted since the file changed; a port that is
// listening but no longer configured is left over from a removed or parked node.
func DriftPorts(configured, listening []int) (missing, extra []int) {
	have := make(map[int]bool, len(listening))
	for _, port := range listening {
		have[port] = true
	}
	want := make(map[int]bool, len(configured))
	for _, port := range configured {
		want[port] = true
		if !have[port] {
			missing = append(missing, port)
		}
	}
	for _, port := range listening {
		if !want[port] {
			extra = append(extra, port)
		}
	}
	sort.Ints(missing)
	sort.Ints(extra)
	return missing, extra
}

// parseSingboxPorts keeps the listening ports whose owning process is sing-box.
// Header lines, other processes and duplicate entries are dropped.
func parseSingboxPorts(output string) []int {
	seen := map[int]bool{}
	ports := make([]int, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[0] != "tcp" && fields[0] != "udp" {
			continue
		}
		if fields[1] != "LISTEN" && fields[1] != "UNCONN" {
			continue
		}
		if !strings.Contains(line, "sing-box") {
			continue
		}
		addr := fields[4]
		idx := strings.LastIndex(addr, ":")
		if idx < 0 || idx+1 >= len(addr) {
			continue
		}
		port, err := strconv.Atoi(addr[idx+1:])
		if err != nil || port <= 0 || port > 65535 || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

// PortDrift compares the enabled node configuration with the ports the running
// process actually listens on. Supported is false when the probe itself cannot
// run, which keeps a missing ss(8) from looking like a configuration fault.
type PortDrift struct {
	Supported  bool   `json:"supported"`
	Configured []int  `json:"configured"`
	Listening  []int  `json:"listening"`
	Missing    []int  `json:"missing"`
	Extra      []int  `json:"extra"`
	Error      string `json:"error,omitempty"`
}

// PortDrift builds the drift report for the current configuration and process.
// The Clash API port is excluded on both sides: it belongs to the panel switch,
// not to a node, and would otherwise always look like an unexpected listener.
func (m *Manager) PortDrift() PortDrift {
	drift := PortDrift{Supported: true, Configured: []int{}, Listening: []int{}, Missing: []int{}, Extra: []int{}}
	ignore := map[int]bool{}
	if addr := m.ClashAPIAddr(); addr != "" {
		if _, portText, err := net.SplitHostPort(addr); err == nil {
			if port, convErr := strconv.Atoi(portText); convErr == nil {
				ignore[port] = true
			}
		}
	}
	if nodes, err := m.Nodes(); err == nil {
		for _, node := range nodes {
			if !node.Enabled {
				continue
			}
			for _, inbound := range node.Inbounds {
				port := asInt(inbound["listen_port"])
				if port > 0 && !ignore[port] {
					drift.Configured = append(drift.Configured, port)
				}
			}
		}
	} else {
		drift.Error = err.Error()
	}
	drift.Configured = uniquePortList(drift.Configured)
	listening, err := m.ListeningPorts()
	if err != nil {
		drift.Supported = false
		drift.Error = err.Error()
		return drift
	}
	for _, port := range listening {
		if !ignore[port] {
			drift.Listening = append(drift.Listening, port)
		}
	}
	drift.Listening = uniquePortList(drift.Listening)
	drift.Missing, drift.Extra = DriftPorts(drift.Configured, drift.Listening)
	return drift
}

// uniquePortList sorts and de-duplicates a port list in place.
func uniquePortList(ports []int) []int {
	seen := make(map[int]bool, len(ports))
	out := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || port > 65535 || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}
