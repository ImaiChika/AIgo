package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	requestClassNormal    = "normal"
	requestClassExpensive = "expensive_read"
	requestClassHeavy     = "heavy"
)

// RequestLimitConfig 是单实例 HTTP 在途请求保护配置。它限制的是同时执行数，
// 不是用户业务频率；达到上限时快速返回 503，让客户端稍后重试。
type RequestLimitConfig struct {
	MaxInFlight          int
	ExpensiveMaxInFlight int
	HeavyMaxInFlight     int
	RetryAfterSeconds    int
}

func (c RequestLimitConfig) normalized() RequestLimitConfig {
	if c.MaxInFlight < 1 {
		c.MaxInFlight = 64
	}
	if c.ExpensiveMaxInFlight < 1 {
		c.ExpensiveMaxInFlight = 8
	}
	if c.HeavyMaxInFlight < 1 {
		c.HeavyMaxInFlight = 2
	}
	if c.RetryAfterSeconds < 1 {
		c.RetryAfterSeconds = 1
	}
	if c.ExpensiveMaxInFlight > c.MaxInFlight {
		c.ExpensiveMaxInFlight = c.MaxInFlight
	}
	if c.HeavyMaxInFlight > c.MaxInFlight {
		c.HeavyMaxInFlight = c.MaxInFlight
	}
	return c
}

type concurrencyGate struct {
	name     string
	slots    chan struct{}
	rejected atomic.Uint64
}

func newConcurrencyGate(name string, limit int) *concurrencyGate {
	return &concurrencyGate{name: name, slots: make(chan struct{}, limit)}
}

func (g *concurrencyGate) tryAcquire() bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		g.rejected.Add(1)
		return false
	}
}

func (g *concurrencyGate) release() { <-g.slots }

type concurrencyGateSnapshot struct {
	Limit    int    `json:"limit"`
	InFlight int    `json:"in_flight"`
	Rejected uint64 `json:"rejected"`
}

func (g *concurrencyGate) snapshot() concurrencyGateSnapshot {
	return concurrencyGateSnapshot{Limit: cap(g.slots), InFlight: len(g.slots), Rejected: g.rejected.Load()}
}

type requestLimiter struct {
	config    RequestLimitConfig
	global    *concurrencyGate
	expensive *concurrencyGate
	heavy     *concurrencyGate
}

func newRequestLimiter(config RequestLimitConfig) *requestLimiter {
	config = config.normalized()
	return &requestLimiter{
		config:    config,
		global:    newConcurrencyGate("global", config.MaxInFlight),
		expensive: newConcurrencyGate(requestClassExpensive, config.ExpensiveMaxInFlight),
		heavy:     newConcurrencyGate(requestClassHeavy, config.HeavyMaxInFlight),
	}
}

// SetRequestLimits 必须在 Handler 首次构建前调用。生产装配在 cmd/aigo/main.go 完成；
// 测试和零值 Server 使用 normalized 中的安全默认值。
func (s *Server) SetRequestLimits(config RequestLimitConfig) {
	s.requestLimitConfig = config
}

func (s *Server) ensureRequestLimiter() {
	s.requestLimiterOnce.Do(func() {
		s.requestLimiter = newRequestLimiter(s.requestLimitConfig)
	})
}

func requestClass(r *http.Request) string {
	path := r.URL.Path
	if r.Method == http.MethodPost {
		switch {
		case path == "/api/knowledge-points/import",
			path == "/api/knowledge-points/export",
			path == "/api/export/xlsx",
			path == "/api/export/docx",
			path == "/api/batch/submit",
			strings.HasPrefix(path, "/api/batch/download/"):
			return requestClassHeavy
		case path == "/api/ai-check/progress":
			return requestClassExpensive
		}
	}
	if r.Method == http.MethodGet {
		switch {
		case strings.HasPrefix(path, "/api/export/download/"):
			return requestClassHeavy
		case path == "/api/stats",
			path == "/api/questions/search",
			path == "/api/review/results",
			path == "/api/ai-check/results":
			return requestClassExpensive
		}
	}
	return requestClassNormal
}

func overloadExempt(r *http.Request) bool {
	return r.URL.Path == "/health/live" || r.URL.Path == "/health/ready"
}

func (l *requestLimiter) classGate(class string) *concurrencyGate {
	switch class {
	case requestClassHeavy:
		return l.heavy
	case requestClassExpensive:
		return l.expensive
	default:
		return nil
	}
}

func (l *requestLimiter) reject(w http.ResponseWriter, gateName string) {
	w.Header().Set("Retry-After", strconv.Itoa(l.config.RetryAfterSeconds))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-AIgo-Overload-Class", gateName)
	writeError(w, http.StatusServiceUnavailable, "系统当前请求较多，请稍后重试")
}

func (l *requestLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if overloadExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		class := requestClass(r)
		if !l.global.tryAcquire() {
			l.reject(w, l.global.name)
			return
		}
		defer l.global.release()

		gate := l.classGate(class)
		if gate == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !gate.tryAcquire() {
			l.reject(w, gate.name)
			return
		}
		defer gate.release()
		next.ServeHTTP(w, r)
	})
}

func (l *requestLimiter) snapshot() map[string]concurrencyGateSnapshot {
	return map[string]concurrencyGateSnapshot{
		"global":              l.global.snapshot(),
		requestClassExpensive: l.expensive.snapshot(),
		requestClassHeavy:     l.heavy.snapshot(),
	}
}
