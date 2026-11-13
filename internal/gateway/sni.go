package gateway

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

type HostMatcher struct {
	exact    map[string]struct{}
	suffixes []string
}

func NewHostMatcher(patterns []string) *HostMatcher {
	m := &HostMatcher{exact: map[string]struct{}{}}
	for _, raw := range patterns {
		p := normalizeHost(raw)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "*.") {
			s := strings.TrimPrefix(p, "*.")
			if s != "" {
				m.suffixes = append(m.suffixes, s)
			}
			continue
		}
		m.exact[p] = struct{}{}
	}
	return m
}

func (m *HostMatcher) Match(host string) bool {
	if m == nil {
		return false
	}
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	if _, ok := m.exact[h]; ok {
		return true
	}
	for _, s := range m.suffixes {
		if strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return ""
	}
	for _, r := range h {
		if r <= 32 || r == 127 {
			return ""
		}
	}
	if strings.ContainsAny(h, "/\\@:") {
		return ""
	}
	return h
}

type SNIProxy struct {
	cfg       config.SNIConfig
	entryAddr string
	panelAddr string
	matcher   *HostMatcher
	stats     *telemetry.Store
	log       *logging.Logger
	dial      func(network, addr string, timeout time.Duration) (net.Conn, error)
	listener  net.Listener
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	closing   bool
	wg        sync.WaitGroup
}

func NewSNIProxy(cfg config.SNIConfig, entryAddr, panelAddr string, stats *telemetry.Store, log *logging.Logger) *SNIProxy {
	if stats == nil {
		stats = telemetry.NewStore()
	}
	return &SNIProxy{
		cfg:       cfg,
		entryAddr: entryAddr,
		panelAddr: panelAddr,
		matcher:   NewHostMatcher(cfg.Passthrough),
		stats:     stats,
		log:       log,
		dial:      net.DialTimeout,
		conns:     make(map[net.Conn]struct{}),
	}
}

func (s *SNIProxy) Listen() error {
	ln, err := net.Listen("tcp", s.entryAddr)
	if err != nil {
		return fmt.Errorf("listen entry %s: %w", s.entryAddr, err)
	}
	s.listener = ln
	s.mu.Lock()
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	s.mu.Unlock()
	return nil
}

func (s *SNIProxy) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *SNIProxy) Serve() error {
	if s.listener == nil {
		return errors.New("sni listener is not initialized")
	}
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		if !s.track(conn) {
			continue
		}
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

// track registers a freshly accepted connection, refusing new ones once Close
// has started so WaitGroup.Add is never raced by Wait.
func (s *SNIProxy) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		_ = conn.Close()
		return false
	}
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	return true
}

func (s *SNIProxy) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// Close stops accepting and force-closes every relay connection so a long-lived
// client cannot block systemd shutdown for its full stop timeout.
func (s *SNIProxy) Close() error {
	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}
	s.mu.Lock()
	s.closing = true
	active := len(s.conns)
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.log.Warnf("sni: %d connection(s) still active after close timeout", active)
	}
	return err
}

func (s *SNIProxy) handle(client net.Conn) {
	defer client.Close()
	defer s.untrack(client)
	call := s.stats.Begin("sni")
	remote := ""
	if addr := client.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))
	hello, serverName, err := readClientHello(client)
	if err != nil {
		s.log.Debugf("sni: read client hello: %v", err)
		call.Done(0, 0, 0, "client hello: "+err.Error(), remote, "TLS", "")
		return
	}
	_ = client.SetDeadline(time.Time{})
	name := normalizeHost(serverName)
	if name == "" {
		call.Done(0, 0, 0, "client hello has no server name", remote, "TLS", "")
		return
	}
	var target string
	switch {
	case strings.EqualFold(name, normalizeHost(s.cfg.PanelDomain)):
		target = s.panelAddr
	case s.matcher.Match(name):
		target = net.JoinHostPort(name, "443")
	default:
		s.log.Debugf("sni: rejected host %q", name)
		call.Done(0, 0, 0, "host not allowed: "+name, remote, "TLS", name)
		return
	}
	dial := s.dial
	if dial == nil {
		dial = net.DialTimeout
	}
	upstream, err := dial("tcp", target, 10*time.Second)
	if err != nil {
		s.log.Warnf("sni: dial %s: %v", target, err)
		call.Done(0, 0, 0, "dial "+target+": "+err.Error(), remote, "TLS", name)
		return
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(10 * time.Second))
	// The panel listener sits behind this relay, so it can only learn the client
	// address from a PROXY protocol header. Passthrough connections go straight
	// to a third-party server and must stay untouched.
	if target == s.panelAddr {
		if err := writeProxyHeader(upstream, client.RemoteAddr(), client.LocalAddr()); err != nil {
			s.log.Debugf("sni: write proxy header to %s: %v", target, err)
			call.Done(0, 0, 0, "write proxy header: "+err.Error(), remote, "TLS", name)
			return
		}
	}
	if _, err := upstream.Write(hello); err != nil {
		s.log.Debugf("sni: write hello to %s: %v", target, err)
		call.Done(0, 0, 0, "write hello: "+err.Error(), remote, "TLS", name)
		return
	}
	_ = upstream.SetDeadline(time.Time{})
	in, out := relay(client, upstream)
	call.Done(200, in, out, "", remote, "TLS", name)
}

// relay copies traffic in both directions and returns the byte counts sent to
// the upstream (in) and returned to the client (out).
func relay(a, b net.Conn) (int64, int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	var toUpstream, toClient int64
	cp := func(dst, src net.Conn, counter *int64) {
		defer wg.Done()
		n, _ := io.Copy(dst, src)
		*counter = n
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(b, a, &toUpstream)
	go cp(a, b, &toClient)
	wg.Wait()
	return toUpstream, toClient
}

func readClientHello(conn net.Conn) ([]byte, string, error) {
	var raw []byte
	var payload []byte
	handshakeLen := -1
	for len(raw) < 128*1024 {
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil {
			return nil, "", err
		}
		if header[0] != 22 {
			return nil, "", errors.New("not a TLS handshake record")
		}
		n := int(header[3])<<8 | int(header[4])
		if n <= 0 || n > 65535 {
			return nil, "", errors.New("invalid TLS record length")
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(conn, body); err != nil {
			return nil, "", err
		}
		raw = append(raw, header...)
		raw = append(raw, body...)
		payload = append(payload, body...)
		if handshakeLen < 0 && len(payload) >= 4 {
			if payload[0] != 1 {
				return nil, "", errors.New("not a ClientHello")
			}
			handshakeLen = int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
		}
		if handshakeLen >= 0 && len(payload) >= 4+handshakeLen {
			name, err := parseServerName(payload[4 : 4+handshakeLen])
			return raw, name, err
		}
	}
	return nil, "", errors.New("client hello too large")
}

func parseServerName(hello []byte) (string, error) {
	pos := 0
	if len(hello) < 2+32+1 {
		return "", io.ErrUnexpectedEOF
	}
	pos += 2 + 32
	sidLen := int(hello[pos])
	pos++
	if pos+sidLen > len(hello) {
		return "", io.ErrUnexpectedEOF
	}
	pos += sidLen
	if pos+2 > len(hello) {
		return "", io.ErrUnexpectedEOF
	}
	cipherLen := int(hello[pos])<<8 | int(hello[pos+1])
	pos += 2
	if pos+cipherLen > len(hello) {
		return "", io.ErrUnexpectedEOF
	}
	pos += cipherLen
	if pos >= len(hello) {
		return "", errors.New("client hello has no compression methods")
	}
	compLen := int(hello[pos])
	pos++
	if pos+compLen > len(hello) {
		return "", io.ErrUnexpectedEOF
	}
	pos += compLen
	if pos+2 > len(hello) {
		return "", errors.New("client hello has no extensions")
	}
	extLen := int(hello[pos])<<8 | int(hello[pos+1])
	pos += 2
	end := pos + extLen
	if end > len(hello) {
		return "", io.ErrUnexpectedEOF
	}
	for pos+4 <= end {
		typ := int(hello[pos])<<8 | int(hello[pos+1])
		n := int(hello[pos+2])<<8 | int(hello[pos+3])
		pos += 4
		if pos+n > end {
			return "", io.ErrUnexpectedEOF
		}
		if typ == 0 {
			return parseServerNameExtension(hello[pos : pos+n])
		}
		pos += n
	}
	return "", errors.New("client hello has no server name")
}

func parseServerNameExtension(ext []byte) (string, error) {
	if len(ext) < 2 {
		return "", io.ErrUnexpectedEOF
	}
	listLen := int(ext[0])<<8 | int(ext[1])
	pos := 2
	if pos+listLen > len(ext) {
		return "", io.ErrUnexpectedEOF
	}
	end := pos + listLen
	for pos+3 <= end {
		nameType := ext[pos]
		n := int(ext[pos+1])<<8 | int(ext[pos+2])
		pos += 3
		if pos+n > end {
			return "", io.ErrUnexpectedEOF
		}
		if nameType == 0 {
			return string(ext[pos : pos+n]), nil
		}
		pos += n
	}
	return "", errors.New("server name extension is empty")
}
