package dnsproxy

import (
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

func TestListenCandidatesPrefersConfiguredThenFallback(t *testing.T) {
	cfg := config.DNSConfig{Enabled: true, Listen: "0.0.0.0:53", GatewayIP: "203.0.113.10"}
	candidates := ListenCandidates(cfg)
	if len(candidates) < 4 {
		t.Fatalf("expected fallback candidates, got %v", candidates)
	}
	if candidates[0] != "0.0.0.0:53" {
		t.Fatalf("first candidate = %q", candidates[0])
	}
	want := map[string]bool{"203.0.113.10:53": false, "0.0.0.0:5353": false, "203.0.113.10:5353": false, "0.0.0.0:9053": false}
	for _, addr := range candidates {
		if _, ok := want[addr]; ok {
			want[addr] = true
		}
	}
	for addr, seen := range want {
		if !seen {
			t.Fatalf("candidate %s missing from %v", addr, candidates)
		}
	}

	custom := ListenCandidates(config.DNSConfig{Enabled: true, Listen: "127.0.0.1:5353", GatewayIP: "203.0.113.10"})
	for _, addr := range custom {
		if addr != "127.0.0.1:5353" && addr != "203.0.113.10:5353" {
			t.Fatalf("unexpected candidate %q for non-default port", addr)
		}
	}
}

func TestStatusReportsDisabledProxyAsStopped(t *testing.T) {
	var proxy *Proxy
	state := proxy.Status()
	if state.Enabled || state.Running {
		t.Fatalf("nil proxy status = %+v", state)
	}
}

func TestHTTPStatusForRcode(t *testing.T) {
	cases := []struct {
		rcode int
		want  int
	}{
		{dns.RcodeSuccess, 200},
		{dns.RcodeFormatError, 400},
		{dns.RcodeServerFailure, 502},
		{dns.RcodeNameError, 404},
		{dns.RcodeNotImplemented, 501},
		{dns.RcodeRefused, 403},
		{99, 502},
	}
	for _, c := range cases {
		if got := httpStatusForRcode(c.rcode); got != c.want {
			t.Fatalf("httpStatusForRcode(%d) = %d, want %d", c.rcode, got, c.want)
		}
	}
	if httpStatusForRcode(0) == 0 {
		t.Fatal("NOERROR must not map to the failure status 0")
	}
}

// fakeWriter captures the response written by serveDNS.
type fakeWriter struct {
	msg *dns.Msg
	err error
}

func (f *fakeWriter) LocalAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53} }
func (f *fakeWriter) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 45678}
}
func (f *fakeWriter) WriteMsg(m *dns.Msg) error {
	f.msg = m
	return f.err
}
func (f *fakeWriter) Write(b []byte) (int, error) { return len(b), nil }
func (f *fakeWriter) Close() error                { return nil }
func (f *fakeWriter) TsigStatus() error           { return nil }
func (f *fakeWriter) TsigTimersOnly(bool)         {}
func (f *fakeWriter) Hijack()                     {}

func newTestProxy(t *testing.T, store *telemetry.Store, upstream string) *Proxy {
	t.Helper()
	logger, err := logging.New("error", "")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	proxy, err := New(config.DNSConfig{
		Enabled:   true,
		Listen:    "127.0.0.1:5353",
		GatewayIP: "203.0.113.10",
		Upstream:  upstream,
		Domains:   []string{"github.com"},
	}, store, logger)
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	return proxy
}

func dnsStats(t *testing.T, store *telemetry.Store) telemetry.ProxyDetail {
	t.Helper()
	for _, d := range store.Detail(60, false) {
		if d.Name == "dns" {
			return d
		}
	}
	t.Fatal("dns proxy stats missing")
	return telemetry.ProxyDetail{}
}

// startUpstream answers every query with the given rcode.
func startUpstream(t *testing.T, rcode int) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Rcode = rcode
		_ = w.WriteMsg(m)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	return pc.LocalAddr().String(), func() { _ = srv.Shutdown() }
}

func TestServeDNSRecordsHitAsSuccess(t *testing.T) {
	store := telemetry.NewStore()
	proxy := newTestProxy(t, store, "127.0.0.1:1")
	req := new(dns.Msg)
	req.SetQuestion("github.com.", dns.TypeA)
	w := &fakeWriter{}
	proxy.serveDNS(w, req)
	if w.msg == nil {
		t.Fatal("no response written")
	}
	if w.msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d", w.msg.Rcode)
	}
	stats := dnsStats(t, store)
	if stats.Requests != 1 || stats.Failures != 0 {
		t.Fatalf("requests/failures = %d/%d, want 1/0", stats.Requests, stats.Failures)
	}
	if stats.StatusClasses["2xx"] != 1 || stats.StatusClasses["5xx"] != 0 {
		t.Fatalf("status classes = %v", stats.StatusClasses)
	}
	if stats.LastStatus != 200 {
		t.Fatalf("last status = %d, want 200", stats.LastStatus)
	}
}

func TestServeDNSRecordsUpstreamFailureAsServerError(t *testing.T) {
	// Port 1 on loopback refuses traffic, so the exchange fails and serveDNS
	// falls back to SERVFAIL.
	store := telemetry.NewStore()
	proxy := newTestProxy(t, store, "127.0.0.1:1")
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	proxy.serveDNS(&fakeWriter{}, req)
	stats := dnsStats(t, store)
	if stats.Requests != 1 || stats.Failures != 1 {
		t.Fatalf("requests/failures = %d/%d, want 1/1", stats.Requests, stats.Failures)
	}
	if stats.StatusClasses["5xx"] != 1 {
		t.Fatalf("status classes = %v", stats.StatusClasses)
	}
	if stats.LastStatus != 502 {
		t.Fatalf("last status = %d, want 502", stats.LastStatus)
	}
}

func TestServeDNSRecordsUpstreamNxdomain(t *testing.T) {
	addr, stop := startUpstream(t, dns.RcodeNameError)
	defer stop()
	store := telemetry.NewStore()
	proxy := newTestProxy(t, store, addr)
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	proxy.serveDNS(&fakeWriter{}, req)
	stats := dnsStats(t, store)
	if stats.Failures != 0 {
		t.Fatalf("nxdomain counted as failure: %+v", stats.ProxyStats)
	}
	if stats.StatusClasses["4xx"] != 1 {
		t.Fatalf("status classes = %v", stats.StatusClasses)
	}
}

func TestServeDNSRecordsUpstreamServfail(t *testing.T) {
	addr, stop := startUpstream(t, dns.RcodeServerFailure)
	defer stop()
	store := telemetry.NewStore()
	proxy := newTestProxy(t, store, addr)
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	proxy.serveDNS(&fakeWriter{}, req)
	stats := dnsStats(t, store)
	if stats.Failures != 1 || stats.StatusClasses["5xx"] != 1 {
		t.Fatalf("servfail stats = %+v classes %v", stats.ProxyStats, stats.StatusClasses)
	}
}
