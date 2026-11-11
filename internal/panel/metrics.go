package panel

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HostSample struct {
	At             int64   `json:"at"`
	CPUPercent     float64 `json:"cpu_percent"`
	Load1          float64 `json:"load1"`
	MemTotal       uint64  `json:"mem_total"`
	MemUsed        uint64  `json:"mem_used"`
	MemAvailable   uint64  `json:"mem_available"`
	NetRxRate      int64   `json:"net_rx_rate"`
	NetTxRate      int64   `json:"net_tx_rate"`
	DiskReadRate   int64   `json:"disk_read_rate"`
	DiskWriteRate  int64   `json:"disk_write_rate"`
	TCPConnections int     `json:"tcp_connections"`
	Processes      int     `json:"processes"`
}

type hostMetrics struct {
	mu       sync.Mutex
	samples  []HostSample
	max      int
	prevAt   time.Time
	prevCPU  cpuStat
	prevNet  netStat
	prevDisk diskStat
	havePrev bool
}

type cpuStat struct{ total, idle uint64 }
type netStat struct{ rx, tx uint64 }
type diskStat struct{ read, write uint64 }

func newHostMetrics(max int) *hostMetrics {
	if max <= 0 {
		max = 5760
	}
	return &hostMetrics{max: max}
}

func (m *hostMetrics) Add(s HostSample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, s)
	if len(m.samples) > m.max {
		m.samples = m.samples[len(m.samples)-m.max:]
	}
}

func (m *hostMetrics) Sample() HostSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	s := HostSample{At: now.Unix()}
	if v, err := readCPUTotal(); err == nil {
		s.CPUPercent = cpuPercent(m.prevCPU, v, m.havePrev)
		m.prevCPU = v
	}
	if v, err := readNet(); err == nil {
		s.NetRxRate, s.NetTxRate = rate(m.prevNet.rx, v.rx, m.prevAt, now), rate(m.prevNet.tx, v.tx, m.prevAt, now)
		m.prevNet = v
	}
	if v, err := readDisk(); err == nil {
		s.DiskReadRate, s.DiskWriteRate = rate(m.prevDisk.read, v.read, m.prevAt, now), rate(m.prevDisk.write, v.write, m.prevAt, now)
		m.prevDisk = v
	}
	if v, err := readLoad1(); err == nil {
		s.Load1 = v
	}
	if total, used, available, err := readMemory(); err == nil {
		s.MemTotal, s.MemUsed, s.MemAvailable = total, used, available
	}
	if n, err := countTCP(); err == nil {
		s.TCPConnections = n
	}
	if n, err := countProcesses(); err == nil {
		s.Processes = n
	}
	m.prevAt = now
	m.havePrev = true
	m.samples = append(m.samples, s)
	if len(m.samples) > m.max {
		m.samples = m.samples[len(m.samples)-m.max:]
	}
	return s
}

func (m *hostMetrics) Series(since int64) []HostSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]HostSample, 0, len(m.samples))
	for _, s := range m.samples {
		if since == 0 || s.At >= since {
			out = append(out, s)
		}
	}
	return out
}

func (m *hostMetrics) Latest() (HostSample, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.samples) == 0 {
		return HostSample{}, false
	}
	return m.samples[len(m.samples)-1], true
}

func rate(prev, cur uint64, prevAt, now time.Time) int64 {
	if prevAt.IsZero() || cur < prev {
		return 0
	}
	secs := now.Sub(prevAt).Seconds()
	if secs <= 0 {
		return 0
	}
	return int64(float64(cur-prev) / secs)
}

func cpuPercent(prev, cur cpuStat, have bool) float64 {
	if !have || cur.total <= prev.total || cur.idle < prev.idle {
		return 0
	}
	total := float64(cur.total - prev.total)
	idle := float64(cur.idle - prev.idle)
	return clampPercent((total - idle) / total * 100)
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func readCPUTotal() (cpuStat, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuStat{}, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 4 {
			return cpuStat{}, os.ErrInvalid
		}
		var total, idle uint64
		for i, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return cpuStat{}, err
			}
			total += v
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return cpuStat{total: total, idle: idle}, nil
	}
	return cpuStat{}, os.ErrNotExist
}

func readMemory() (uint64, uint64, uint64, error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = v * 1024
		}
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return 0, 0, 0, os.ErrInvalid
	}
	if available > total {
		available = total
	}
	return total, total - available, available, nil
}

func readLoad1() (float64, error) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, os.ErrInvalid
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readNet() (netStat, error) {
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return netStat{}, err
	}
	var out netStat
	for _, line := range strings.Split(string(raw), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 16 {
			continue
		}
		rx, err1 := strconv.ParseUint(fields[0], 10, 64)
		tx, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 == nil {
			out.rx += rx
		}
		if err2 == nil {
			out.tx += tx
		}
	}
	return out, nil
}

func readDisk() (diskStat, error) {
	raw, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return diskStat{}, err
	}
	var out diskStat
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") {
			continue
		}
		read, err1 := strconv.ParseUint(fields[5], 10, 64)
		write, err2 := strconv.ParseUint(fields[9], 10, 64)
		if err1 == nil {
			out.read += read * 512
		}
		if err2 == nil {
			out.write += write * 512
		}
	}
	return out, nil
}

func countTCP() (int, error) {
	n := 0
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "sl") || strings.TrimSpace(line) == "" {
				continue
			}
			n++
		}
	}
	return n, nil
}

func countProcesses() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && isDigits(e.Name()) {
			n++
		}
	}
	return n, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func readUptime() uint64 {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return uint64(v)
}

func hostInfo() map[string]interface{} {
	hostname, _ := os.Hostname()
	out := map[string]interface{}{
		"hostname":       hostname,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"go":             runtime.Version(),
		"uptime_seconds": readUptime(),
	}
	if raw, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				out["os"] = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			}
		}
	}
	if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		out["kernel"] = strings.TrimSpace(string(raw))
	}
	if total, used, free, err := diskUsage("/"); err == nil {
		out["disk"] = map[string]interface{}{"total": total, "used": used, "free": free}
	}
	if data, err := os.ReadFile(filepath.Join("/proc", "meminfo")); err == nil {
		_ = data
	}
	return out
}
