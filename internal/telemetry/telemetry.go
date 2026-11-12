package telemetry

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BucketSeconds is the resolution of the rolling window kept per proxy.
const BucketSeconds = 10

// BucketCount is how many buckets are retained (one hour at BucketSeconds).
const BucketCount = 360

const latencyBucketCount = 12

// latencyBounds holds the upper bound of each latency histogram bucket in
// milliseconds; the last entry covers everything above 30s.
var latencyBounds = [latencyBucketCount]int64{10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 1 << 62}

var latencyLabels = [latencyBucketCount]string{"≤10ms", "≤25ms", "≤50ms", "≤100ms", "≤250ms", "≤500ms", "≤1s", "≤2.5s", "≤5s", "≤10s", "≤30s", ">30s"}

type ProxyStats struct {
	Name       string `json:"name"`
	Requests   int64  `json:"requests"`
	Failures   int64  `json:"failures"`
	BytesIn    int64  `json:"bytes_in"`
	BytesOut   int64  `json:"bytes_out"`
	LastStatus int    `json:"last_status"`
	LastAt     int64  `json:"last_at"`
	LastError  string `json:"last_error,omitempty"`
}

type LogEntry struct {
	At     int64  `json:"at"`
	Proxy  string `json:"proxy"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Bytes  int64  `json:"bytes"`
	Millis int64  `json:"millis"`
	Remote string `json:"remote,omitempty"`
	Error  string `json:"error,omitempty"`
}

// WindowPoint is one aggregated step of the rolling proxy window.
type WindowPoint struct {
	At           int64 `json:"at"`
	Requests     int64 `json:"requests"`
	Failures     int64 `json:"failures"`
	BytesIn      int64 `json:"bytes_in"`
	BytesOut     int64 `json:"bytes_out"`
	LatencyCount int64 `json:"latency_count,omitempty"`
	LatencySum   int64 `json:"latency_sum,omitempty"`
	LatencyMax   int64 `json:"latency_max,omitempty"`
}

// HistBin is one barrel of the latency histogram.
type HistBin struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// Latency summarises response times inside the requested window.
type Latency struct {
	Count     int64     `json:"count"`
	AvgMillis int64     `json:"avg_millis"`
	P50       int64     `json:"p50"`
	P90       int64     `json:"p90"`
	P99       int64     `json:"p99"`
	Max       int64     `json:"max"`
	Histogram []HistBin `json:"histogram"`
}

// Rates converts the cumulative counters into per-minute and per-second speeds.
type Rates struct {
	RequestsPerMin float64 `json:"requests_per_min"`
	FailuresPerMin float64 `json:"failures_per_min"`
	BytesInPerSec  float64 `json:"bytes_in_per_sec"`
	BytesOutPerSec float64 `json:"bytes_out_per_sec"`
	RequestsPerSec float64 `json:"requests_per_sec"`
}

// TargetStat aggregates traffic for one upstream target (docker repository,
// GitHub host, DNS name, TLS SNI name).
type TargetStat struct {
	Target   string `json:"target"`
	Requests int64  `json:"requests"`
	Failures int64  `json:"failures"`
	Bytes    int64  `json:"bytes"`
	LastAt   int64  `json:"last_at"`
}

// maxTargets bounds the per-proxy target table so a hostile client cannot grow
// memory without limit.
const maxTargets = 256

// ProxyDetail is the per-proxy observability payload served to the panel.
type ProxyDetail struct {
	ProxyStats
	SuccessRate   float64          `json:"success_rate"`
	InFlight      int64            `json:"in_flight"`
	StatusClasses map[string]int64 `json:"status_classes"`
	Latency       Latency          `json:"latency"`
	Rates         Rates            `json:"rates"`
	Window        WindowPoint      `json:"window"`
	Series        []WindowPoint    `json:"series,omitempty"`
	Targets       []TargetStat     `json:"targets,omitempty"`
	Upstream      UpstreamLatency  `json:"upstream"`
}

// UpstreamLatency separates the time spent waiting for the upstream server from
// the time spent streaming the body through the proxy.
type UpstreamLatency struct {
	Count     int64 `json:"count"`
	AvgMillis int64 `json:"avg_millis"`
}

type bucket struct {
	at       int64
	requests int64
	failures int64
	bytesIn  int64
	bytesOut int64
	latCount int64
	latSum   int64
	latMax   int64
	hist     [latencyBucketCount]int64
}

type entry struct {
	requests       int64
	failures       int64
	bytesIn        int64
	bytesOut       int64
	lastAt         int64
	lastStatus     int
	lastError      string
	inFlight       int64
	s2, s3, s4, s5 int64
	latCount       int64
	latSum         int64
	latMax         int64
	upCount        int64
	upSum          int64
	hist           [latencyBucketCount]int64
	buckets        []bucket
	targets        map[string]*TargetStat
}

type Store struct {
	mu       sync.RWMutex
	started  time.Time
	proxies  map[string]*entry
	recent   []LogEntry
	maxLog   int
	counters map[string]int64
}

func NewStore() *Store {
	return &Store{
		started:  time.Now(),
		proxies:  map[string]*entry{},
		maxLog:   300,
		counters: map[string]int64{},
	}
}

func (s *Store) Started() time.Time { return s.started }

func (s *Store) get(name string) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.proxies[name]
	if !ok {
		e = &entry{}
		s.proxies[name] = e
	}
	return e
}

// Increment advances a named free-form counter (used for DNS split hits and
// other events that are not full proxy requests).
func (s *Store) Increment(name string) {
	s.mu.Lock()
	s.counters[name]++
	s.mu.Unlock()
}

// IncrementBy advances a named counter by delta.
func (s *Store) IncrementBy(name string, delta int64) {
	if delta == 0 {
		return
	}
	s.mu.Lock()
	s.counters[name] += delta
	s.mu.Unlock()
}

// Counters returns a copy of every named counter.
func (s *Store) Counters() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int64, len(s.counters))
	for k, v := range s.counters {
		out[k] = v
	}
	return out
}

func (s *Store) Begin(name string) *Call {
	e := s.get(name)
	atomic.AddInt64(&e.requests, 1)
	atomic.AddInt64(&e.inFlight, 1)
	return &Call{store: s, name: name, started: time.Now()}
}

func (s *Store) add(name, target string, status int, bytesIn, bytesOut int64, errText, remote, method, path string, ms, upstreamMs int64) {
	e := s.get(name)
	atomic.AddInt64(&e.bytesIn, bytesIn)
	atomic.AddInt64(&e.bytesOut, bytesOut)
	now := time.Now()
	if status >= 500 || status == 0 || errText != "" {
		atomic.AddInt64(&e.failures, 1)
	}
	atomic.StoreInt64(&e.lastAt, now.Unix())
	latencyIndex := latencyBucket(ms)
	s.mu.Lock()
	e.lastStatus = status
	e.lastError = errText
	switch {
	case status >= 500 || status == 0:
		e.s5++
	case status >= 400:
		e.s4++
	case status >= 300:
		e.s3++
	case status >= 200:
		e.s2++
	}
	e.latCount++
	e.latSum += ms
	if ms > e.latMax {
		e.latMax = ms
	}
	e.hist[latencyIndex]++
	if upstreamMs > 0 {
		e.upCount++
		e.upSum += upstreamMs
	}
	b := e.bucketAt(now.Unix() / BucketSeconds * BucketSeconds)
	if b != nil {
		b.requests++
		if status >= 500 || status == 0 || errText != "" {
			b.failures++
		}
		b.bytesIn += bytesIn
		b.bytesOut += bytesOut
		b.latCount++
		b.latSum += ms
		if ms > b.latMax {
			b.latMax = ms
		}
		b.hist[latencyIndex]++
	}
	e.recordTarget(target, status, bytesIn+bytesOut, now.Unix())
	s.recent = append(s.recent, LogEntry{
		At: now.Unix(), Proxy: name, Method: method, Path: path,
		Status: status, Bytes: bytesOut, Millis: ms, Remote: remote, Error: errText,
	})
	if len(s.recent) > s.maxLog {
		s.recent = s.recent[len(s.recent)-s.maxLog:]
	}
	s.mu.Unlock()
}

// recordTarget keeps a bounded per-target traffic table for one proxy. Callers
// must hold the store lock.
func (e *entry) recordTarget(target string, status int, bytes int64, at int64) {
	target = normalizeTarget(target)
	if target == "" {
		return
	}
	if e.targets == nil {
		e.targets = map[string]*TargetStat{}
	}
	t, ok := e.targets[target]
	if !ok {
		if len(e.targets) >= maxTargets {
			return
		}
		t = &TargetStat{Target: target}
		e.targets[target] = t
	}
	t.Requests++
	if status >= 500 || status == 0 {
		t.Failures++
	}
	t.Bytes += bytes
	t.LastAt = at
}

// targetList returns the busiest targets of one proxy, largest traffic first.
func (e *entry) targetList(limit int) []TargetStat {
	if len(e.targets) == 0 {
		return nil
	}
	out := make([]TargetStat, 0, len(e.targets))
	for _, t := range e.targets {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Requests > out[j].Requests
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// normalizeTarget trims a raw target label to a bounded single line string.
func normalizeTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	for _, r := range target {
		if r < 32 || r == 127 {
			return ""
		}
	}
	if len(target) > 96 {
		target = target[:96]
	}
	return target
}

// bucketAt returns the bucket for at, appending a fresh bucket when the window
// moves on. Callers must hold the store lock.
func (e *entry) bucketAt(at int64) *bucket {
	if n := len(e.buckets); n > 0 {
		last := &e.buckets[n-1]
		if last.at == at {
			return last
		}
		if last.at > at {
			// A late Done from an older call lands in the last bucket.
			return last
		}
	}
	e.buckets = append(e.buckets, bucket{at: at})
	if len(e.buckets) > BucketCount {
		e.buckets = e.buckets[len(e.buckets)-BucketCount:]
	}
	return &e.buckets[len(e.buckets)-1]
}

func latencyBucket(ms int64) int {
	for i, bound := range latencyBounds {
		if ms <= bound {
			return i
		}
	}
	return latencyBucketCount - 1
}

func (s *Store) Snapshot() []ProxyStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProxyStats, 0, len(s.proxies))
	for name, e := range s.proxies {
		out = append(out, ProxyStats{
			Name:       name,
			Requests:   atomic.LoadInt64(&e.requests),
			Failures:   atomic.LoadInt64(&e.failures),
			BytesIn:    atomic.LoadInt64(&e.bytesIn),
			BytesOut:   atomic.LoadInt64(&e.bytesOut),
			LastStatus: e.lastStatus,
			LastAt:     atomic.LoadInt64(&e.lastAt),
			LastError:  e.lastError,
		})
	}
	return out
}

// Detail returns windowed statistics for every proxy that saw traffic. Series
// is only populated when withSeries is true; callers normally fetch it through
// Series to keep poll responses small.
func (s *Store) Detail(minutes int, withSeries bool) []ProxyDetail {
	if minutes <= 0 {
		minutes = 60
	}
	if minutes > BucketCount*BucketSeconds/60 {
		minutes = BucketCount * BucketSeconds / 60
	}
	now := time.Now()
	cutoff := now.Unix() - int64(minutes*60)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProxyDetail, 0, len(s.proxies))
	for name, e := range s.proxies {
		d := ProxyDetail{ProxyStats: ProxyStats{
			Name:       name,
			Requests:   atomic.LoadInt64(&e.requests),
			Failures:   atomic.LoadInt64(&e.failures),
			BytesIn:    atomic.LoadInt64(&e.bytesIn),
			BytesOut:   atomic.LoadInt64(&e.bytesOut),
			LastStatus: e.lastStatus,
			LastAt:     atomic.LoadInt64(&e.lastAt),
			LastError:  e.lastError,
		}}
		d.InFlight = atomic.LoadInt64(&e.inFlight)
		d.StatusClasses = map[string]int64{
			"2xx": e.s2, "3xx": e.s3, "4xx": e.s4, "5xx": e.s5,
		}
		if d.Requests > 0 {
			d.SuccessRate = float64(d.Requests-d.Failures) / float64(d.Requests) * 100
		}
		win, hist := e.window(cutoff)
		d.Window = win
		if e.upCount > 0 {
			d.Upstream = UpstreamLatency{Count: e.upCount, AvgMillis: e.upSum / e.upCount}
		}
		d.Rates = ratesOf(win, minutes)
		d.Latency = latencySummary(hist, win.LatencyCount, win.LatencySum, win.LatencyMax)
		if withSeries {
			d.Series = e.series(cutoff, BucketSeconds)
		}
		d.Targets = e.targetList(8)
		out = append(out, d)
	}
	return out
}

// Series aggregates the rolling window into fixed steps. The first value is the
// sum over every proxy, the second maps each proxy to its own series.
func (s *Store) Series(minutes, stepSeconds int) ([]WindowPoint, map[string][]WindowPoint) {
	if minutes <= 0 {
		minutes = 60
	}
	if minutes > BucketCount*BucketSeconds/60 {
		minutes = BucketCount * BucketSeconds / 60
	}
	if stepSeconds < BucketSeconds {
		stepSeconds = BucketSeconds
	}
	stepSeconds = stepSeconds / BucketSeconds * BucketSeconds
	now := time.Now()
	cutoff := now.Unix() - int64(minutes*60)
	total := make([]WindowPoint, 0, minutes*60/stepSeconds)
	per := make(map[string][]WindowPoint, len(s.proxies))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, e := range s.proxies {
		series := e.series(cutoff, int64(stepSeconds))
		per[name] = series
		if len(total) < len(series) {
			total = make([]WindowPoint, len(series))
			for i := range total {
				total[i] = WindowPoint{At: series[i].At}
			}
		}
		for i, p := range series {
			total[i].Requests += p.Requests
			total[i].Failures += p.Failures
			total[i].BytesIn += p.BytesIn
			total[i].BytesOut += p.BytesOut
			total[i].LatencyCount += p.LatencyCount
			total[i].LatencySum += p.LatencySum
			if p.LatencyMax > total[i].LatencyMax {
				total[i].LatencyMax = p.LatencyMax
			}
		}
	}
	return total, per
}

// window sums every bucket at or after cutoff and returns its histogram.
func (e *entry) window(cutoff int64) (WindowPoint, []int64) {
	hist := make([]int64, latencyBucketCount)
	var out WindowPoint
	for _, b := range e.buckets {
		if b.at < cutoff {
			continue
		}
		out.Requests += b.requests
		out.Failures += b.failures
		out.BytesIn += b.bytesIn
		out.BytesOut += b.bytesOut
		out.LatencyCount += b.latCount
		out.LatencySum += b.latSum
		if b.latMax > out.LatencyMax {
			out.LatencyMax = b.latMax
		}
		for i := range b.hist {
			hist[i] += b.hist[i]
		}
	}
	return out, hist
}

// series builds a gap-filled timeline with the given step.
func (e *entry) series(cutoff int64, step int64) []WindowPoint {
	if step <= 0 {
		step = BucketSeconds
	}
	start := cutoff / step * step
	end := time.Now().Unix()
	count := int((end-start)/step) + 1
	if count <= 0 {
		count = 1
	}
	out := make([]WindowPoint, count)
	for i := range out {
		out[i].At = start + int64(i)*step
	}
	for _, b := range e.buckets {
		if b.at < start {
			continue
		}
		idx := int((b.at - start) / step)
		if idx < 0 || idx >= count {
			continue
		}
		out[idx].Requests += b.requests
		out[idx].Failures += b.failures
		out[idx].BytesIn += b.bytesIn
		out[idx].BytesOut += b.bytesOut
		out[idx].LatencyCount += b.latCount
		out[idx].LatencySum += b.latSum
		if b.latMax > out[idx].LatencyMax {
			out[idx].LatencyMax = b.latMax
		}
	}
	return out
}

func latencySummary(hist []int64, count, sum, max int64) Latency {
	out := Latency{Count: count, Max: max}
	if count > 0 {
		out.AvgMillis = sum / count
	}
	out.P50 = histogramPercentile(hist, count, 50)
	out.P90 = histogramPercentile(hist, count, 90)
	out.P99 = histogramPercentile(hist, count, 99)
	out.Histogram = make([]HistBin, latencyBucketCount)
	for i := range out.Histogram {
		var c int64
		if i < len(hist) {
			c = hist[i]
		}
		out.Histogram[i] = HistBin{Label: latencyLabels[i], Count: c}
	}
	return out
}

func histogramPercentile(hist []int64, count int64, p float64) int64 {
	if count <= 0 {
		return 0
	}
	target := int64(float64(count)*p/100 + 0.5)
	if target < 1 {
		target = 1
	}
	var cum int64
	for i := 0; i < latencyBucketCount; i++ {
		var v int64
		if i < len(hist) {
			v = hist[i]
		}
		cum += v
		if cum >= target {
			bound := latencyBounds[i]
			if bound > 30000 {
				bound = 30000
			}
			return bound
		}
	}
	return 30000
}

func ratesOf(w WindowPoint, minutes int) Rates {
	seconds := float64(minutes * 60)
	if seconds <= 0 {
		return Rates{}
	}
	return Rates{
		RequestsPerMin: float64(w.Requests) / seconds * 60,
		FailuresPerMin: float64(w.Failures) / seconds * 60,
		RequestsPerSec: float64(w.Requests) / seconds,
		BytesInPerSec:  float64(w.BytesIn) / seconds,
		BytesOutPerSec: float64(w.BytesOut) / seconds,
	}
}

func (s *Store) Recent(limit int) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.recent) {
		limit = len(s.recent)
	}
	out := make([]LogEntry, limit)
	copy(out, s.recent[len(s.recent)-limit:])
	return out
}

// RecentByProxy filters the recent ring to one proxy name.
func (s *Store) RecentByProxy(name string, limit int) []LogEntry {
	s.mu.RLock()
	all := s.recent
	out := make([]LogEntry, 0, limit)
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if all[i].Proxy == name {
			out = append(out, all[i])
		}
	}
	s.mu.RUnlock()
	// reverse back to chronological order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

type Call struct {
	store   *Store
	name    string
	started time.Time
	done    bool
}

func (c *Call) Done(status int, bytesIn, bytesOut int64, errText, remote, method, path string) {
	c.DoneTarget("", status, bytesIn, bytesOut, errText, remote, method, path)
}

// DoneTarget is Done plus a logical target label (repository, host or domain)
// used by the per-target traffic table.
func (c *Call) DoneTarget(target string, status int, bytesIn, bytesOut int64, errText, remote, method, path string) {
	c.DoneUpstream(target, 0, status, bytesIn, bytesOut, errText, remote, method, path)
}

// DoneUpstream is DoneTarget plus the time spent waiting for the upstream
// server, which separates upstream latency from client transfer time.
func (c *Call) DoneUpstream(target string, upstreamMs int64, status int, bytesIn, bytesOut int64, errText, remote, method, path string) {
	if c == nil || c.done {
		return
	}
	c.done = true
	elapsed := time.Since(c.started).Milliseconds()
	if upstreamMs <= 0 || upstreamMs > elapsed {
		upstreamMs = 0
	}
	atomic.AddInt64(&c.store.get(c.name).inFlight, -1)
	c.store.add(c.name, target, status, bytesIn, bytesOut, errText, remote, method, path, elapsed, upstreamMs)
}
