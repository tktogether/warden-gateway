//go:build !linux

package panel

func diskUsage(path string) (uint64, uint64, uint64, error) {
	return 0, 0, 0, nil
}
