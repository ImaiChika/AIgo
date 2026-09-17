// ai_check_task.go 提供 AI 检查任务队列的内存实现，语义与 PostgreSQL 版本一致：
// 幂等入队、抢占唯一、租约回收、退避重试、耗尽标记。
package testutil

import (
	"context"
	"sync"
	"time"

	"aigo/internal/domain"
)

type MemoryAICheckTaskStore struct {
	mu    sync.Mutex
	tasks map[string]domain.AICheckTask
	order []string // 按插入顺序记录任务 ID，模拟 ORDER BY created_at
}

func NewMemoryAICheckTaskStore() *MemoryAICheckTaskStore {
	return &MemoryAICheckTaskStore{tasks: make(map[string]domain.AICheckTask)}
}

func (s *MemoryAICheckTaskStore) EnqueueCheckTask(_ context.Context, task domain.AICheckTask) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.QuestionID == task.QuestionID {
			return false, nil
		}
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	task.UpdatedAt = task.CreatedAt
	s.tasks[task.ID] = task
	s.order = append(s.order, task.ID)
	return true, nil
}

func (s *MemoryAICheckTaskStore) claimableLocked(t domain.AICheckTask, now time.Time) bool {
	leasePassed := t.LeasedUntil.IsZero() || !t.LeasedUntil.After(now)
	if t.Status == domain.AICheckTaskPending {
		return leasePassed
	}
	if t.Status == domain.AICheckTaskRunning {
		return !t.LeasedUntil.IsZero() && !t.LeasedUntil.After(now)
	}
	return false
}

func (s *MemoryAICheckTaskStore) ClaimNextCheckTask(_ context.Context, lease time.Duration) (*domain.AICheckTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, id := range s.order {
		t := s.tasks[id]
		if !s.claimableLocked(t, now) {
			continue
		}
		t.Status = domain.AICheckTaskRunning
		t.Attempts++
		t.LeasedUntil = now.Add(lease)
		t.UpdatedAt = now
		s.tasks[id] = t
		clone := t
		return &clone, nil
	}
	return nil, nil
}

func (s *MemoryAICheckTaskStore) CompleteCheckTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil
	}
	t.Status = domain.AICheckTaskSucceeded
	t.LeasedUntil = time.Time{}
	t.LastError = ""
	t.UpdatedAt = time.Now()
	s.tasks[taskID] = t
	return nil
}

func (s *MemoryAICheckTaskStore) FailCheckTask(_ context.Context, taskID string, errMsg string, backoff time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil
	}
	now := time.Now()
	t.LastError = errMsg
	t.UpdatedAt = now
	if t.Attempts >= t.MaxAttempts {
		t.Status = domain.AICheckTaskExhausted
		t.LeasedUntil = time.Time{}
	} else {
		t.Status = domain.AICheckTaskPending
		t.LeasedUntil = now.Add(backoff)
	}
	s.tasks[taskID] = t
	return nil
}

func (s *MemoryAICheckTaskStore) LatestCheckTasksByQuestionIDs(_ context.Context, questionIDs []string) (map[string]domain.AICheckTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]domain.AICheckTask, len(questionIDs))
	// order 末尾是最新任务，倒序首个出现的即为最新
	for i := len(s.order) - 1; i >= 0; i-- {
		t := s.tasks[s.order[i]]
		if _, seen := out[t.QuestionID]; !seen {
			out[t.QuestionID] = t
		}
	}
	return out, nil
}

func (s *MemoryAICheckTaskStore) CountCheckTasksByStatus(_ context.Context) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, t := range s.tasks {
		out[t.Status]++
	}
	return out, nil
}
