//go:build linux

package cnagent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSDNotifyDeliversState exercises ruling 3 against a real unixgram
// listener: sdNotifySocket is pointed at a filesystem socket inside a temp
// directory (the production notify socket cannot be captured in a test) and
// the state string must arrive byte-for-byte.
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

// TestSDNotifyUsesNotifySocketEnv pins the production bug this file fixes: the
// code used to dial a hard-coded abstract address ("@/run/systemd/notify")
// while systemd actually binds the filesystem path it exports in
// NOTIFY_SOCKET. Every watchdog ping was therefore lost, and systemd SIGABRTed
// a perfectly healthy agent every WatchdogSec (measured on a live CN machine:
// 990 kills in one day). The socket must come from the environment.
func TestSDNotifyUsesNotifySocketEnv(t *testing.T) {
	// Leave the override empty so the environment is consulted; a regression
	// that hard-codes a path again fails here instead of reaching the host's
	// real systemd.
	old := sdNotifySocket
	sdNotifySocket = ""
	t.Cleanup(func() { sdNotifySocket = old })

	sock := filepath.Join(t.TempDir(), "from-env.sock")
	pc, err := net.ListenPacket("unixgram", sock)
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	t.Setenv(sdNotifySocketEnv, sock)

	if err := SDNotify(watchdogPing); err != nil {
		t.Fatalf("SDNotify: %v", err)
	}
	buf := make([]byte, 128)
	if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("NOTIFY_SOCKET was ignored: %v", err)
	}
	if got := string(buf[:n]); got != watchdogPing {
		t.Errorf("SDNotify delivered %q, want %q", got, watchdogPing)
	}
}

// TestSDNotifySocketFallsBackToFilesystemPath locks the fallback: without
// NOTIFY_SOCKET the address must be the filesystem path systemd binds, never
// the abstract name the old code invented.
func TestSDNotifySocketFallsBackToFilesystemPath(t *testing.T) {
	old := sdNotifySocket
	sdNotifySocket = ""
	t.Cleanup(func() { sdNotifySocket = old })

	t.Setenv(sdNotifySocketEnv, "")

	got := notifySocketAddr()
	if got != sdNotifySocketDefault {
		t.Errorf("notifySocketAddr() = %q, want fallback %q", got, sdNotifySocketDefault)
	}
	if got != "/run/systemd/notify" {
		t.Errorf("fallback = %q, want the filesystem path /run/systemd/notify", got)
	}
}

// TestSDNotifyAbstractNamespaceDelivery exercises the '@' → NUL translation end
// to end against a real abstract-namespace listener. TestNotifySocketAddr-
// HonoursEnv only compares strings, so a regression in dialNotifySocket (for
// example dropping the "\x00" prefix, which would silently dial a filesystem
// path named "@...") would pass every other test while breaking the abstract
// builds the old code was written for.
func TestSDNotifyAbstractNamespaceDelivery(t *testing.T) {
	// A unique abstract name: the NUL prefix is what net.ListenPacket wants for
	// the abstract namespace, and '@' is how systemd spells the same address.
	name := fmt.Sprintf("@warden-sdnotify-test-%d", os.Getpid())
	pc, err := net.ListenPacket("unixgram", "\x00"+name[1:])
	if err != nil {
		t.Skipf("abstract namespace unavailable on this host: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	old := sdNotifySocket
	sdNotifySocket = ""
	t.Cleanup(func() { sdNotifySocket = old })

	t.Setenv(sdNotifySocketEnv, name)

	if err := SDNotify(watchdogPing); err != nil {
		t.Fatalf("SDNotify via abstract address %q: %v", name, err)
	}
	buf := make([]byte, 128)
	if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read from abstract listener (the '@' prefix was not translated to NUL): %v", err)
	}
	if got := string(buf[:n]); got != watchdogPing {
		t.Errorf("SDNotify delivered %q, want %q", got, watchdogPing)
	}
}

// TestNotifySocketAddrHonoursEnv covers the resolution rules, including the
// abstract-namespace form systemd uses on some builds.
func TestNotifySocketAddrHonoursEnv(t *testing.T) {
	old := sdNotifySocket
	sdNotifySocket = ""
	t.Cleanup(func() { sdNotifySocket = old })

	cases := []struct {
		env  string
		want string
	}{
		{"/run/systemd/notify", "/run/systemd/notify"},
		{"@/org/freedesktop/systemd1/notify", "@/org/freedesktop/systemd1/notify"},
		{"  /run/user/0/systemd/notify  ", "/run/user/0/systemd/notify"},
	}
	for _, tc := range cases {
		t.Setenv(sdNotifySocketEnv, tc.env)
		if got := notifySocketAddr(); got != tc.want {
			t.Errorf("NOTIFY_SOCKET=%q -> %q, want %q", tc.env, got, tc.want)
		}
	}
}
