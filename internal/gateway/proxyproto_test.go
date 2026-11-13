package gateway

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

func TestParseProxyHeader(t *testing.T) {
	addr, err := parseProxyHeader([]byte("PROXY TCP4 203.0.113.7 198.51.100.20 51234 8443\r\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("address type = %T, want *net.TCPAddr", addr)
	}
	if tcp.IP.String() != "203.0.113.7" || tcp.Port != 51234 {
		t.Fatalf("address = %s, want 203.0.113.7:51234", tcp)
	}

	unknown, err := parseProxyHeader([]byte("PROXY UNKNOWN\r\n"))
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if unknown != nil {
		t.Fatalf("unknown address = %v, want nil", unknown)
	}

	for _, line := range []string{
		"PROXY\r\n",
		"PROXY UDP4 203.0.113.7 198.51.100.20 51234 8443\r\n",
		"PROXY TCP4 203.0.113.7 198.51.100.20 51234\r\n",
		"PROXY TCP4 not-an-ip 198.51.100.20 51234 8443\r\n",
		"PROXY TCP4 203.0.113.7 198.51.100.20 99999 8443\r\n",
	} {
		if _, err := parseProxyHeader([]byte(line)); err == nil {
			t.Fatalf("parse %q: want error", line)
		}
	}
}

func TestFormatProxyHeader(t *testing.T) {
	v4, err := formatProxyHeader(
		&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51234},
		&net.TCPAddr{IP: net.ParseIP("198.51.100.20"), Port: 8443},
	)
	if err != nil {
		t.Fatalf("v4: %v", err)
	}
	if want := "PROXY TCP4 203.0.113.7 198.51.100.20 51234 8443\r\n"; v4 != want {
		t.Fatalf("v4 header = %q, want %q", v4, want)
	}

	v6, err := formatProxyHeader(
		&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 51234},
		&net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 8443},
	)
	if err != nil {
		t.Fatalf("v6: %v", err)
	}
	if want := "PROXY TCP6 2001:db8::1 2001:db8::2 51234 8443\r\n"; v6 != want {
		t.Fatalf("v6 header = %q, want %q", v6, want)
	}

	if _, err := formatProxyHeader(&net.TCPAddr{IP: net.ParseIP("203.0.113.7")}, nil); err == nil {
		t.Fatal("nil destination: want error")
	}
}

// A relayed connection must report the announced client address and must hand
// the forwarded bytes to the reader unchanged.
func TestWithProxyProtocolReadsClientAddr(t *testing.T) {
	ln := listenLocal(t)
	clientCh := make(chan net.Conn, 1)
	go func() {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			clientCh <- nil
			return
		}
		clientCh <- conn
	}()
	server := acceptWithin(t, ln)
	client := <-clientCh
	if client == nil {
		t.Fatal("dial listener")
	}
	defer client.Close()
	if err := writeProxyHeader(client,
		&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 40000},
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8443},
	); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := client.Write([]byte("payload")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if cw, ok := client.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	wrapped := &proxyProtocolConn{Conn: server, reader: bufio.NewReader(server)}
	defer wrapped.Close()
	if got := wrapped.RemoteAddr().String(); got != "203.0.113.9:40000" {
		t.Fatalf("remote addr = %s, want 203.0.113.9:40000", got)
	}
	got, err := io.ReadAll(wrapped)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("payload = %q, want %q", got, "payload")
	}
}

// A connection that opens without a relay header keeps its socket address and
// its bytes.
func TestWithProxyProtocolKeepsPlainConnections(t *testing.T) {
	ln := listenLocal(t)
	wrapped := WithProxyProtocol(ln)
	if wrapped == nil {
		t.Fatal("wrapped listener is nil")
	}
	go func() {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
		_ = conn.Close()
	}()
	server := acceptWithin(t, wrapped)
	defer server.Close()
	host, _, err := net.SplitHostPort(server.RemoteAddr().String())
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("remote addr = %s", server.RemoteAddr())
	}
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(server, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "GET /" {
		t.Fatalf("first bytes = %q", buf)
	}
}

// The relay must announce the real client to the panel listener, and must leave
// passthrough connections untouched.
func TestSNIProxyRelaysPanelClientAddr(t *testing.T) {
	panelLn := listenLocal(t)
	type seen struct {
		addr  string
		hello []byte
	}
	seenCh := make(chan seen, 1)
	go func() {
		conn, err := WithProxyProtocol(panelLn).Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		addr := conn.RemoteAddr().String()
		buf := make([]byte, 5)
		_, _ = io.ReadFull(conn, buf)
		seenCh <- seen{addr: addr, hello: buf}
	}()

	log, err := logging.New("error", "")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	cfg := config.SNIConfig{Enabled: true, PanelDomain: "panel.example.com"}
	proxy := NewSNIProxy(cfg, "127.0.0.1:0", panelLn.Addr().String(), telemetry.NewStore(), log)
	if err := proxy.Listen(); err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	defer proxy.Close()
	go func() { _ = proxy.Serve() }()

	client, err := net.Dial("tcp", proxy.Addr())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer client.Close()
	hello := clientHelloBytes(t, "panel.example.com")
	if _, err := client.Write(hello); err != nil {
		t.Fatalf("write client hello: %v", err)
	}

	select {
	case got := <-seenCh:
		wantHost, wantPort, err := net.SplitHostPort(client.LocalAddr().String())
		if err != nil {
			t.Fatalf("split client addr: %v", err)
		}
		if got.addr != net.JoinHostPort(wantHost, wantPort) {
			t.Fatalf("panel saw client %s, want %s", got.addr, client.LocalAddr())
		}
		if !bytes.Equal(got.hello, hello[:5]) {
			t.Fatalf("panel hello record = %x, want %x", got.hello, hello[:5])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("panel listener was never reached")
	}
}

// Only a loopback peer may announce a client address; the header itself is
// unauthenticated and would otherwise let any client forge its audit address.
func TestTrustedProxyPeer(t *testing.T) {
	for _, tc := range []struct {
		addr net.Addr
		want bool
	}{
		{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5000}, true},
		{&net.TCPAddr{IP: net.ParseIP("::1"), Port: 5000}, true},
		{&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 5000}, false},
		{&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 5000}, false},
		{nil, false},
	} {
		if got := trustedProxyPeer(tc.addr); got != tc.want {
			t.Fatalf("trustedProxyPeer(%v) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// A remote peer that sends a PROXY header must not be believed: the socket
// address stays authoritative for the connection.
func TestWithProxyProtocolIgnoresRemotePeers(t *testing.T) {
	local := nonLoopbackIPv4(t)
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listen addr: %v", err)
	}
	clientCh := make(chan net.Conn, 1)
	go func() {
		conn, err := net.Dial("tcp", net.JoinHostPort(local, port))
		if err != nil {
			clientCh <- nil
			return
		}
		clientCh <- conn
	}()
	server := acceptWithin(t, WithProxyProtocol(ln))
	defer server.Close()
	client := <-clientCh
	if client == nil {
		t.Skip("cannot dial the non-loopback address of this host")
	}
	defer client.Close()
	if err := writeProxyHeader(client,
		&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 40000},
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8443},
	); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := client.Write([]byte("payload")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	if got := server.RemoteAddr().String(); !strings.HasPrefix(got, local+":") {
		t.Fatalf("remote addr = %s, want the socket peer %s", got, local)
	}
	buf := make([]byte, len("PROXY "))
	if _, err := io.ReadFull(server, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "PROXY " {
		t.Fatalf("first bytes = %q, want the untouched header", buf)
	}
}

// nonLoopbackIPv4 returns a non-loopback IPv4 address of this host.
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// acceptWithin accepts one connection or fails the test instead of blocking
// the whole package when a peer never arrives.
func acceptWithin(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		ch <- result{conn: conn, err: err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("accept: %v", got.err)
		}
		return got.conn
	case <-time.After(10 * time.Second):
		t.Fatal("accept timed out")
		return nil
	}
}

// listenLocal returns a loopback listener that is closed with the test.
func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// clientHelloBytes builds a minimal TLS ClientHello carrying serverName.
func clientHelloBytes(t *testing.T, serverName string) []byte {
	t.Helper()
	var body bytes.Buffer
	body.Write([]byte{0x03, 0x03})
	body.Write(bytes.Repeat([]byte{0x11}, 32))
	body.WriteByte(0)
	body.Write([]byte{0x00, 0x02, 0x13, 0x01})
	body.Write([]byte{0x01, 0x00})
	var ext bytes.Buffer
	name := []byte(serverName)
	entry := append([]byte{0x00, byte(len(name) >> 8), byte(len(name))}, name...)
	list := append([]byte{byte(len(entry) >> 8), byte(len(entry))}, entry...)
	ext.Write([]byte{0x00, 0x00, byte(len(list) >> 8), byte(len(list))})
	ext.Write(list)
	body.Write([]byte{byte(ext.Len() >> 8), byte(ext.Len())})
	body.Write(ext.Bytes())

	out := []byte{0x16, 0x03, 0x01}
	helloLen := 4 + body.Len()
	out = append(out, byte(helloLen>>8), byte(helloLen), 0x01)
	out = append(out, byte(body.Len()>>16), byte(body.Len()>>8), byte(body.Len()))
	out = append(out, body.Bytes()...)
	return out
}
