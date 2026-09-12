// worker.go 提供 AI 检查的后台异步执行能力。
// 任务持久化在 ai_check_tasks 表：CheckAsync 幂等入队，worker 通过
// 「抢占 + 租约 + 重试 + 耗尽标记」消费，服务重启后未完成任务自动恢复执行。
package aicheck

import (
	"context"
	"fmt"
	"time"

	"aigo/internal/domain"
)

const (
	defaultCheckTimeout = 90 * time.Second
	defaultMaxAttempts  = 3
	defaultRetryBackoff = 15 * time.Second
	maxRetryBackoff     = 5 * time.Minute
	// leaseBuffer 租约在检查超时之上增加的缓冲，保证正常执行的慢请求
	// 不会在执行期间被其他 worker 当作卡死任务回收。
	leaseBuffer = 30 * time.Second
	// pollInterval 抢占不到任务时的轮询间隔。
	pollInterval = time.Second
)

// checkTimeoutOrDefault 单次检查调用超时。
func (s *Service) checkTimeoutOrDefault() time.Duration {
	if s.CheckTimeout > 0 {
		return s.CheckTimeout
	}
	return defaultCheckTimeout
}

// maxAttemptsOrDefault 失败重试上限。
func (s *Service) maxAttemptsOrDefault() int {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return defaultMaxAttempts
}

// leaseDuration 任务租约时长 = 检查超时 + 缓冲。
func (s *Service) leaseDuration() time.Duration {
	return s.checkTimeoutOrDefault() + leaseBuffer
}

// retryBackoffFor 按已执行次数计算退避：base, 2×base, 4×base…封顶 5 分钟。
func (s *Service) retryBackoffFor(attempts int) time.Duration {
	base := s.RetryBackoff
	if base <= 0 {
		base = defaultRetryBackoff
	}
	d := base
	for i := 1; i < attempts && d < maxRetryBackoff; i++ {
		d *= 2
	}
	if d > maxRetryBackoff {
		d = maxRetryBackoff
	}
	return d
}

// SetAutoCheckEnabled 控制自动触发检查是否生效（对应 AIGO_AICHECK_AUTO，默认开）。
// 关闭后 CheckAsync 直接忽略，仅保留 POST /api/ai-check 手动检查入口。
func (s *Service) SetAutoCheckEnabled(enabled bool) {
	s.autoEnabled = enabled
}

// CheckAsync 把题目写入持久化检查队列（立即返回，不等待检查完成）。
// 幂等：该题目已有 pending/running 任务时不再新建；重试耗尽的题目再次入队
// 会开启新一轮重试周期。任务由 StartWorkers 启动的 worker 消费。
func (s *Service) CheckAsync(questionIDs ...string) {
	if !s.autoEnabled || s.taskStore == nil {
		return
	}
	for _, id := range questionIDs {
		if id == "" {
			continue
		}
		q, err := s.questionStore.GetQuestion(context.Background(), id)
		if err != nil || q == nil {
			continue
		}
		task := domain.AICheckTask{
			ID:              fmt.Sprintf("act-%s-%d", id, time.Now().UnixNano()),
			QuestionID:      id,
			QuestionVersion: q.Version,
			Status:          domain.AICheckTaskPending,
			MaxAttempts:     s.maxAttemptsOrDefault(),
		}
		created, err := s.taskStore.EnqueueCheckTask(context.Background(), task)
		if err != nil {
			fmt.Printf("⚠ AI 检查任务入队失败: %s: %v\n", id, err)
		} else if !created {
			fmt.Printf("AI 检查任务已存在，跳过重复入队: %s\n", id)
		}
	}
}

// StartWorkers 启动 n 个后台检查 worker，ctx 取消时退出。
func (s *Service) StartWorkers(ctx context.Context, n int) {
	if s.taskStore == nil {
		fmt.Println("⚠ 未配置 AI 检查任务存储，跳过 worker 启动")
		return
	}
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		go s.runWorker(ctx)
	}
	fmt.Printf("✓ AI 检查后台 worker 已启动（并发 %d，单次超时 %s，重试上限 %d 次）\n",
		n, s.checkTimeoutOrDefault(), s.maxAttemptsOrDefault())
}

func (s *Service) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		task, err := s.taskStore.ClaimNextCheckTask(ctx, s.leaseDuration())
		if err != nil {
			fmt.Printf("⚠ AI 检查任务抢占失败: %v\n", err)
			if !sleepCtx(ctx, pollInterval) {
				return
			}
			continue
		}
		if task == nil {
			if !sleepCtx(ctx, pollInterval) {
				return
			}
			continue
		}
		s.executeTask(ctx, task)
	}
}

// executeTask 执行单个检查任务，带超时控制与失败重试记录。
// 成功路径中，任务完成标记与检查结论在同一事务落库（见 service.checkQuestion），
// 不会出现“题目已通过/已删除但任务仍挂着”的中间态。
func (s *Service) executeTask(ctx context.Context, task *domain.AICheckTask) {
	execCtx, cancel := context.WithTimeout(ctx, s.checkTimeoutOrDefault())
	defer cancel()

	if _, err := s.checkQuestion(execCtx, task.QuestionID, task.ID); err != nil {
		if execCtx.Err() != nil {
			err = fmt.Errorf("检查超时（单次上限 %s）", s.checkTimeoutOrDefault())
		}
		if ferr := s.taskStore.FailCheckTask(ctx, task.ID, err.Error(), s.retryBackoffFor(task.Attempts)); ferr != nil {
			fmt.Printf("⚠ 记录 AI 检查失败状态出错: %s: %v\n", task.ID, ferr)
		}
		fmt.Printf("⚠ AI 检查失败（第 %d/%d 次）: %s: %v\n", task.Attempts, task.MaxAttempts, task.QuestionID, err)
	}
}

// PendingCount 返回当前排队中 + 执行中的任务数（用于观测）。
func (s *Service) PendingCount() int {
	if s.taskStore == nil {
		return 0
	}
	counts, err := s.taskStore.CountCheckTasksByStatus(context.Background())
	if err != nil {
		return 0
	}
	return counts[domain.AICheckTaskPending] + counts[domain.AICheckTaskRunning]
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
