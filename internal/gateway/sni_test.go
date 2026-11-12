package gateway

import (
	"net"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

func newTestProxy(t *testing.T) *SNIProxy {
	t.Helper()
	log, err := logging.New("error", "")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	cfg := config.SNIConfig{Enabled: true, PanelDomain: "panel.example.com", Passthrough: []string{"github.com"}}
	return NewSNIProxy(cfg, "127.0.0.1:0", "127.0.0.1:1", telemetry.NewStore(), log)
}

// A client that connects and then stays silent must not block shutdown: systemd
// waits for the whole stop timeout otherwise and kills the unit instead of
// restarting it.
func TestSNICloseUnblocksActiveConnections(t *testing.T) {
	proxy := newTestProxy(t)
	if err := proxy.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = proxy.Serve() }()
	conn, err := net.DialTimeout("tcp", proxy.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	started := time.Now()
	if err := proxy.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("close blocked for %s with one idle connection", elapsed)
	}
}

// Closing a proxy without connections, or twice, must stay cheap and safe.
func TestSNICloseWithoutConnections(t *testing.T) {
	proxy := newTestProxy(t)
	if err := proxy.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	started := time.Now()
	if err := proxy.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("close without connections took %s", elapsed)
	}
	_ = proxy.Close()
}
