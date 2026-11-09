package llm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// EndpointRecovery pauses new calls only after transport failures across distinct
// requests. Quality failures and repeated failure of one prompt never pause peers.
// Keys hash endpoint, credentials and model; independent model routes do not block each other.
type EndpointRecovery struct {
	mu        sync.Mutex
	endpoints map[[32]byte]*endpointFailures
	Pause     time.Duration
}
type endpointFailures struct {
	prompts map[[32]byte]bool
	until   time.Time
}

func NewEndpointRecovery() *EndpointRecovery {
	return &EndpointRecovery{endpoints: map[[32]byte]*endpointFailures{}}
}
func endpointKey(cfg QwenConfig) [32]byte {
	return sha256.Sum256([]byte(strings.TrimRight(cfg.BaseURL, "/") + "\x00" + cfg.APIKey + "\x00" + cfg.Model))
}
func (g *EndpointRecovery) remaining(cfg QwenConfig) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.endpoints[endpointKey(cfg)]; e != nil {
		return time.Until(e.until)
	}
	return 0
}
func (g *EndpointRecovery) wait(ctx context.Context, cfg QwenConfig) error {
	for {
		d := g.remaining(cfg)
		if d <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (g *EndpointRecovery) observe(cfg QwenConfig, prompt [32]byte, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := endpointKey(cfg)
	if err == nil {
		delete(g.endpoints, key)
		return
	}
	if !endpointTransportFailure(err) {
		return
	}
	e := g.endpoints[key]
	if e == nil {
		e = &endpointFailures{prompts: map[[32]byte]bool{}}
		g.endpoints[key] = e
	}
	e.prompts[prompt] = true
	if len(e.prompts) >= 3 {
		pause := g.Pause
		if pause <= 0 {
			pause = 5 * time.Second
		}
		e.until = time.Now().Add(pause)
		slog.Warn("模型服务连续故障，暂缓该端点的新调用", "retry_after", pause.String())
	}
}
func endpointTransportFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var h *HTTPStatusError
	if errors.As(err, &h) {
		return h.Status == 429 || h.Status == 502 || h.Status == 503 || h.Status == 504
	}
	var n net.Error
	return errors.As(err, &n)
}

type HTTPStatusError struct {
	RetryAfter time.Duration
	Status     int
	Message    string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("推理接口 HTTP %d: %s", e.Status, e.Message)
}
