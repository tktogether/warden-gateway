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

// HostSample is one 15s point of host level telemetry.
type HostSample struct {
	At             int64   `json:"at"`
	CPUPercent     float64 `json:"cpu_percent"`
	Load1          float64 `json:"load1"`
	Load5          float64 `json:"load5"`
	Load15         float64 `json:"load15"`
	MemTotal       uint64  `json:"mem_total"`
	MemUsed        uint64  `json:"mem_used"`
	MemAvailable   uint64  `json:"mem_available"`
	SwapTotal      uint64  `json:"swap_total"`
	SwapUsed       uint64  `json:"swap_used"`
	NetRxRate      int64   `json:"net_rx_rate"`
	NetTxRate      int64   `json:"net_tx_rate"`
	DiskReadRate   int64   `json:"disk_read_rate"`
	DiskWriteRate  int64   `json:"disk_write_rate"`
	TCPConnections int     `json:"tcp_connections"`
	Established    int     `json:"established"`
	TimeWait       int     `json:"time_wait"`
	Listen         int     `json:"listen"`
	Processes      int     `json:"processes"`
	Conntrack      int     `json:"conntrack"`
}

// IfaceRate is a per interface throughput snapshot.
type IfaceRate struct {
	Name   string `json:"name"`
	RxRate int64  `json:"rx_rate"`
	TxRate int64  `json:"tx_rate"`
}

type hostMetrics struct {
	mu         sync.Mutex
	samples    []HostSample
	max        int
	prevAt     time.Time
	prevCPU    cpuStat
	prevNet    map[string]netStat
	prevDisk   diskStat
	havePrev   bool
	lastIfaces []IfaceRate
}

type cpuStat struct{ total, idle uint64 }
type netStat struct{ rx, tx uint64 }
type diskStat struct{ read, write uint64 }

func newHostMetrics(max int) *hostMetrics {
	if max <= 0 {
		max = 5760
	}
	return &hostMetrics{max: max, prevNet: map[string]netStat{}}
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
	if ifaces, err := readNet(); err == nil {
		rates := make([]IfaceRate, 0, len(ifaces))
		for name, v := range ifaces {
			prev := m.prevNet[name]
			rates = append(rates, IfaceRate{
				Name:   name,
				RxRate: rate(prev.rx, v.rx, m.prevAt, now),
				TxRate: rate(prev.tx, v.tx, m.prevAt, now),
			})
			s.NetRxRate += rate(prev.rx, v.rx, m.prevAt, now)
			s.NetTxRate += rate(prev.tx, v.tx, m.prevAt, now)
		}
		m.prevNet = ifaces
		sortIfaces(rates)
		m.lastIfaces = rates
	}
	if v, err := readDisk(); err == nil {
		s.DiskReadRate, s.DiskWriteRate = rate(m.prevDisk.read, v.read, m.prevAt, now), rate(m.prevDisk.write, v.write, m.prevAt, now)
		m.prevDisk = v
	}
	if l1, l5, l15, err := readLoad(); err == nil {
		s.Load1, s.Load5, s.Load15 = l1, l5, l15
	}
	if mem, err := readMemory(); err == nil {
		s.MemTotal, s.MemUsed, s.MemAvailable = mem.total, mem.used, mem.available
		s.SwapTotal, s.SwapUsed = mem.swapTotal, mem.swapUsed
	}
	if st, err := countTCP(); err == nil {
		s.TCPConnections = st.total
		s.Established = st.established
		s.TimeWait = st.timeWait
		s.Listen = st.listen
	}
	if n, err := countProcesses(); err == nil {
		s.Processes = n
	}
	s.Conntrack = readConntrack()
	m.prevAt = now
	m.havePrev = true
	m.samples = append(m.samples, s)
	if len(m.samples) > m.max {
		m.samples = m.samples[len(m.samples)-m.max:]
	}
	return s
}

// Ifaces returns the interface throughput of the most recent sample.
func (m *hostMetrics) Ifaces() []IfaceRate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]IfaceRate, len(m.lastIfaces))
	copy(out, m.lastIfaces)
	return out
}

func sortIfaces(list []IfaceRate) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			if list[j].RxRate+list[j].TxRate > list[j-1].RxRate+list[j-1].TxRate {
				list[j], list[j-1] = list[j-1], list[j]
				continue
			}
			break
		}
	}
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

type memStat struct{ total, used, available, swapTotal, swapUsed uint64 }

func readMemory() (memStat, error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memStat{}, err
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
		return memStat{}, os.ErrInvalid
	}
	if available > total {
		available = total
	}
	swapTotal := values["SwapTotal"]
	swapFree := values["SwapFree"]
	swapUsed := uint64(0)
	if swapTotal > swapFree {
		swapUsed = swapTotal - swapFree
	}
	return memStat{total: total, used: total - available, available: available, swapTotal: swapTotal, swapUsed: swapUsed}, nil
}

func readLoad() (float64, float64, float64, error) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return 0, 0, 0, os.ErrInvalid
	}
	l1, err1 := strconv.ParseFloat(fields[0], 64)
	l5, err2 := strconv.ParseFloat(fields[1], 64)
	l15, err3 := strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, os.ErrInvalid
	}
	return l1, l5, l15, nil
}

func readLoad1() (float64, error) {
	l1, _, _, err := readLoad()
	return l1, err
}

func readNet() (map[string]netStat, error) {
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	out := map[string]netStat{}
	for _, line := range strings.Split(string(raw), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		fields := strings.Fields(line[idx+1:])
		if name == "" || len(fields) < 16 {
			continue
		}
		var st netStat
		if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			st.rx = v
		}
		if v, err := strconv.ParseUint(fields[8], 10, 64); err == nil {
			st.tx = v
		}
		out[name] = st
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

type tcpCounts struct {
	total, established, timeWait, listen int
}

func countTCP() (tcpCounts, error) {
	var out tcpCounts
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[0] == "sl" {
				continue
			}
			out.total++
			switch strings.ToUpper(fields[3]) {
			case "01":
				out.established++
			case "06":
				out.timeWait++
			case "0A":
				out.listen++
			}
		}
	}
	if out.total == 0 {
		if _, err := os.Stat("/proc/net/tcp"); err != nil {
			return out, err
		}
	}
	return out, nil
}

func readConntrack() int {
	raw, err := os.ReadFile("/proc/sys/net/netfilter/nf_conntrack_count")
	if err != nil {
		return -1
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return -1
	}
	return v
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
		"cpus":           runtime.NumCPU(),
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
	return out
}

var _ = filepath.Join
