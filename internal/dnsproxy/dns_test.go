package dnsproxy

import (
	"testing"

	"github.com/tktogether/warden-gateway/internal/config"
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
