//go:build linux

package cnagent

import (
	"fmt"
	"syscall"
)

// diskUsedPct returns the used/total ratio of the filesystem at path as a
// percentage (same statfs approach as internal/panel/disk_linux.go).
func diskUsedPct(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	total := st.Blocks * uint64(st.Bsize)
	if total == 0 {
		return 0, fmt.Errorf("statfs %s: zero size", path)
	}
	used := total - st.Bfree*uint64(st.Bsize)
	return float64(used) / float64(total) * 100, nil
}
