package dnsproxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

type Proxy struct {
	cfg      config.DNSConfig
	upstream string
	gateway  net.IP
	domains  []string
	server   *dns.Server
	tcp      *dns.Server
	stats    *telemetry.Store
	log      *logging.Logger
	mu       sync.Mutex
}

func New(cfg config.DNSConfig, stats *telemetry.Store, log *logging.Logger) (*Proxy, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	ip := net.ParseIP(cfg.GatewayIP)
	if ip == nil {
		return nil, fmt.Errorf("invalid dns.gateway_ip %q", cfg.GatewayIP)
	}
	p := &Proxy{cfg: cfg, upstream: cfg.Upstream, gateway: ip, stats: stats, log: log}
	for _, d := range cfg.Domains {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d != "" {
			p.domains = append(p.domains, d)
		}
	}
	return p, nil
}

func (p *Proxy) Serve() error {
	if p == nil {
		return nil
	}
	handler := dns.HandlerFunc(p.serveDNS)
	p.server = &dns.Server{Addr: p.cfg.Listen, Net: "udp", Handler: handler}
	p.tcp = &dns.Server{Addr: p.cfg.Listen, Net: "tcp", Handler: handler}
	errCh := make(chan error, 2)
	go func() { errCh <- p.server.ListenAndServe() }()
	go func() { errCh <- p.tcp.ListenAndServe() }()
	p.log.Infof("dns listening on %s (gateway %s, %d domains)", p.cfg.Listen, p.gateway.String(), len(p.domains))
	select {
	case err := <-errCh:
		return err
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}

func (p *Proxy) Close() error {
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var first error
	if p.server != nil {
		if err := p.server.ShutdownContext(ctx); err != nil {
			first = err
		}
	}
	if p.tcp != nil {
		if err := p.tcp.ShutdownContext(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (p *Proxy) serveDNS(w dns.ResponseWriter, req *dns.Msg) {
	if req == nil || len(req.Question) == 0 {
		return
	}
	q := req.Question[0]
	qname := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	in := int64(req.Len())
	remote := ""
	if addr := w.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	// hit/miss counters drive the split ratio shown by the panel.
	call := p.stats.Begin("dns")
	if p.matches(qname) {
		p.stats.Increment("dns_hit")
		resp := p.localAnswer(req, q, qname)
		_ = w.WriteMsg(resp)
		call.Done(dns.RcodeSuccess, in, int64(resp.Len()), "", remote, "DNS", qname)
		return
	}
	p.stats.Increment("dns_miss")
	client := &dns.Client{Net: "udp", Timeout: 5 * time.Second}
	resp, _, err := client.Exchange(req, p.upstream)
	if err != nil || resp == nil {
		call.Done(dns.RcodeServerFailure, in, 0, errString(err), remote, "DNS", qname)
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
		return
	}
	if resp.Truncated {
		tcpClient := &dns.Client{Net: "tcp", Timeout: 5 * time.Second}
		if r2, _, err2 := tcpClient.Exchange(req, p.upstream); err2 == nil && r2 != nil {
			resp = r2
		}
	}
	_ = w.WriteMsg(resp)
	call.Done(resp.Rcode, in, int64(resp.Len()), "", remote, "DNS", qname)
}

func (p *Proxy) localAnswer(req *dns.Msg, q dns.Question, qname string) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	m.RecursionAvailable = true
	ttl := uint32(60)
	switch q.Qtype {
	case dns.TypeA:
		if v4 := p.gateway.To4(); v4 != nil {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
				A:   v4,
			})
		}
	case dns.TypeAAAA:
		if p.gateway.To4() == nil {
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
				AAAA: p.gateway,
			})
		}
	case dns.TypeCNAME:
		// Return NOERROR with no answer for CNAME queries; clients should use A records.
	case dns.TypeHTTPS:
		// Avoid advertising HTTPS records that bypass this gateway.
	default:
		m.Rcode = dns.RcodeSuccess
	}
	p.log.Debugf("dns local answer: %s type=%d from gateway", qname, q.Qtype)
	return m
}

func (p *Proxy) matches(name string) bool {
	for _, d := range p.domains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ = errors.New
var _ = log.Println
var _ = net.IPv4len
