package cnagent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

const (
	testGatewayIP  = "203.0.113.10"
	testHealthzURL = "http://" + testGatewayIP + "/healthz"
)

var testNow = func() time.Time {
	return time.Date(2026, 11, 17, 8, 30, 0, 0, time.UTC)
}

var errRefused = errors.New("connection refused")

// probeCall records one prober invocation so tests can assert the exact
// sequence of network operations and the timeouts used.
type probeCall struct {
	kind    string // "get", "dial", "ping"
	target  string // url, port, or ip
	timeout time.Duration
}

func (c probeCall) key() string { return c.kind + ":" + c.target }

func dial(port string) probeCall { return probeCall{kind: "dial", target: port} }
func get(url string) probeCall   { return probeCall{kind: "get", target: url} }
func ping(ip string) probeCall   { return probeCall{kind: "ping", target: ip} }

// fakeProber scripts every network outcome; unset dials and HTTP gets
// succeed and unset pings succeed or fail via pingOK.
type fakeProber struct {
	pingOK  bool
	dialErr map[string]error
	httpErr map[string]error
	calls   []probeCall
}

func (f *fakeProber) DialTCP(host string, port string, timeout time.Duration) error {
	f.calls = append(f.calls, probeCall{kind: "dial", target: port, timeout: timeout})
	if err, ok := f.dialErr[port]; ok {
		return err
	}
	return nil
}

func (f *fakeProber) HTTPGet(url string, timeout time.Duration) error {
	f.calls = append(f.calls, probeCall{kind: "get", target: url, timeout: timeout})
	if err, ok := f.httpErr[url]; ok {
		return err
	}
	return nil
}

func (f *fakeProber) Ping(ip string, timeout time.Duration) bool {
	f.calls = append(f.calls, probeCall{kind: "ping", target: ip, timeout: timeout})
	return f.pingOK
}

func TestProbeGatewayClassifications(t *testing.T) {
	tests := []struct {
		name       string
		pingOK     bool
		dialErr    map[string]error
		httpErr    map[string]error
		want       string
		wantDetail string
		wantCalls  []probeCall
	}{
		{
			name:       "ok via healthz when 443 is closed",
			pingOK:     true,
			dialErr:    map[string]error{"443": errRefused},
			want:       "OK",
			wantDetail: "tcp 443 connect failed: connection refused",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
				ping(testGatewayIP),
				dial("22"),
				dial("80"),
				dial("443"),
				get(testHealthzURL),
			},
		},
		{
			name:       "ok via 443 skips healthz when 80 is closed",
			pingOK:     true,
			dialErr:    map[string]error{"80": errRefused},
			want:       "OK",
			wantDetail: "tcp 80 connect failed: connection refused",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
				ping(testGatewayIP),
				dial("22"),
				dial("80"),
				dial("443"),
			},
		},
		{
			name:       "single control failure is tolerated",
			pingOK:     true,
			httpErr:    map[string]error{"https://www.apple.com": errRefused},
			want:       "OK",
			wantDetail: "control https://www.apple.com failed: connection refused",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
				ping(testGatewayIP),
				dial("22"),
				dial("80"),
				dial("443"),
				get(testHealthzURL),
			},
		},
		{
			name:       "link_443_blocked when only 80 answers but healthz fails",
			pingOK:     true,
			dialErr:    map[string]error{"443": errRefused},
			httpErr:    map[string]error{testHealthzURL: errRefused},
			want:       "LINK_443_BLOCKED",
			wantDetail: "tcp 443 connect failed: connection refused",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
				ping(testGatewayIP),
				dial("22"),
				dial("80"),
				dial("443"),
				get(testHealthzURL),
			},
		},
		{
			name:       "link_443_blocked beats c_down when 80 still answers",
			dialErr:    map[string]error{"22": errRefused, "443": errRefused},
			httpErr:    map[string]error{testHealthzURL: errRefused},
			want:       "LINK_443_BLOCKED",
			wantDetail: "icmp ping 203.0.113.10 failed",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
				ping(testGatewayIP),
				dial("22"),
				dial("80"),
				dial("443"),
				get(testHealthzURL),
			},
		},
		{
			name:       "c_down when icmp and 22 both fail",
			dialErr:    map[string]error{"22": errRefused, "80": errRefused, "443": errRefused},
			want:       "C_DOWN",
			wantDetail: "icmp ping 203.0.113.10 failed",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
				ping(testGatewayIP),
				dial("22"),
				dial("80"),
				dial("443"),
			},
		},
		{
			name:       "link_ports_blocked when icmp answers but 22 is closed",
			pingOK:     true,
			dialErr:    map[string]error{"22": errRefused, "80": errRefused, "443": errRefused},
			want:       "LINK_PORTS_BLOCKED",
			wantDetail: "tcp 22 connect failed: connection refused",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
				ping(testGatewayIP),
				dial("22"),
				dial("80"),
				dial("443"),
			},
		},
		{
			name:       "local_broken when two control sites fail",
			pingOK:     true,
			httpErr:    map[string]error{"https://www.apple.com": errRefused, "https://mirrors.aliyun.com": errRefused},
			want:       "LOCAL_BROKEN",
			wantDetail: "control https://www.apple.com failed: connection refused",
			wantCalls: []probeCall{
				get("https://www.apple.com"),
				get("https://mirrors.aliyun.com"),
				get("https://pypi.org"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeProber{pingOK: tt.pingOK, dialErr: tt.dialErr, httpErr: tt.httpErr}
			got := ProbeGatewayWith(context.Background(), testGatewayIP, testNow, fake)

			if got.Result != tt.want {
				t.Errorf("Result = %q, want %q", got.Result, tt.want)
			}
			if got.Detail != tt.wantDetail {
				t.Errorf("Detail = %q, want %q", got.Detail, tt.wantDetail)
			}
			if !got.CheckedAt.Equal(testNow()) {
				t.Errorf("CheckedAt = %v, want %v (injected clock)", got.CheckedAt, testNow())
			}

			gotKeys := make([]string, 0, len(fake.calls))
			for _, c := range fake.calls {
				gotKeys = append(gotKeys, c.key())
			}
			wantKeys := make([]string, 0, len(tt.wantCalls))
			for _, c := range tt.wantCalls {
				wantKeys = append(wantKeys, c.key())
			}
			if !reflect.DeepEqual(gotKeys, wantKeys) {
				t.Errorf("probe calls = %v, want %v", gotKeys, wantKeys)
			}
		})
	}
}

func TestLocalBrokenBeatsGatewayDown(t *testing.T) {
	fake := &fakeProber{
		httpErr: map[string]error{
			"https://www.apple.com":      errRefused,
			"https://mirrors.aliyun.com": errRefused,
			"https://pypi.org":           errRefused,
		},
		// Gateway fully down: ping and every port fail.
	}
	got := ProbeGatewayWith(context.Background(), testGatewayIP, testNow, fake)

	if got.Result != "LOCAL_BROKEN" {
		t.Fatalf("Result = %q, want LOCAL_BROKEN (local breakage must not be reported as C_DOWN)", got.Result)
	}
	if got.Detail != "control https://www.apple.com failed: connection refused" {
		t.Errorf("Detail = %q, want the first control failure", got.Detail)
	}
	for _, c := range fake.calls {
		if c.kind != "get" {
			t.Errorf("gateway probe %q ran despite LOCAL_BROKEN; calls = %v", c.key(), fake.calls)
		}
	}
}

func TestProbeGatewayTimeouts(t *testing.T) {
	fake := &fakeProber{pingOK: true}
	ProbeGatewayWith(context.Background(), testGatewayIP, testNow, fake)

	dials, gets, pings := 0, 0, 0
	for _, c := range fake.calls {
		switch c.kind {
		case "dial":
			dials++
			if c.timeout != 2*time.Second {
				t.Errorf("dial %s timeout = %v, want 2s (shell connect timeout)", c.target, c.timeout)
			}
		case "get":
			gets++
			if c.timeout != 4*time.Second {
				t.Errorf("get %s timeout = %v, want 4s (shell max-time)", c.target, c.timeout)
			}
		case "ping":
			pings++
			if c.timeout != 1*time.Second {
				t.Errorf("ping timeout = %v, want 1s (shell ping -W 1)", c.timeout)
			}
		}
	}
	if dials != 3 || gets != 4 || pings != 1 {
		t.Errorf("call counts: dials=%d gets=%d pings=%d, want 3 dials, 4 gets (3 control + healthz), 1 ping", dials, gets, pings)
	}
}
