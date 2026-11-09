package storage

import (
	"context"
	"errors"
)

type QueueKind string

const (
	QueueGeneration QueueKind = "generation"
	QueueAICheck    QueueKind = "ai_check"
)

var ErrAICheckTaskLeaseLost = errors.New("首检任务已由其他执行轮次接管")

type queueAttemptKey struct{}
type queueAttempt struct {
	kind    QueueKind
	id      string
	attempt int
}

// WithQueueAttempt fences every write of a claimed worker to its monotonically
// increasing attempt. WithoutCancel preserves this identity during safe cleanup.
func WithQueueAttempt(ctx context.Context, kind QueueKind, id string, attempt int) context.Context {
	return context.WithValue(ctx, queueAttemptKey{}, queueAttempt{kind, id, attempt})
}

// QueueAttemptFromContext returns zero for legacy non-worker callers. A present
// but invalid/mismatched fence returns -1, which can never match a claimed task.
func QueueAttemptFromContext(ctx context.Context, kind QueueKind, id string) int {
	guard, ok := ctx.Value(queueAttemptKey{}).(queueAttempt)
	if !ok {
		return 0
	}
	if guard.kind != kind || guard.id != id || guard.attempt < 1 {
		return -1
	}
	return guard.attempt
}
