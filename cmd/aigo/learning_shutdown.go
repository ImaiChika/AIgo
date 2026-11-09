package main

import (
	"context"
	"log/slog"
	"time"
)

// stopLearningAndWait keeps the database alive while the canceled analysis
// finishes its safe result/accounting write. Uncooperative clients cannot hold
// process shutdown indefinitely.
func stopLearningAndWait(cancel context.CancelFunc, done <-chan struct{}, budget time.Duration) bool {
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return true
	}
	if budget <= 0 || budget > 5*time.Second {
		budget = 5 * time.Second
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		slog.Warn("学习后台任务关闭等待超时，后续按持久租约恢复")
		return false
	}
}
