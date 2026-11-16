//go:build linux

package cnagent

import (
	"net"
)

// sdNotifySocket is the systemd notify socket address. The leading @ makes
// net.Dial use the Linux abstract namespace (Go maps it to "\0/run/systemd/
// notify"), exactly how sd_notify(3) reaches /run/systemd/notify. It is a
// variable so tests can point it at a real unixgram listener.
var sdNotifySocket = "@/run/systemd/notify"

// SDNotify sends one sd_notify state string (for example "WATCHDOG=1") to the
// systemd notify socket over a datagram unix socket and closes it. Errors are
// returned, never swallowed here: production callers (the notifyFn hook) drop
// them, because a missing socket — running outside systemd, or in tests —
// must never break a cycle.
func SDNotify(state string) error {
	conn, err := net.Dial("unixgram", sdNotifySocket)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
