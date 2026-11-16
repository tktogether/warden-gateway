//go:build !linux

package cnagent

// diskUsedPct is only implemented for the Linux machines the CN agent runs
// on; elsewhere (Windows builds/tests) it reports nothing and the metric
// stays zeroed.
func diskUsedPct(path string) (float64, error) {
	return 0, nil
}
