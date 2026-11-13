package singbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ListeningPorts returns every TCP and UDP port a listener is bound to on the
// host. The unit keeps CAP_NET_BIND_SERVICE only, so the process cannot read
// /proc/<pid>/fd and "ss -p" cannot attribute sockets to a process; the plain
// listener table is readable without any added privilege.
func (m *Manager) ListeningPorts() ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ss", "-H", "-lntu").Output()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("读取监听端口失败：%s", message)
	}
	return parseListenerPorts(string(out)), nil
}

// parseListenerPorts extracts the local port of every listener listed by
// "ss -H -lntu". Fields are netid, state, queues, local address; the port
// follows the last colon, so IPv6 and scoped addresses parse the same way.
func parseListenerPorts(raw string) []int {
	ports := make([]int, 0, 16)
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[0] != "tcp" && fields[0] != "udp" {
			continue
		}
		local := fields[4]
		idx := strings.LastIndex(local, ":")
		if idx < 0 || idx+1 >= len(local) {
			continue
		}
		port, err := strconv.Atoi(local[idx+1:])
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		ports = append(ports, port)
	}
	return uniquePortList(ports)
}

// PortDrift compares the enabled node configuration with what the host listens
// on, and flags node files the running process has not loaded yet.
//
// Listening holds only configured ports that are actually bound, so the report
// stays free of unrelated host listeners. A port bound by another program reads
// as listening; that is the price of not being able to attribute sockets to a
// process without extra privileges.
type PortDrift struct {
	Supported  bool   `json:"supported"`
	Running    bool   `json:"running"`
	Configured []int  `json:"configured"`
	Listening  []int  `json:"listening"`
	Missing    []int  `json:"missing"`
	Pending    bool   `json:"pending"`
	ChangedAt  string `json:"changed_at,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// PortDrift builds the report for the current configuration and process. The
// Clash API port is excluded: it belongs to the panel switch, not to a node.
func (m *Manager) PortDrift() PortDrift {
	drift := PortDrift{Supported: true, Configured: []int{}, Listening: []int{}, Missing: []int{}}
	drift.Running = m.Running()
	ignore := map[int]bool{}
	if addr := m.ClashAPIAddr(); addr != "" {
		if idx := strings.LastIndex(addr, ":"); idx > 0 {
			if port, convErr := strconv.Atoi(addr[idx+1:]); convErr == nil {
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
	if !drift.Running {
		return drift
	}
	bound, err := m.ListeningPorts()
	if err != nil {
		drift.Supported = false
		drift.Error = err.Error()
		return drift
	}
	for _, port := range drift.Configured {
		if containsPort(bound, port) && !ignore[port] {
			drift.Listening = append(drift.Listening, port)
		}
	}
	drift.Listening = uniquePortList(drift.Listening)
	drift.Missing = missingPorts(drift.Configured, drift.Listening)
	drift.Pending, drift.ChangedAt, drift.StartedAt = m.pendingRestart()
	return drift
}

// pendingRestart reports node files modified after the unit last started, which
// means the running process has not loaded them yet.
func (m *Manager) pendingRestart() (bool, string, string) {
	started, err := unitStartedAtHook(m.service)
	if err != nil || started.IsZero() {
		return false, "", ""
	}
	changed := m.configChangedAt()
	if changed.IsZero() || !changed.After(started.Add(2*time.Second)) {
		return false, "", ""
	}
	return true, changed.UTC().Format(time.RFC3339), started.UTC().Format(time.RFC3339)
}

// configChangedAt returns the newest modification time of the node files and the
// main configuration.
func (m *Manager) configChangedAt() time.Time {
	var newest time.Time
	mark := func(path string) {
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	mark(m.configPath)
	entries, err := os.ReadDir(m.confDir)
	if err != nil {
		return newest
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, enabledExt) && !strings.HasSuffix(name, disabledExt) {
			continue
		}
		mark(filepath.Join(m.confDir, name))
	}
	return newest
}

// unitStartedAtHook resolves the unit activation time; tests replace it.
var unitStartedAtHook = unitStartedAt

// unitStartedAt reads the activation timestamp as seconds since the epoch, so no
// locale or date format parsing is involved.
func unitStartedAt(service string) (time.Time, error) {
	if service == "" {
		return time.Time{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveEnterTimestamp", "--value", service).Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("读取 sing-box 启动时间失败：%v", err)
	}
	seconds, convErr := parseUnitTimestamp(strings.TrimSpace(string(out)))
	if convErr != nil || seconds <= 0 {
		return time.Time{}, nil
	}
	return time.Unix(seconds, 0), nil
}

// parseUnitTimestamp accepts a plain epoch value or the human readable form
// systemd prints when --timestamp is not honored.
func parseUnitTimestamp(text string) (int64, error) {
	if text == "" {
		return 0, nil
	}
	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		return value, nil
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 MST"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Unix(), nil
		}
	}
	return 0, fmt.Errorf("无法解析服务启动时间：%s", text)
}

// missingPorts lists the ports in want that are absent from have.
func missingPorts(want, have []int) []int {
	missing := make([]int, 0, len(want))
	for _, port := range want {
		if !containsPort(have, port) {
			missing = append(missing, port)
		}
	}
	sort.Ints(missing)
	return missing
}

func containsPort(ports []int, port int) bool {
	for _, candidate := range ports {
		if candidate == port {
			return true
		}
	}
	return false
}

// uniquePortList sorts and de-duplicates a port list.
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
