//go:build linux

package cnagent

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestSDNotifyDeliversState exercises ruling 3 against a real unixgram
// listener: sdNotifySocket is pointed at a filesystem socket inside a temp
// directory (the production @/run/systemd/notify abstract socket cannot be
// captured in a test) and the state string must arrive byte-for-byte.
func TestSDNotifyDeliversState(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "notify.sock")
	pc, err := net.ListenPacket("unixgram", sock)
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	old := sdNotifySocket
	sdNotifySocket = sock
	t.Cleanup(func() { sdNotifySocket = old })

	if err := SDNotify(watchdogPing); err != nil {
		t.Fatalf("SDNotify: %v", err)
	}

	buf := make([]byte, 128)
	if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read from listener: %v", err)
	}
	if got := string(buf[:n]); got != watchdogPing {
		t.Errorf("SDNotify delivered %q, want %q", got, watchdogPing)
	}
}
