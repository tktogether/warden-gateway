package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDetailAggregatesWindowAndLatency(t *testing.T) {
	s := NewStore()
	call := s.Begin("docker")
	time.Sleep(20 * time.Millisecond)
	call.DoneUpstream("tktogether/app", 12, 200, 1024, 4096, "", "10.0.0.1:5000", "GET", "/v2/tktogether/app/manifests/latest")
	call = s.Begin("docker")
	call.DoneTarget("tktogether/app", 500, 0, 0, "boom", "10.0.0.1:5000", "GET", "/v2/tktogether/app/blobs/sha256:deadbeef")

	details := s.Detail(60, true)
	if len(details) != 1 {
		t.Fatalf("want one proxy detail, got %d", len(details))
	}
	d := details[0]
	if d.Name != "docker" || d.Requests != 2 || d.Failures != 1 {
		t.Fatalf("unexpected counters: %+v", d)
	}
	if d.InFlight != 0 {
		t.Fatalf("in flight should be zero, got %d", d.InFlight)
	}
	if d.SuccessRate != 50 {
		t.Fatalf("success rate %v", d.SuccessRate)
	}
	if d.StatusClasses["2xx"] != 1 || d.StatusClasses["5xx"] != 1 {
		t.Fatalf("status classes %+v", d.StatusClasses)
	}
	if d.Latency.Count != 2 {
		t.Fatalf("latency histogram %+v", d.Latency)
	}
	var histTotal int64
	for _, bin := range d.Latency.Histogram {
		histTotal += bin.Count
	}
	if histTotal != 2 || d.Latency.Max < 20 {
		t.Fatalf("latency histogram total=%d max=%d", histTotal, d.Latency.Max)
	}
	if d.Upstream.Count != 1 || d.Upstream.AvgMillis != 12 {
		t.Fatalf("upstream latency %+v", d.Upstream)
	}
	if d.Window.Requests != 2 || d.Window.BytesIn != 1024 {
		t.Fatalf("window %+v", d.Window)
	}
	if d.Rates.RequestsPerMin <= 0 {
		t.Fatalf("rates %+v", d.Rates)
	}
	if len(d.Targets) != 1 || d.Targets[0].Target != "tktogether/app" || d.Targets[0].Requests != 2 {
		t.Fatalf("targets %+v", d.Targets)
	}
	if len(d.Series) == 0 {
		t.Fatal("series should not be empty")
	}
}

func TestSeriesSumsEveryProxy(t *testing.T) {
	s := NewStore()
	s.Begin("docker").Done(200, 10, 20, "", "", "GET", "/v2/")
	s.Begin("github").Done(200, 30, 40, "", "", "GET", "/https://github.com/x")

	total, per := s.Series(60, BucketSeconds)
	if len(per) != 2 {
		t.Fatalf("want two proxy series, got %d", len(per))
	}
	if len(total) == 0 {
		t.Fatal("total series should not be empty")
	}
	var requests, bytesIn int64
	for _, p := range total {
		requests += p.Requests
		bytesIn += p.BytesIn
	}
	if requests != 2 || bytesIn != 40 {
		t.Fatalf("total series requests=%d bytesIn=%d", requests, bytesIn)
	}
	for name, series := range per {
		if len(series) == 0 {
			t.Fatalf("series for %s is empty", name)
		}
	}
}

func TestCountersAndRecentByProxy(t *testing.T) {
	s := NewStore()
	s.Increment("dns_hit")
	s.Increment("dns_hit")
	s.IncrementBy("dns_miss", 3)
	counters := s.Counters()
	if counters["dns_hit"] != 2 || counters["dns_miss"] != 3 {
		t.Fatalf("counters %+v", counters)
	}
	s.Begin("dns").Done(0, 40, 80, "", "127.0.0.1:5300", "DNS", "github.com.")
	s.Begin("sni").Done(200, 1, 2, "", "127.0.0.1:6000", "TLS", "github.com")
	recent := s.RecentByProxy("dns", 10)
	if len(recent) != 1 || recent[0].Proxy != "dns" {
		t.Fatalf("recent by proxy %+v", recent)
	}
	if recent[0].Bytes != 80 {
		t.Fatalf("recent bytes %+v", recent[0])
	}
}

func TestDetailJSONShape(t *testing.T) {
	s := NewStore()
	s.Begin("docker").DoneTarget("repo", 200, 5, 6, "", "", "GET", "/v2/repo/blobs/x")
	raw, err := json.Marshal(s.Detail(60, false)[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{"\"success_rate\"", "\"in_flight\"", "\"status_classes\"", "\"latency\"", "\"rates\"", "\"window\"", "\"targets\"", "\"upstream\""} {
		if !strings.Contains(text, key) {
			t.Fatalf("missing key %s in %s", key, text)
		}
	}
}

func TestLatencyPercentilesNeverExceedMax(t *testing.T) {
	s := NewStore()
	call := s.Begin("sni")
	time.Sleep(12 * time.Millisecond)
	call.Done(200, 100, 200, "", "127.0.0.1:6000", "TLS", "github.com")
	d := s.Detail(60, false)[0]
	lat := d.Latency
	if lat.Count != 1 {
		t.Fatalf("latency count %d", lat.Count)
	}
	if lat.Max <= 0 {
		t.Fatalf("max should be sampled, got %d", lat.Max)
	}
	for name, v := range map[string]int64{"p50": lat.P50, "p90": lat.P90, "p99": lat.P99} {
		if v > lat.Max {
			t.Fatalf("%s=%d exceeds max=%d", name, v, lat.Max)
		}
		if v <= 0 {
			t.Fatalf("%s should be positive, got %d", name, v)
		}
	}
}

// Sub-millisecond samples round Max down to 0; the percentiles must follow so a
// card never reports P90 10 ms above Max 0 ms.
func TestLatencyPercentilesClampToZeroMax(t *testing.T) {
	s := NewStore()
	s.Begin("dns").Done(200, 51, 54, "", "127.0.0.1:5300", "DNS", "github.com.")
	lat := s.Detail(60, false)[0].Latency
	if lat.Count != 1 {
		t.Fatalf("latency count %d", lat.Count)
	}
	if lat.Max != 0 {
		t.Fatalf("expected sub-millisecond max to round to 0, got %d", lat.Max)
	}
	for name, v := range map[string]int64{"p50": lat.P50, "p90": lat.P90, "p99": lat.P99} {
		if v != 0 {
			t.Fatalf("%s=%d should be clamped to max 0", name, v)
		}
	}
}

func TestRatesAreRounded(t *testing.T) {
	s := NewStore()
	s.Begin("dns").Done(200, 30, 60, "", "127.0.0.1:5300", "DNS", "github.com.")
	rates := s.Detail(60, false)[0].Rates
	if rates.RequestsPerMin != 0.017 {
		t.Fatalf("requests per minute = %v, want 0.017", rates.RequestsPerMin)
	}
	if rates.FailuresPerMin != 0 {
		t.Fatalf("failures per minute = %v, want 0", rates.FailuresPerMin)
	}
	if rates.BytesInPerSec != 0.008 || rates.BytesOutPerSec != 0.017 {
		t.Fatalf("byte rates = %v/%v", rates.BytesInPerSec, rates.BytesOutPerSec)
	}
	if rates.RequestsPerSec != 0.000 {
		t.Fatalf("requests per second = %v", rates.RequestsPerSec)
	}
}
