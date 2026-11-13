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
)

// ListeningPorts returns the ports the running sing-box process listens on.
// It resolves the unit main PID and maps that process' socket inodes to the
// listening entries in /proc/net, because `ss -p` needs privileges this
// service deliberately does not have: the unit keeps CAP_NET_BIND_SERVICE only,
// so ss can list sockets but cannot attribute them to a process.
func (m *Manager) ListeningPorts() ([]int, error) {
	pids, err := m.corePids()
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return []int{}, nil
	}
	inodes := map[uint64]bool{}
	for _, pid := range pids {
		found, err := processSocketInodesHook(pid)
		if err != nil {
			return nil, err
		}
		for inode := range found {
			inodes[inode] = true
		}
	}
	ports := make([]int, 0, 8)
	for _, source := range procNetSources {
		raw, err := os.ReadFile(filepath.Join(procRoot, source.path))
		if err != nil {
			continue
		}
		for inode, port := range parseProcNet(string(raw), source.proto) {
			if inodes[inode] {
				ports = append(ports, port)
			}
		}
	}
	return uniquePortList(ports), nil
}

type procNetSource struct {
	path  string
	proto string
}

var procNetSources = []procNetSource{
	{"net/tcp", "tcp"},
	{"net/tcp6", "tcp"},
	{"net/udp", "udp"},
	{"net/udp6", "udp"},
}

// procRoot is the mount point of procfs. Tests point it at a fixture tree.
var procRoot = "/proc"

// processSocketInodesHook is replaced in tests so a fixture tree can stand in
// for a live process.
var processSocketInodesHook = processSocketInodes

// corePids returns the main PID of the unit that runs sing-box. A stopped unit
// yields no PID, which is not an error: it simply has no listening sockets.
func (m *Manager) corePids() ([]int, error) {
	return unitMainPidsHook(m.service)
}

// unitMainPidsHook resolves the unit main PID; tests replace it.
var unitMainPidsHook = unitMainPids

func unitMainPids(service string) ([]int, error) {
	if service == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "--property=MainPID", "--value", service).Output()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("读取 sing-box 进程失败：%s", message)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if convErr != nil || pid <= 0 {
		return nil, nil
	}
	return []int{pid}, nil
}

// processSocketInodes lists the socket inodes a process holds open.
func processSocketInodes(pid int) (map[uint64]bool, error) {
	dir := fmt.Sprintf("%s/%d/fd", procRoot, pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取 sing-box 进程端口失败：%v", err)
	}
	inodes := make(map[uint64]bool, len(entries))
	for _, entry := range entries {
		target, err := os.Readlink(dir + "/" + entry.Name())
		if err != nil {
			continue
		}
		if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inode, convErr := strconv.ParseUint(target[len("socket:["):len(target)-1], 10, 64)
		if convErr != nil {
			continue
		}
		inodes[inode] = true
	}
	return inodes, nil
}

// parseProcNet maps socket inode to listening port for one /proc/net table.
// TCP entries must be in LISTEN state; UDP entries are kept only when they are
// unconnected, which is how a UDP server socket appears.
func parseProcNet(raw, proto string) map[uint64]int {
	out := map[uint64]int{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[0] == "sl" {
			continue
		}
		local, state, inodeText := fields[1], fields[3], fields[9]
		switch proto {
		case "tcp":
			if state != "0A" {
				continue
			}
		case "udp":
			if state != "07" {
				continue
			}
		default:
			continue
		}
		inode, err := strconv.ParseUint(inodeText, 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		idx := strings.LastIndex(local, ":")
		if idx < 0 || idx+1 >= len(local) {
			continue
		}
		port, err := strconv.ParseUint(local[idx+1:], 16, 16)
		if err != nil || port == 0 {
			continue
		}
		out[inode] = int(port)
	}
	return out
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

// PortDrift compares the enabled node configuration with the ports the running
// process actually listens on. Supported is false when the probe itself cannot
// run, which keeps a missing /proc or a stopped unit from looking like a
// configuration fault.
type PortDrift struct {
	Supported  bool   `json:"supported"`
	Running    bool   `json:"running"`
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
