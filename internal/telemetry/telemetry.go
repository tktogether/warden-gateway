package telemetry

import (
	"sync"
	"sync/atomic"
	"time"
)

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

type entry struct {
	requests   int64
	failures   int64
	bytesIn    int64
	bytesOut   int64
	lastAt     int64
	lastStatus int
	lastError  string
}

type Store struct {
	mu      sync.RWMutex
	started time.Time
	proxies map[string]*entry
	recent  []LogEntry
	maxLog  int
}

func NewStore() *Store {
	return &Store{
		started: time.Now(),
		proxies: map[string]*entry{},
		maxLog:  300,
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

func (s *Store) Begin(name string) *Call {
	e := s.get(name)
	atomic.AddInt64(&e.requests, 1)
	return &Call{store: s, name: name, started: time.Now()}
}

func (s *Store) add(name string, status int, bytesIn, bytesOut int64, errText, remote, method, path string, ms int64) {
	e := s.get(name)
	atomic.AddInt64(&e.bytesIn, bytesIn)
	atomic.AddInt64(&e.bytesOut, bytesOut)
	if status >= 500 || status == 0 || errText != "" {
		atomic.AddInt64(&e.failures, 1)
	}
	atomic.StoreInt64(&e.lastAt, time.Now().Unix())
	s.mu.Lock()
	e.lastStatus = status
	e.lastError = errText
	s.recent = append(s.recent, LogEntry{
		At: time.Now().Unix(), Proxy: name, Method: method, Path: path,
		Status: status, Bytes: bytesOut, Millis: ms, Remote: remote, Error: errText,
	})
	if len(s.recent) > s.maxLog {
		s.recent = s.recent[len(s.recent)-s.maxLog:]
	}
	s.mu.Unlock()
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

type Call struct {
	store   *Store
	name    string
	started time.Time
	done    bool
}

func (c *Call) Done(status int, bytesIn, bytesOut int64, errText, remote, method, path string) {
	if c == nil || c.done {
		return
	}
	c.done = true
	c.store.add(c.name, status, bytesIn, bytesOut, errText, remote, method, path, time.Since(c.started).Milliseconds())
}
