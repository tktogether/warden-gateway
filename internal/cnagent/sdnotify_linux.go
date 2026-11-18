//go:build linux

package cnagent

import (
	"net"
	"os"
	"strings"
)

// sdNotifySocketEnv is the variable systemd exports with the address of its
// notification socket. sd_notify(3) reads it; the previous implementation
// hard-coded "@/run/systemd/notify" instead, which only works on builds that
// bind the abstract namespace. On a systemd that binds the filesystem path
// (Ubuntu 22.04 / systemd 249 does) every watchdog ping was lost and the unit
// was SIGABRTed every WatchdogSec while the agent itself was healthy.
const sdNotifySocketEnv = "NOTIFY_SOCKET"

// sdNotifySocketDefault is the fallback address used when NOTIFY_SOCKET is not
// set (running outside systemd, or an old unit file). It is the real
// filesystem path systemd binds, not an abstract name.
const sdNotifySocketDefault = "/run/systemd/notify"

// sdNotifySocket overrides the resolved address. Production leaves it empty so
// notifySocketAddr() reads NOTIFY_SOCKET; tests set it to a temporary socket.
var sdNotifySocket string

// notifySocketAddr resolves the notification socket address. systemd passes an
// absolute filesystem path on standard builds, and an abstract-namespace name
// prefixed with '@' on others; Go's net package wants the abstract name with a
// NUL byte in front, which is what the caller applies.
func notifySocketAddr() string {
	if override := strings.TrimSpace(sdNotifySocket); override != "" {
		return override
	}
	if env := strings.TrimSpace(os.Getenv(sdNotifySocketEnv)); env != "" {
		return env
	}
	return sdNotifySocketDefault
}

// dialNotifySocket opens the datagram connection to the notify socket,
// translating systemd's '@' abstract-namespace prefix into the leading NUL
// byte Go expects.
func dialNotifySocket() (net.Conn, error) {
	addr := notifySocketAddr()
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:]
	}
	return net.Dial("unixgram", addr)
}

// SDNotify sends one sd_notify state string (for example "WATCHDOG=1") to the
// systemd notify socket over a datagram unix socket and closes it. Errors are
// returned, never swallowed here: production callers (the notifyFn hook) log
// them, because a silently lost ping makes systemd kill a healthy agent on
// every WatchdogSec.
func SDNotify(state string) error {
	conn, err := dialNotifySocket()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
