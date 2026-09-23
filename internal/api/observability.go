package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 固定延迟桶既能近似观察 p95/p99，也避免为每个请求保存样本造成无界内存增长。
var requestDurationBuckets = [...]time.Duration{
	5 * time.Millisecond,
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2500 * time.Millisecond,
	5 * time.Second,
	30 * time.Second,
}

type requestIDContextKey struct{}

// RequestID 返回当前 HTTP 请求的关联 ID，供后续业务日志复用。
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

type routeMetric struct {
	Requests       uint64
	ClientErrors   uint64
	ServerErrors   uint64
	RateLimited    uint64
	Unavailable    uint64
	ResponseBytes  uint64
	DurationTotal  time.Duration
	DurationMax    time.Duration
	DurationBucket [len(requestDurationBuckets) + 1]uint64
}

type httpMetrics struct {
	startedAt time.Time
	inFlight  atomic.Int64
	total     atomic.Uint64
	mu        sync.Mutex
	routes    map[string]*routeMetric
}

func newHTTPMetrics() *httpMetrics {
	return &httpMetrics{startedAt: time.Now(), routes: make(map[string]*routeMetric)}
}

func (m *httpMetrics) observe(route string, status int, responseBytes int64, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	m.total.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	metric := m.routes[route]
	if metric == nil {
		metric = &routeMetric{}
		m.routes[route] = metric
	}
	metric.Requests++
	if status >= 400 && status < 500 {
		metric.ClientErrors++
	}
	if status >= 500 {
		metric.ServerErrors++
	}
	if status == http.StatusTooManyRequests {
		metric.RateLimited++
	}
	if status == http.StatusServiceUnavailable {
		metric.Unavailable++
	}
	if responseBytes > 0 {
		metric.ResponseBytes += uint64(responseBytes)
	}
	metric.DurationTotal += duration
	if duration > metric.DurationMax {
		metric.DurationMax = duration
	}
	bucket := len(requestDurationBuckets)
	for i, upper := range requestDurationBuckets {
		if duration <= upper {
			bucket = i
			break
		}
	}
	metric.DurationBucket[bucket]++
}

type durationBucketSnapshot struct {
	LEMS  int64  `json:"le_ms,omitempty"`
	Label string `json:"label,omitempty"`
	Count uint64 `json:"count"`
}

type routeMetricSnapshot struct {
	Route             string                   `json:"route"`
	Requests          uint64                   `json:"requests"`
	ClientErrors      uint64                   `json:"client_errors"`
	ServerErrors      uint64                   `json:"server_errors"`
	RateLimited       uint64                   `json:"rate_limited"`
	Unavailable       uint64                   `json:"unavailable"`
	ResponseBytes     uint64                   `json:"response_bytes"`
	AverageDurationMS float64                  `json:"average_duration_ms"`
	MaxDurationMS     float64                  `json:"max_duration_ms"`
	DurationBuckets   []durationBucketSnapshot `json:"duration_buckets"`
}

type httpMetricSnapshot struct {
	StartedAt     time.Time             `json:"started_at"`
	UptimeSeconds int64                 `json:"uptime_seconds"`
	InFlight      int64                 `json:"in_flight"`
	Requests      uint64                `json:"requests"`
	Routes        []routeMetricSnapshot `json:"routes"`
}

func (m *httpMetrics) snapshot() httpMetricSnapshot {
	result := httpMetricSnapshot{
		StartedAt:     m.startedAt,
		UptimeSeconds: int64(time.Since(m.startedAt).Seconds()),
		InFlight:      m.inFlight.Load(),
		Requests:      m.total.Load(),
	}
	m.mu.Lock()
	for route, metric := range m.routes {
		item := routeMetricSnapshot{
			Route:           route,
			Requests:        metric.Requests,
			ClientErrors:    metric.ClientErrors,
			ServerErrors:    metric.ServerErrors,
			RateLimited:     metric.RateLimited,
			Unavailable:     metric.Unavailable,
			ResponseBytes:   metric.ResponseBytes,
			MaxDurationMS:   durationMilliseconds(metric.DurationMax),
			DurationBuckets: make([]durationBucketSnapshot, 0, len(metric.DurationBucket)),
		}
		if metric.Requests > 0 {
			item.AverageDurationMS = durationMilliseconds(metric.DurationTotal) / float64(metric.Requests)
		}
		for i, count := range metric.DurationBucket {
			bucket := durationBucketSnapshot{Count: count}
			if i < len(requestDurationBuckets) {
				bucket.LEMS = requestDurationBuckets[i].Milliseconds()
			} else {
				bucket.Label = "+Inf"
			}
			item.DurationBuckets = append(item.DurationBuckets, bucket)
		}
		result.Routes = append(result.Routes, item)
	}
	m.mu.Unlock()
	sort.Slice(result.Routes, func(i, j int) bool { return result.Routes[i].Route < result.Routes[j].Route })
	return result
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Unwrap 让 http.ResponseController 仍可访问底层 ResponseWriter 的可选能力。
func (w *statusResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	// crypto/rand 在正常系统上不会失败；保守回退仍保证进程内关联性。
	return time.Now().UTC().Format("20060102T150405.000000000")
}

func (s *Server) ensureObservability() {
	s.observabilityOnce.Do(func() {
		s.httpMetrics = newHTTPMetrics()
	})
}

func (s *Server) withRequestObservability(next http.Handler) http.Handler {
	s.ensureObservability()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", requestID)

		recorder := &statusResponseWriter{ResponseWriter: w}
		startedAt := time.Now()
		s.httpMetrics.inFlight.Add(1)
		defer func() {
			s.httpMetrics.inFlight.Add(-1)
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			duration := time.Since(startedAt)
			route := r.Pattern
			s.httpMetrics.observe(route, status, recorder.bytes, duration)
			if route == "GET /health/live" || route == "GET /health/ready" {
				return
			}
			level := slog.LevelInfo
			if status >= 500 {
				level = slog.LevelError
			} else if status >= 400 {
				level = slog.LevelWarn
			}
			slog.LogAttrs(ctx, level, "http_request",
				slog.String("request_id", requestID),
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", status),
				slog.Int64("duration_ms", duration.Milliseconds()),
				slog.Int64("response_bytes", recorder.bytes),
			)
		}()

		next.ServeHTTP(recorder, r)
	})
}

type databasePoolSnapshot struct {
	MaxOpenConnections int   `json:"max_open_connections"`
	OpenConnections    int   `json:"open_connections"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	WaitCount          int64 `json:"wait_count"`
	WaitDurationMS     int64 `json:"wait_duration_ms"`
	MaxIdleClosed      int64 `json:"max_idle_closed"`
	MaxIdleTimeClosed  int64 `json:"max_idle_time_closed"`
	MaxLifetimeClosed  int64 `json:"max_lifetime_closed"`
}

type processMetricSnapshot struct {
	GoVersion        string `json:"go_version"`
	GOMAXPROCS       int    `json:"gomaxprocs"`
	Goroutines       int    `json:"goroutines"`
	HeapAllocBytes   uint64 `json:"heap_alloc_bytes"`
	HeapInUseBytes   uint64 `json:"heap_in_use_bytes"`
	HeapObjects      uint64 `json:"heap_objects"`
	StackInUseBytes  uint64 `json:"stack_in_use_bytes"`
	SysBytes         uint64 `json:"sys_bytes"`
	TotalAllocBytes  uint64 `json:"total_alloc_bytes"`
	GCCount          uint32 `json:"gc_count"`
	LastGCPauseNanos uint64 `json:"last_gc_pause_nanos"`
}

func readProcessMetrics() processMetricSnapshot {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var lastPause uint64
	if memory.NumGC > 0 {
		lastPause = memory.PauseNs[(memory.NumGC-1)%uint32(len(memory.PauseNs))]
	}
	return processMetricSnapshot{
		GoVersion: runtime.Version(), GOMAXPROCS: runtime.GOMAXPROCS(0), Goroutines: runtime.NumGoroutine(),
		HeapAllocBytes: memory.HeapAlloc, HeapInUseBytes: memory.HeapInuse, HeapObjects: memory.HeapObjects,
		StackInUseBytes: memory.StackInuse, SysBytes: memory.Sys, TotalAllocBytes: memory.TotalAlloc,
		GCCount: memory.NumGC, LastGCPauseNanos: lastPause,
	}
}

func databasePoolMetrics(db *sql.DB) databasePoolSnapshot {
	stats := db.Stats()
	return databasePoolSnapshot{
		MaxOpenConnections: stats.MaxOpenConnections,
		OpenConnections:    stats.OpenConnections,
		InUse:              stats.InUse,
		Idle:               stats.Idle,
		WaitCount:          stats.WaitCount,
		WaitDurationMS:     stats.WaitDuration.Milliseconds(),
		MaxIdleClosed:      stats.MaxIdleClosed,
		MaxIdleTimeClosed:  stats.MaxIdleTimeClosed,
		MaxLifetimeClosed:  stats.MaxLifetimeClosed,
	}
}

// handleRuntimeMetrics 返回脱敏的进程、HTTP、数据库连接池和持久任务队列快照。
// 路由由 requireSuperAdmin 保护，不暴露请求参数、用户数据、DSN 或模型凭证。
func (s *Server) handleRuntimeMetrics(w http.ResponseWriter, r *http.Request) {
	s.ensureObservability()
	s.ensureRequestLimiter()
	response := map[string]any{
		"generated_at": time.Now().UTC(),
		"http":         s.httpMetrics.snapshot(),
		"process":      readProcessMetrics(),
		"overload":     s.requestLimiter.snapshot(),
	}
	if provider, ok := s.questionStore.(interface{ DB() *sql.DB }); ok && provider.DB() != nil {
		response["database_pool"] = databasePoolMetrics(provider.DB())
	}
	queues := map[string]any{}
	if s.aiCheckSvc != nil {
		if counts, err := s.aiCheckSvc.TaskSummary(r.Context()); err == nil {
			queues["ai_check"] = counts
		} else {
			queues["ai_check"] = map[string]string{"status": "unavailable"}
		}
	}
	if s.generationRunStore != nil {
		if counts, err := s.generationRunStore.CountGenerationRunsByStatus(r.Context()); err == nil {
			queues["generation"] = counts
		} else {
			queues["generation"] = map[string]string{"status": "unavailable"}
		}
	}
	response["queues"] = queues
	writeJSON(w, http.StatusOK, response)
}
