package gateway

// The SNI entry terminates TCP, so without a relay header every request that
// reaches the HTTPS listener would come from 127.0.0.1: audit records, login
// rate limits and proxy telemetry would all report the loopback address instead
// of the real client. The relay therefore prepends a PROXY protocol v1 header
// when it hands a panel connection over, and the HTTPS listener strips it here.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// proxyHeaderMax is the longest PROXY protocol v1 header, CRLF included.
	proxyHeaderMax = 107
	// proxyHeaderTimeout bounds the wait for a header from a peer that stays
	// silent. net/http reads RemoteAddr (and therefore this header) before it
	// installs the TLS handshake deadline, so the bound has to come from here:
	// without it a silent peer would hold a connection goroutine forever.
	proxyHeaderTimeout = 5 * time.Second
)

var proxyV1Prefix = []byte("PROXY ")

// WithProxyProtocol wraps ln so a connection that opens with a PROXY protocol
// v1 header reports the forwarded client address from RemoteAddr. Connections
// without a header keep the socket address untouched.
func WithProxyProtocol(ln net.Listener) net.Listener {
	if ln == nil {
		return nil
	}
	return &proxyProtocolListener{Listener: ln}
}

type proxyProtocolListener struct {
	net.Listener
}

func (l *proxyProtocolListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &proxyProtocolConn{Conn: conn, reader: bufio.NewReader(conn)}, nil
}

// proxyProtocolConn consumes the relay header on first use. Parsing stays lazy
// so a peer that never sends anything cannot block the accept loop.
type proxyProtocolConn struct {
	net.Conn
	reader *bufio.Reader
	once   sync.Once
	remote net.Addr
}

func (c *proxyProtocolConn) Read(p []byte) (int, error) {
	c.parseHeader()
	return c.reader.Read(p)
}

func (c *proxyProtocolConn) RemoteAddr() net.Addr {
	c.parseHeader()
	if c.remote != nil {
		return c.remote
	}
	return c.Conn.RemoteAddr()
}

// CloseWrite keeps http.Server able to half-close a wrapped connection.
func (c *proxyProtocolConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// parseHeader reads and strips a PROXY v1 header. net/http asks for the remote
// address before it installs its own read deadline, so the deadline added here
// is removed again straight away.
func (c *proxyProtocolConn) parseHeader() {
	c.once.Do(func() {
		// Only a local relay is allowed to announce an address: the header is
		// unauthenticated, so a directly connected client could otherwise claim
		// any source IP in the audit log and in the login rate limiter.
		if !trustedProxyPeer(c.Conn.RemoteAddr()) {
			return
		}
		if err := c.Conn.SetReadDeadline(time.Now().Add(proxyHeaderTimeout)); err != nil {
			return
		}
		defer func() { _ = c.Conn.SetReadDeadline(time.Time{}) }()
		prefix, err := c.reader.Peek(len(proxyV1Prefix))
		if err != nil || !bytes.Equal(prefix, proxyV1Prefix) {
			return
		}
		line, err := c.peekLine()
		if err != nil {
			return
		}
		addr, err := parseProxyHeader(line)
		if err != nil {
			// Keep the bytes in the stream: the TLS handshake in front of this
			// connection fails and the caller closes it.
			return
		}
		_, _ = c.reader.Discard(len(line))
		if addr != nil {
			c.remote = addr
		}
	})
}

// trustedProxyPeer reports whether the socket peer may announce a client
// address. The SNI entry dials the panel listener over loopback, so only a
// loopback peer is trusted.
func trustedProxyPeer(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil {
		return false
	}
	return tcp.IP.IsLoopback()
}

// peekLine returns the header line without consuming it.
func (c *proxyProtocolConn) peekLine() ([]byte, error) {
	for n := len(proxyV1Prefix) + 2; n <= proxyHeaderMax; n++ {
		buf, err := c.reader.Peek(n)
		if err != nil {
			return nil, err
		}
		if buf[n-2] == '\r' && buf[n-1] == '\n' {
			return buf, nil
		}
	}
	return nil, errors.New("proxy protocol: header line too long")
}

// parseProxyHeader decodes one PROXY v1 header line. A nil address means the
// header announced UNKNOWN and the socket address stays authoritative.
func parseProxyHeader(line []byte) (net.Addr, error) {
	fields := strings.Fields(strings.TrimRight(string(line), "\r\n"))
	if len(fields) < 2 || !strings.EqualFold(fields[0], "PROXY") {
		return nil, errors.New("proxy protocol: not a version 1 header")
	}
	switch strings.ToUpper(fields[1]) {
	case "UNKNOWN":
		return nil, nil
	case "TCP4", "TCP6":
	default:
		return nil, fmt.Errorf("proxy protocol: unsupported protocol %q", fields[1])
	}
	if len(fields) != 6 {
		return nil, fmt.Errorf("proxy protocol: got %d fields, want 6", len(fields))
	}
	ip := net.ParseIP(fields[2])
	if ip == nil {
		return nil, fmt.Errorf("proxy protocol: invalid source address %q", fields[2])
	}
	port, err := strconv.Atoi(fields[4])
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("proxy protocol: invalid source port %q", fields[4])
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}

// writeProxyHeader announces the original client and the relay destination to a
// listener that understands the PROXY protocol.
func writeProxyHeader(w io.Writer, src, dst net.Addr) error {
	line, err := formatProxyHeader(src, dst)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, line)
	return err
}

// formatProxyHeader renders the version 1 header for two TCP addresses.
func formatProxyHeader(src, dst net.Addr) (string, error) {
	srcTCP, ok := src.(*net.TCPAddr)
	if !ok || srcTCP.IP == nil {
		return "", fmt.Errorf("proxy protocol: unsupported source address %v", src)
	}
	dstTCP, ok := dst.(*net.TCPAddr)
	if !ok || dstTCP.IP == nil {
		return "", fmt.Errorf("proxy protocol: unsupported destination address %v", dst)
	}
	srcIP, dstIP := srcTCP.IP, dstTCP.IP
	family := "TCP4"
	if srcIP.To4() != nil && dstIP.To4() != nil {
		srcIP, dstIP = srcIP.To4(), dstIP.To4()
	} else if srcIP.To4() == nil && dstIP.To4() == nil {
		family = "TCP6"
	} else {
		return "", errors.New("proxy protocol: source and destination address families differ")
	}
	return fmt.Sprintf("PROXY %s %s %s %d %d\r\n", family, srcIP.String(), dstIP.String(), srcTCP.Port, dstTCP.Port), nil
}
