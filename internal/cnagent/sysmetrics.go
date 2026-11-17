package cnagent

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// System metrics are sampled from /proc on every heartbeat cycle (spec §5.2:
// disk/memory/load in Report.System). Every sampler fails soft: when /proc is
// absent (e.g. building or testing on Windows) the field stays at its zero
// value and the cycle continues, which is why sampleSystem returns no error.

// sampleSystem reads the machine's disk/memory/load sample for the heartbeat
// report. disk_used_pct is the usage of the / filesystem, mem_used_pct is
// derived from MemAvailable (the value "free" memory tools report on modern
// kernels) and load1 comes from /proc/loadavg.
func sampleSystem() System {
	var s System
	if pct, err := diskUsedPct("/"); err == nil {
		s.DiskUsedPct = pct
	}
	if pct, err := memUsedPct(); err == nil {
		s.MemUsedPct = pct
	}
	if load, err := loadAverage1(); err == nil {
		s.Load1 = load
	}
	return s
}

// memUsedPct parses MemTotal and MemAvailable from /proc/meminfo and returns
// (total-available)/total as a percentage.
func memUsedPct() (float64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	total, err := meminfoField(string(data), "MemTotal")
	if err != nil {
		return 0, err
	}
	avail, err := meminfoField(string(data), "MemAvailable")
	if err != nil {
		return 0, err
	}
	if total <= 0 {
		return 0, fmt.Errorf("meminfo: MemTotal %d <= 0", total)
	}
	return float64(total-avail) / float64(total) * 100, nil
}

// meminfoField extracts the value (in kB) of one "Field: 123 kB" line.
func meminfoField(meminfo, field string) (int64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != field {
			continue
		}
		var value int64
		if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%d", &value); err != nil {
			return 0, fmt.Errorf("meminfo: parse %s: %w", field, err)
		}
		return value, nil
	}
	return 0, fmt.Errorf("meminfo: %s not found", field)
}

// loadAverage1 returns the 1-minute load average from /proc/loadavg.
func loadAverage1() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("loadavg: empty")
	}
	var load float64
	if _, err := fmt.Sscanf(fields[0], "%g", &load); err != nil {
		return 0, fmt.Errorf("loadavg: parse %q: %w", fields[0], err)
	}
	return load, nil
}

// bootInfo returns the machine boot time as an RFC3339 UTC timestamp plus the
// uptime in seconds, parsed from the btime line of /proc/stat. When the value
// is unavailable it returns empty strings and 0 so the report simply omits
// them.
func bootInfo() (string, int64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return "", 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, ok := strings.Cut(line, " ")
		if !ok || strings.TrimSpace(name) != "btime" {
			continue
		}
		var secs int64
		if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%d", &secs); err != nil || secs <= 0 {
			return "", 0
		}
		boot := time.Unix(secs, 0).UTC()
		return boot.Format(time.RFC3339), int64(time.Since(boot).Seconds())
	}
	return "", 0
}
