package panel

import (
	"net/http"
	"strconv"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

// handleEffect reports what the gateway itself carried in the recent window:
// request volume, success rate, latency split, upstream wait and throughput.
// The gateway is measured on its own traffic, never against a direct path.
func (s *Server) handleEffect(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	minutes := 60
	if raw := r.URL.Query().Get("minutes"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 5 && v <= 60 {
			minutes = v
		}
	}
	details := s.stats.Detail(minutes, false)
	byName := map[string]telemetry.ProxyDetail{}
	for _, d := range details {
		byName[d.Name] = d
	}
	var (
		totalRequests, totalFailures, totalIn, totalOut, totalInFlight int64
		latCount, latSum, latMax, p90Max                               int64
		upCount, upSum                                                 int64
	)
	channels := []map[string]interface{}{}
	specs := []struct {
		name, label, path string
		enabled           bool
	}{
		{"docker", "Docker 镜像代理", "/v2/", s.cfg.Docker.Enabled},
		{"github", "GitHub 下载代理", "/https://", s.cfg.GitHub.Enabled},
		{"dns", "DNS 分流", s.cfg.DNS.Listen, s.cfg.DNS.Enabled},
		{"sni", "SNI 入口", s.cfg.Listen.Entry, s.cfg.SNI.Enabled},
	}
	for _, spec := range specs {
		d := byName[spec.name]
		gatewayMs := int64(0)
		if d.Latency.Count > 0 && d.Upstream.Count > 0 {
			gatewayMs = d.Latency.AvgMillis - d.Upstream.AvgMillis
			if gatewayMs < 0 {
				gatewayMs = 0
			}
		}
		totalRequests += d.Requests
		totalFailures += d.Failures
		totalIn += d.BytesIn
		totalOut += d.BytesOut
		totalInFlight += d.InFlight
		latCount += d.Latency.Count
		latSum += d.Latency.AvgMillis * d.Latency.Count
		if d.Latency.P90 > p90Max {
			p90Max = d.Latency.P90
		}
		if d.Latency.Max > latMax {
			latMax = d.Latency.Max
		}
		upCount += d.Upstream.Count
		upSum += d.Upstream.AvgMillis * d.Upstream.Count
		channels = append(channels, map[string]interface{}{
			"name": spec.name, "label": spec.label, "path": spec.path, "enabled": spec.enabled,
			"requests": d.Requests, "failures": d.Failures, "success_rate": round1(d.SuccessRate),
			"avg_latency_ms": d.Latency.AvgMillis, "p90_ms": d.Latency.P90, "max_latency_ms": d.Latency.Max,
			"upstream_ms": d.Upstream.AvgMillis, "upstream_count": d.Upstream.Count,
			"gateway_ms": gatewayMs, "in_flight": d.InFlight,
			"bytes_in": d.BytesIn, "bytes_out": d.BytesOut,
			"in_per_sec": round1(d.Rates.BytesInPerSec), "out_per_sec": round1(d.Rates.BytesOutPerSec),
			"requests_per_min": round1(d.Rates.RequestsPerMin),
			"status_classes":   d.StatusClasses, "targets": d.Targets,
		})
	}
	avgLatency, avgUpstream := int64(0), int64(0)
	if latCount > 0 {
		avgLatency = latSum / latCount
	}
	if upCount > 0 {
		avgUpstream = upSum / upCount
	}
	gatewayAvg := avgLatency - avgUpstream
	if gatewayAvg < 0 {
		gatewayAvg = 0
	}
	successRate := 100.0
	if totalRequests > 0 {
		successRate = float64(totalRequests-totalFailures) / float64(totalRequests) * 100
	}
	counters := s.stats.Counters()
	hit, miss := counters["dns_hit"], counters["dns_miss"]
	hitRate := 0.0
	if hit+miss > 0 {
		hitRate = float64(hit) / float64(hit+miss) * 100
	}
	windowSeconds := float64(minutes * 60)
	seriesPoints, _ := s.stats.Series(minutes, 60)
	series := make([]map[string]interface{}, 0, len(seriesPoints))
	for _, p := range seriesPoints {
		series = append(series, map[string]interface{}{
			"at": p.At, "requests": p.Requests, "failures": p.Failures,
			"bytes_in": p.BytesIn, "bytes_out": p.BytesOut,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"as_of":          config.NowUTC(),
		"window_minutes": minutes,
		"sample_seconds": 60,
		"totals": map[string]interface{}{
			"requests": totalRequests, "failures": totalFailures,
			"success_rate": round1(successRate), "in_flight": totalInFlight,
			"bytes_in": totalIn, "bytes_out": totalOut,
			"in_per_sec":     round1(float64(totalIn) / windowSeconds),
			"out_per_sec":    round1(float64(totalOut) / windowSeconds),
			"avg_latency_ms": avgLatency, "p90_ms": p90Max, "max_latency_ms": latMax,
			"latency_samples": latCount,
		},
		"overhead": map[string]interface{}{
			"avg_latency_ms": avgLatency, "avg_upstream_ms": avgUpstream,
			"gateway_ms": gatewayAvg, "upstream_samples": upCount,
			"upstream_pct": round1(percentOf(avgUpstream, avgLatency)),
		},
		"dns": map[string]interface{}{
			"hit": hit, "miss": miss, "hit_rate": round1(hitRate), "total": hit + miss,
		},
		"channels":  channels,
		"series":    series,
		"explain":   "统计最近 " + strconv.Itoa(minutes) + " 分钟内网关实际承载的流量；耗时来自请求完成时间，网关转发为总耗时减去上游等待。",
		"generated": time.Now().UTC().Format(time.RFC3339),
	})
}

func percentOf(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
