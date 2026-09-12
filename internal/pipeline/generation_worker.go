// generation_worker.go 把单题生成从同步 HTTP 请求升级为持久化任务：
// 提交方把请求快照与 pending 运行落库即返回，worker 以「抢占 + 租约 + 重试」消费，
// 进程重启后未完成运行由新进程接管（语义与 AI 检查任务队列一致）。
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// GenerationSpec 提交时持久化的生成请求快照：worker 依据快照执行，
// 不依赖发起时的 HTTP 请求；知识点内容即提交时解析结果（天然快照）。
type GenerationSpec struct {
	Request domain.GenerationRequest `json:"request"`
	BankID  string                   `json:"bank_id,omitempty"`
	Actor   string                   `json:"actor,omitempty"`
	OwnerID string                   `json:"owner_id,omitempty"`
}

// BankAssigner 题库自动归纳能力（由 bank.Service 实现）。
type BankAssigner interface {
	AssignBank(ctx context.Context, q *domain.A2Question) error
}

// GenerationAuditor 命题审计能力（由 audit.Service 实现）。
type GenerationAuditor interface {
	LogCreate(ctx context.Context, questionID, actor string) error
}

const (
	// generationWorkTimeout 单次执行总超时（覆盖 LLM 调用与逐题落库）。
	generationWorkTimeout = 10 * time.Minute
	// generationLeaseBuffer 租约在工作超时之上的缓冲，避免正常慢请求被其他 worker 回收。
	generationLeaseBuffer  = time.Minute
	generationMaxAttempts  = 3
	generationRetryBackoff = 15 * time.Second
	generationMaxBackoff   = 5 * time.Minute
	// generationPollInterval 抢占不到任务时的轮询间隔。
	generationPollInterval = time.Second
)

// Pipeline 级可调参数（零值使用上述默认；测试可调小以缩短等待）。
// GenerationPollInterval worker 抢占不到任务时的轮询间隔。
// GenerationRetryBackoff 首次重试退避基数，按尝试次数指数递增。

func (p *Pipeline) pollIntervalOrDefault() time.Duration {
	if p.GenerationPollInterval > 0 {
		return p.GenerationPollInterval
	}
	return generationPollInterval
}

func (p *Pipeline) retryBackoffFor(attempts int) time.Duration {
	base := p.GenerationRetryBackoff
	if base <= 0 {
		base = generationRetryBackoff
	}
	d := base
	for i := 1; i < attempts && d < generationMaxBackoff; i++ {
		d *= 2
	}
	if d > generationMaxBackoff {
		d = generationMaxBackoff
	}
	return d
}

// SetBankAssigner 注入题库自动归纳（serve 模式装配，见 cmd/aigo/main.go）。
func (p *Pipeline) SetBankAssigner(b BankAssigner) { p.bankAssigner = b }

// SetGenerationAuditor 注入命题审计（serve 模式装配）。
func (p *Pipeline) SetGenerationAuditor(a GenerationAuditor) { p.auditor = a }

// generationLease 执行租约 = 工作超时 + 缓冲。
func generationLease() time.Duration { return generationWorkTimeout + generationLeaseBuffer }

// StartGenerationWorkers 启动 n 个后台生成 worker，消费 pending 运行；ctx 取消时退出。
func (p *Pipeline) StartGenerationWorkers(ctx context.Context, n int) {
	if p.runs == nil {
		slog.Warn("未配置命题运行存储，跳过生成 worker 启动")
		return
	}
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		go p.runGenerationWorker(ctx)
	}
	slog.Info("单题生成后台 worker 已启动",
		"并发", n, "单次超时", generationWorkTimeout.String(), "重试上限", generationMaxAttempts)
}

func (p *Pipeline) runGenerationWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		run, specJSON, err := p.runs.ClaimNextGenerationRun(ctx, generationLease())
		if err != nil {
			slog.Error("命题运行抢占失败", "error", err)
			if !sleepCtx(ctx, p.pollIntervalOrDefault()) {
				return
			}
			continue
		}
		if run == nil {
			if !sleepCtx(ctx, p.pollIntervalOrDefault()) {
				return
			}
			continue
		}
		var spec GenerationSpec
		if err := json.Unmarshal(specJSON, &spec); err != nil {
			// 快照损坏不可重试：直接终态，避免反复执行同一损坏任务
			slog.Error("命题请求快照解析失败", "run_id", run.ID, "error", err)
			_ = p.runs.FailGenerationRun(context.WithoutCancel(ctx), run.ID, "命题请求快照损坏，无法执行")
			continue
		}
		p.executeGenerationRun(ctx, run, &spec)
	}
}

// executeGenerationRun 执行单次命题：生成 → 逐题落库 → AI 检查 → 题库归纳/审计，
// 落库顺序与原同步路径一致。生成阶段（LLM/解析，无落库副作用）失败按次数上限
// 退避重试；落库阶段一旦失败直接终态——部分落库后重试会产生重复题目。
func (p *Pipeline) executeGenerationRun(ctx context.Context, run *domain.GenerationRun, spec *GenerationSpec) {
	started := time.Now()
	// 终态写入使用脱离当前超时/取消的 ctx，保证执行中断时状态仍能落库
	statusCtx := context.WithoutCancel(ctx)
	execCtx, cancel := context.WithTimeout(ctx, generationWorkTimeout)
	defer cancel()

	genCtx := storage.WithQuestionChange(execCtx, storage.QuestionChange{
		Actor: spec.Actor, OwnerID: spec.OwnerID, ChangeType: "ai_generate", ChangeNote: "AI生成题目草稿",
	})

	// 阶段一：生成草稿（无落库副作用，可重试）
	drafts, err := p.generateDrafts(genCtx, spec.Request)
	if err != nil {
		p.failGenerationRun(statusCtx, run, err, true)
		return
	}

	// 阶段二：逐题落库 + AI 检查触发（与原同步路径一致；失败终态，不重试）
	for i := range drafts {
		if err := p.store.SaveQuestion(genCtx, drafts[i]); err != nil {
			p.failGenerationRun(statusCtx, run, fmt.Errorf("保存题目失败: %w", err), false)
			return
		}
	}
	if p.checker != nil && len(drafts) > 0 {
		ids := make([]string, 0, len(drafts))
		for _, q := range drafts {
			ids = append(ids, q.ID)
		}
		p.checker.CheckAsync(ids...)
	}
	// 阶段三：题库归纳 + 审计（题目已入库，仅补充归属与留痕）
	questionIDs := make([]string, 0, len(drafts))
	for i := range drafts {
		q := drafts[i]
		if spec.BankID != "" {
			q.BankIDs = []string{spec.BankID}
		} else if p.bankAssigner != nil {
			if err := p.bankAssigner.AssignBank(genCtx, &q); err != nil {
				slog.Warn("自动归纳题库失败", "run_id", run.ID, "question_id", q.ID, "error", err)
			}
		}
		if err := p.store.SaveQuestion(genCtx, q); err != nil {
			// 归纳失败不回滚已入库草稿：题目仍可用，仅题库归属可能为空，管理员可手动调整
			slog.Warn("更新题目题库归属失败", "run_id", run.ID, "question_id", q.ID, "error", err)
		}
		if p.auditor != nil {
			p.auditor.LogCreate(genCtx, q.ID, spec.Actor)
		}
		questionIDs = append(questionIDs, q.ID)
	}

	if err := p.runs.CompleteGenerationRun(statusCtx, run.ID, questionIDs); err != nil {
		slog.Error("标记命题运行完成失败", "run_id", run.ID, "error", err)
		return
	}
	slog.Info("单题生成完成",
		"run_id", run.ID, "owner", spec.OwnerID, "count", len(questionIDs),
		"attempts", run.Attempts, "duration_ms", time.Since(started).Milliseconds())
}

// failGenerationRun 记录失败：retryable 且未达次数上限时回 pending 退避重试，
// 否则标记 failed 终态。
func (p *Pipeline) failGenerationRun(ctx context.Context, run *domain.GenerationRun, err error, retryable bool) {
	message := fmt.Sprintf("生成失败: %v", err)
	if retryable {
		scheduled, rerr := p.runs.RetryGenerationRun(ctx, run.ID, message, p.retryBackoffFor(run.Attempts))
		if rerr != nil {
			slog.Error("记录命题重试状态失败", "run_id", run.ID, "error", rerr)
		}
		if scheduled {
			slog.Warn("命题生成失败，将按退避重试",
				"run_id", run.ID, "attempts", run.Attempts, "max_attempts", run.MaxAttempts, "error", err)
			return
		}
	}
	if ferr := p.runs.FailGenerationRun(ctx, run.ID, message); ferr != nil {
		slog.Error("标记命题运行失败出错", "run_id", run.ID, "error", ferr)
	}
	slog.Error("命题运行失败", "run_id", run.ID, "attempts", run.Attempts, "error", err)
}

// sleepCtx 可取消的休眠，供 worker 轮询使用（与 aicheck 保持一致）。
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
