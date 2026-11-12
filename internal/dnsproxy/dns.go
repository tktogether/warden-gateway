package dnsproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

// fallbackPorts are tried in order when the preferred DNS port is unavailable.
var fallbackPorts = []string{"5353", "5300", "9053"}

// Status reports the live state of the DNS listener. Listen is the address the
// proxy actually bound; Configured is the address from the config file.
type Status struct {
	Enabled    bool     `json:"enabled"`
	Running    bool     `json:"running"`
	Configured string   `json:"configured"`
	Listen     string   `json:"listen"`
	Port       string   `json:"port"`
	Fallback   bool     `json:"fallback"`
	Error      string   `json:"error,omitempty"`
	GatewayIP  string   `json:"gateway_ip"`
	Domains    []string `json:"domains"`
	Upstream   string   `json:"upstream"`
}

// Proxy answers the split domains with the gateway address and forwards every
// other query upstream.
type Proxy struct {
	cfg      config.DNSConfig
	upstream string
	gateway  net.IP
	domains  []string
	stats    *telemetry.Store
	log      *logging.Logger

	mu     sync.Mutex
	udp    *dns.Server
	tcp    *dns.Server
	status Status
}

// New builds a DNS proxy. It returns (nil, nil) when the feature is disabled.
func New(cfg config.DNSConfig, stats *telemetry.Store, log *logging.Logger) (*Proxy, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	gateway := net.ParseIP(strings.TrimSpace(cfg.GatewayIP))
	if gateway == nil {
		return nil, errors.New("dns gateway ip is invalid")
	}
	upstream := strings.TrimSpace(cfg.Upstream)
	if _, _, err := net.SplitHostPort(upstream); err != nil {
		upstream = net.JoinHostPort(upstream, "53")
	}
	domains := make([]string, 0, len(cfg.Domains))
	for _, d := range cfg.Domains {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d != "" {
			domains = append(domains, d)
		}
	}
	return &Proxy{
		cfg:      cfg,
		upstream: upstream,
		gateway:  gateway,
		domains:  domains,
		stats:    stats,
		log:      log,
		status: Status{
			Enabled:    true,
			Configured: cfg.Listen,
			GatewayIP:  gateway.String(),
			Domains:    domains,
			Upstream:   upstream,
		},
	}, nil
}

// Status returns a snapshot of the current listener state.
func (p *Proxy) Status() Status {
	if p == nil {
		return Status{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.status
	st.Domains = append([]string(nil), p.status.Domains...)
	return st
}

// ListenCandidates returns the bind addresses handled in order: the configured
// address first, then the gateway address on the same port, then the fallback
// ports when the preferred port is the standard DNS port.
func ListenCandidates(cfg config.DNSConfig) []string {
	base := strings.TrimSpace(cfg.Listen)
	if base == "" {
		base = "0.0.0.0:53"
	}
	host, port, err := net.SplitHostPort(base)
	if err != nil {
		host, port = "0.0.0.0", "53"
	}
	gateway := strings.TrimSpace(cfg.GatewayIP)
	if net.ParseIP(gateway) == nil {
		gateway = ""
	}
	out := make([]string, 0, 8)
	add := func(addr string) {
		for _, seen := range out {
			if seen == addr {
				return
			}
		}
		out = append(out, addr)
	}
	add(base)
	if gateway != "" {
		add(net.JoinHostPort(gateway, port))
	}
	if port == "53" {
		for _, alt := range fallbackPorts {
			add(net.JoinHostPort(host, alt))
			if gateway != "" {
				add(net.JoinHostPort(gateway, alt))
			}
		}
	}
	return out
}

// SelectListen probes the candidates and returns the first address that can be
// bound for both UDP and TCP.
func SelectListen(cfg config.DNSConfig) (string, error) {
	var lastErr error
	for _, addr := range ListenCandidates(cfg) {
		if err := probe(addr); err != nil {
			lastErr = err
			continue
		}
		return addr, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no candidate address")
	}
	return "", lastErr
}

func probe(addr string) error {
	udp, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer udp.Close()
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return tcp.Close()
}

// Serve binds the first usable candidate and serves DNS on UDP and TCP until
// Close is called. A bind failure is reported once without terminating the
// caller, so a busy port never takes the whole gateway down.
func (p *Proxy) Serve() error {
	var lastErr error
	for _, addr := range ListenCandidates(p.cfg) {
		udpConn, err := net.ListenPacket("udp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		tcpLn, err := net.Listen("tcp", addr)
		if err != nil {
			_ = udpConn.Close()
			lastErr = err
			continue
		}
		udpSrv := &dns.Server{PacketConn: udpConn, Handler: dns.HandlerFunc(p.serveDNS)}
		tcpSrv := &dns.Server{Listener: tcpLn, Handler: dns.HandlerFunc(p.serveDNS)}
		_, port, _ := net.SplitHostPort(addr)
		fallback := addr != strings.TrimSpace(p.cfg.Listen)
		p.mu.Lock()
		p.udp = udpSrv
		p.tcp = tcpSrv
		p.status.Running = true
		p.status.Listen = addr
		p.status.Port = port
		p.status.Fallback = fallback
		p.status.Error = ""
		p.mu.Unlock()
		go func() { _ = udpSrv.ActivateAndServe() }()
		go func() { _ = tcpSrv.ActivateAndServe() }()
		if fallback {
			p.log.Warnf("dns listening on %s (configured %s unavailable: %v)", addr, p.cfg.Listen, lastErr)
		} else {
			p.log.Infof("dns listening on %s (gateway %s, %d domains)", addr, p.gateway, len(p.domains))
		}
		return nil
	}
	err := fmt.Errorf("dns listener unavailable: %w", lastErr)
	p.mu.Lock()
	p.status.Running = false
	p.status.Error = err.Error()
	p.mu.Unlock()
	return err
}

// Close shuts both DNS listeners down.
func (p *Proxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	udp, tcp := p.udp, p.tcp
	p.udp, p.tcp = nil, nil
	p.status.Running = false
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var firstErr error
	if udp != nil {
		if err := udp.ShutdownContext(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if tcp != nil {
		if err := tcp.ShutdownContext(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (p *Proxy) serveDNS(w dns.ResponseWriter, req *dns.Msg) {
	call := p.stats.Begin("dns")
	in, out := req.Len(), 0
	qname := ""
	if len(req.Question) > 0 {
		qname = strings.ToLower(strings.TrimSuffix(req.Question[0].Name, "."))
	}
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.RecursionAvailable = true
	if p.matches(qname) {
		p.stats.Increment("dns_hit")
		p.localAnswer(resp, qname)
	} else {
		p.stats.Increment("dns_miss")
		upstream := &dns.Client{Net: "udp", Timeout: 5 * time.Second}
		answer, _, err := upstream.Exchange(req, p.upstream)
		if err != nil {
			resp.Rcode = dns.RcodeServerFailure
		} else {
			if answer.Truncated {
				tcpClient := &dns.Client{Net: "tcp", Timeout: 5 * time.Second}
				if full, _, err := tcpClient.Exchange(req, p.upstream); err == nil {
					answer = full
				}
			}
			resp = answer
		}
	}
	out = resp.Len()
	errText := ""
	if err := w.WriteMsg(resp); err != nil {
		errText = err.Error()
	}
	call.DoneTarget(qname, httpStatusForRcode(resp.Rcode), int64(in), int64(out), errText, w.RemoteAddr().String(), "DNS", qname)
}

// httpStatusForRcode maps a DNS response code onto the HTTP-like status
// vocabulary used by the telemetry store. The store counts statuses >= 500,
// status 0 and non-empty errors as failures, so NOERROR must become 200 and
// SERVFAIL must become 5xx; passing the raw rcode made every successful lookup
// look like a failure.
func httpStatusForRcode(rcode int) int {
	switch rcode {
	case dns.RcodeSuccess:
		return 200
	case dns.RcodeFormatError:
		return 400
	case dns.RcodeServerFailure:
		return 502
	case dns.RcodeNameError:
		return 404
	case dns.RcodeNotImplemented:
		return 501
	case dns.RcodeRefused:
		return 403
	default:
		return 502
	}
}

func (p *Proxy) matches(name string) bool {
	if name == "" {
		return false
	}
	for _, d := range p.domains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

func (p *Proxy) localAnswer(resp *dns.Msg, qname string) {
	if len(resp.Question) == 0 {
		return
	}
	q := resp.Question[0]
	switch q.Qtype {
	case dns.TypeA:
		if v4 := p.gateway.To4(); v4 != nil {
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   v4,
			})
		}
	case dns.TypeAAAA:
		if p.gateway.To4() == nil {
			resp.Answer = append(resp.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60},
				AAAA: p.gateway,
			})
		}
	case dns.TypeCNAME, dns.TypeHTTPS:
		resp.Answer = nil
	}
}
