package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"aigo/internal/llm"
)

// GenerationQuota 控制提交时的任务准入；所有数值是全站配置，修改后立即生效。
// GlobalPendingQuestions 统计尚未完成的生成单元与首次 AI 检查任务，
// 同一道单题生成运行收尾和首检短暂交叠时只计算一次。
type GenerationQuota struct {
	LearningConcurrency int `json:"learning_concurrency,omitempty"`
	// nil on old callers means preserve the saved policy, not reset it.
	ModelTools             *llm.NetworkTools `json:"model_tools,omitempty"`
	GenerationConcurrency  int               `json:"generation_concurrency,omitempty"`
	BatchConcurrency       int               `json:"batch_concurrency,omitempty"`
	AICheckConcurrency     int               `json:"ai_check_concurrency,omitempty"`
	Revision               int64             `json:"revision,omitempty"`
	SingleMaxQuestions     int               `json:"single_max_questions"`
	BatchMaxQuestions      int               `json:"batch_max_questions"`
	SingleActivePerUser    int               `json:"single_active_per_user"`
	BatchActivePerUser     int               `json:"batch_active_per_user"`
	GlobalPendingQuestions int               `json:"global_pending_questions"`
}

func DefaultGenerationQuota() GenerationQuota {
	return GenerationQuota{LearningConcurrency: 2, SingleMaxQuestions: 20, BatchMaxQuestions: 100,
		SingleActivePerUser: 2, BatchActivePerUser: 1, GlobalPendingQuestions: 500,
		GenerationConcurrency: 2, BatchConcurrency: 2, AICheckConcurrency: 2, Revision: 1}
}

func (q GenerationQuota) Validate() error {
	if q.LearningConcurrency < 0 || q.LearningConcurrency > 8 {
		return errors.New("AI总结并发应为1—8；省略时保留现值")
	}
	if q.ModelTools != nil {
		if err := q.ModelTools.Validate(); err != nil {
			return err
		}
	}
	checks := []struct {
		name            string
		value, min, max int
	}{
		{"单题生成并发", q.GenerationConcurrency, 1, 32},
		{"批量生成并发", q.BatchConcurrency, 1, 32},
		{"AI首检并发", q.AICheckConcurrency, 1, 32},
		{"单题每次题数", q.SingleMaxQuestions, 1, 20},
		{"批量每任务题数", q.BatchMaxQuestions, 1, 500},
		{"每人未完成单题任务数", q.SingleActivePerUser, 1, 10},
		{"每人活跃批量任务数", q.BatchActivePerUser, 1, 5},
		{"全站未完成出题/检查量", q.GlobalPendingQuestions, 20, 5000},
	}
	for _, c := range checks {
		if c.value < c.min || c.value > c.max {
			return fmt.Errorf("%s应在 %d–%d 之间", c.name, c.min, c.max)
		}
	}
	if q.GlobalPendingQuestions < q.SingleMaxQuestions || q.GlobalPendingQuestions < q.BatchMaxQuestions {
		return errors.New("全站未完成出题/检查量不能小于单题或批量的单次上限")
	}
	return nil
}

type GenerationQuotaUsage struct {
	RunningLearningJobs          int `json:"running_learning_jobs,omitempty"`
	PendingLearningJobs          int `json:"pending_learning_jobs,omitempty"`
	ActiveSingleRuns             int `json:"active_single_runs"`
	ActiveBatchJobs              int `json:"active_batch_jobs"`
	ActiveAICheckTasks           int `json:"active_ai_check_tasks"`
	ActiveBatchImports           int `json:"active_batch_imports"`
	SinglePendingQuestions       int `json:"single_pending_questions"`
	BatchPendingQuestions        int `json:"batch_pending_questions"`
	BatchImportReservedQuestions int `json:"batch_import_reserved_questions"`
	OverlapQuestions             int `json:"overlap_questions"`
	PendingQuestionUnits         int `json:"pending_question_units"`
}

type GenerationQuotaStore interface {
	GetGenerationQuota(ctx context.Context) (GenerationQuota, GenerationQuotaUsage, error)
	SaveGenerationQuota(ctx context.Context, quota GenerationQuota) error
}

// Versioned saves are used by the management page; trusted legacy callers keep
// the original SaveGenerationQuota contract. Both paths share the admission lock.
type GenerationQuotaVersionStore interface {
	SaveGenerationQuotaVersion(context.Context, GenerationQuota, int64) error
}

// GenerationQuotaAuditStore commits the operator action and configuration together.
// revision=0 preserves compatibility with older five-field management clients.
type GenerationQuotaAuditStore interface {
	SaveGenerationQuotaWithAudit(context.Context, GenerationQuota, int64, string) error
}

var ErrGenerationQuotaConflict = errors.New("配额配置已被其他窗口修改，请载入最新配置后重新修改")

type ConcurrencySnapshot struct {
	Limit   int `json:"limit"`
	Active  int `json:"active"`
	Waiting int `json:"waiting"`
}

type concurrencyWaiter struct {
	ready   chan struct{}
	granted bool
}

// ConcurrencyLimiter changes admission without replacing channels or cancelling
// current work. Waiters are FIFO so a large batch cannot repeatedly bypass others.
type ConcurrencyLimiter struct {
	mu            sync.Mutex
	limit, active int
	waiters       []*concurrencyWaiter
	changed       chan struct{}
}

func NewConcurrencyLimiter(limit int) *ConcurrencyLimiter {
	if limit < 1 || limit > 32 {
		limit = 2
	}
	return &ConcurrencyLimiter{limit: limit, changed: make(chan struct{})}
}

func (l *ConcurrencyLimiter) Snapshot() ConcurrencySnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return ConcurrencySnapshot{Limit: l.limit, Active: l.active, Waiting: len(l.waiters)}
}

func (l *ConcurrencyLimiter) signalLocked() { close(l.changed); l.changed = make(chan struct{}) }
func (l *ConcurrencyLimiter) grantLocked() {
	for len(l.waiters) > 0 && l.active < l.limit {
		w := l.waiters[0]
		l.waiters[0] = nil
		l.waiters = l.waiters[1:]
		w.granted = true
		l.active++
		close(w.ready)
	}
}
func (l *ConcurrencyLimiter) releaseLocked() { l.active--; l.grantLocked(); l.signalLocked() }
func (l *ConcurrencyLimiter) releaser() func() {
	var once sync.Once
	return func() { once.Do(func() { l.mu.Lock(); defer l.mu.Unlock(); l.releaseLocked() }) }
}

func (l *ConcurrencyLimiter) SetLimit(limit int) error {
	if limit < 1 || limit > 32 {
		return fmt.Errorf("并发应在1–32之间")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limit != limit {
		l.limit = limit
		l.grantLocked()
		l.signalLocked()
	}
	return nil
}

func (l *ConcurrencyLimiter) Changed() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.changed
}

func (l *ConcurrencyLimiter) TryAcquire() (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= l.limit || len(l.waiters) > 0 {
		return nil, false
	}
	l.active++
	return l.releaser(), true
}

func (l *ConcurrencyLimiter) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.active < l.limit && len(l.waiters) == 0 {
		l.active++
		release := l.releaser()
		l.mu.Unlock()
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
	w := &concurrencyWaiter{ready: make(chan struct{})}
	l.waiters = append(l.waiters, w)
	l.mu.Unlock()
	select {
	case <-w.ready:
		release := l.releaser()
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		l.mu.Lock()
		if w.granted {
			l.releaseLocked()
		} else {
			for i, queued := range l.waiters {
				if queued == w {
					copy(l.waiters[i:], l.waiters[i+1:])
					l.waiters[len(l.waiters)-1] = nil
					l.waiters = l.waiters[:len(l.waiters)-1]
					break
				}
			}
			l.grantLocked()
			l.signalLocked()
		}
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}

// One dispatcher claims only when a slot is available. Increasing the limit wakes
// it immediately; idle queues poll once per interval, not once per potential slot.
func RunTaskDispatcher(ctx context.Context, limiter *ConcurrencyLimiter, poll time.Duration, queue string, claim func(context.Context) (func(), error)) {
	var work sync.WaitGroup
	defer work.Wait()
	for {
		if ctx.Err() != nil {
			return
		}
		changed := limiter.Changed()
		release, ok := limiter.TryAcquire()
		if !ok {
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return
			}
		}
		task, err := claim(ctx)
		if err != nil || task == nil {
			release()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				slog.Error("任务抢占失败", "queue", queue, "error", err)
			}
			select {
			case <-time.After(poll):
				continue
			case <-ctx.Done():
				return
			}
		}
		work.Add(1)
		go func() { defer work.Done(); defer release(); task() }()
	}
}

type GenerationConcurrencySnapshot struct {
	Revision   int64               `json:"revision"`
	Generation ConcurrencySnapshot `json:"generation"`
	Batch      ConcurrencySnapshot `json:"batch"`
	AICheck    ConcurrencySnapshot `json:"ai_check"`
}

type GenerationConcurrencyRuntime struct {
	Generation, Batch, AICheck *ConcurrencyLimiter
	mu                         sync.Mutex
	revision                   int64
}

func NewGenerationConcurrencyRuntime(q GenerationQuota) (*GenerationConcurrencyRuntime, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	return &GenerationConcurrencyRuntime{Generation: NewConcurrencyLimiter(q.GenerationConcurrency), Batch: NewConcurrencyLimiter(q.BatchConcurrency), AICheck: NewConcurrencyLimiter(q.AICheckConcurrency), revision: q.Revision}, nil
}

func (r *GenerationConcurrencyRuntime) Apply(q GenerationQuota) (bool, error) {
	if err := q.Validate(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if q.Revision <= r.revision {
		return false, nil
	}
	_ = r.Generation.SetLimit(q.GenerationConcurrency)
	_ = r.Batch.SetLimit(q.BatchConcurrency)
	_ = r.AICheck.SetLimit(q.AICheckConcurrency)
	r.revision = q.Revision
	return true, nil
}

func (r *GenerationConcurrencyRuntime) Snapshot() GenerationConcurrencySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return GenerationConcurrencySnapshot{Revision: r.revision, Generation: r.Generation.Snapshot(), Batch: r.Batch.Snapshot(), AICheck: r.AICheck.Snapshot()}
}

var ErrGenerationQuotaExceeded = errors.New("生成任务配额已满")
var ErrBatchJobAlreadyActive = errors.New("批量任务已在执行中")

// BatchJobRetryAdmitter 原子地将终态批量任务重新纳入配额并设为执行中。
type BatchJobRetryAdmitter interface {
	ReactivateBatchJob(ctx context.Context, job BatchJobRecord) error
}

type GenerationQuotaError struct{ Message string }

func (e *GenerationQuotaError) Error() string        { return e.Message }
func (e *GenerationQuotaError) Is(target error) bool { return target == ErrGenerationQuotaExceeded }
