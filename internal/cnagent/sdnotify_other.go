//go:build !linux

package cnagent

// SDNotify is a no-op outside Linux: systemd's notify socket does not exist
// there, and the agent only runs under systemd on Linux in production (Windows
// is a build/test target, mirroring the sysmetrics split).
func SDNotify(state string) error {
	return nil
}
