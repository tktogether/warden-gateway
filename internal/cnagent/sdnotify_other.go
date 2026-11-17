//go:build !linux

package cnagent

// SDNotify is a no-op outside Linux: the abstract-namespace sd_notify socket
// does not exist there (the agent runs under systemd on Linux in production;
// Windows is only a build/test target, mirroring the sysmetrics split).
func SDNotify(state string) error {
	return nil
}
