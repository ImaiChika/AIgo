// generation_worker.go 把单题生成从同步 HTTP 请求升级为持久化任务：
// 提交方把请求快照与 pending 运行落库即返回，worker 以「抢占 + 租约 + 重试」消费，
// 进程重启后未完成运行由新进程接管（语义与 AI 检查任务队列一致）。
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/workflowtrace"
)

// GenerationSpec 提交时持久化的生成请求快照：worker 依据快照执行，
// 不依赖发起时的 HTTP 请求；知识点内容即提交时解析结果（天然快照）。
type GenerationSpec struct {
	Request domain.GenerationRequest `json:"request"`
	// BankID 兼容保留的显式目标题库（当前提交方不再设置，恒为空）。
	BankID string `json:"bank_id,omitempty"`
	// ScopedBankIDs 范围受限出题账号的可见题库集合：非空时题目归入这些题库，
	// 保证受限范围的管理员能看到并送审；为空表示全范围账号，按专业自动归纳。
	ScopedBankIDs []string `json:"scoped_bank_ids,omitempty"`
	Actor         string   `json:"actor,omitempty"`
	OwnerID       string   `json:"owner_id,omitempty"`
}

// A resumed task must never send another owner's frozen experience to a model.
// Nil remains the pre-C5 protocol; active cards are deliberately not reloaded.
func validateGenerationExperienceSnapshot(run *domain.GenerationRun, spec *GenerationSpec) error {
	snapshot := spec.Request.PersonalExperiences
	if snapshot == nil {
		return nil
	}
	if err := domain.ValidatePersonalExperienceSnapshot(snapshot); err != nil {
		return fmt.Errorf("个人经验冻结快照无效：%w", err)
	}
	if snapshot.Version != domain.ExperienceProtocolVersion ||
		strings.TrimSpace(run.OwnerID) == "" || strings.TrimSpace(spec.OwnerID) == "" ||
		strings.TrimSpace(snapshot.OwnerID) == "" ||
		run.OwnerID != spec.OwnerID || snapshot.OwnerID != spec.OwnerID {
		return errors.New("个人经验冻结快照无效：协议版本或任务归属不一致")
	}
	return nil
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
	if p.GenerationLimiter == nil {
		p.GenerationLimiter = storage.NewConcurrencyLimiter(n)
	}
	go p.runGenerationWorker(ctx)
	slog.Info("单题生成后台 worker 已启动",
		"并发", p.GenerationLimiter.Snapshot().Limit, "单次超时", generationWorkTimeout.String(), "重试上限", generationMaxAttempts)
}

func (p *Pipeline) runGenerationWorker(ctx context.Context) {
	storage.RunTaskDispatcher(ctx, p.GenerationLimiter, p.pollIntervalOrDefault(), "generation", func(ctx context.Context) (func(), error) {
		if !p.generator.Ready(ctx) {
			return nil, nil
		}
		run, specJSON, err := p.runs.ClaimNextGenerationRun(ctx, generationLease())
		if err != nil || run == nil {
			return nil, err
		}
		ctx = storage.WithQueueAttempt(ctx, storage.QueueGeneration, run.ID, run.Attempts)
		var spec GenerationSpec
		if err := json.Unmarshal(specJSON, &spec); err != nil {
			// 快照损坏不可重试：直接终态，避免反复执行同一损坏任务
			slog.Error("命题请求快照解析失败", "run_id", run.ID, "error", err)
			_ = p.runs.FailGenerationRun(context.WithoutCancel(ctx), run.ID, "命题请求快照损坏，无法执行")
			return func() {}, nil
		}
		return func() { p.executeGenerationRun(ctx, run, &spec) }, nil
	})
}

// executeGenerationRun 先生成草稿并准备归属/分类，再原子交接题目、首次检查
// 和运行终态。事务失败可安全退避重试；非事务测试适配器保留分步流程，
// 其部分落库失败直接终态，避免盲目重试产生重复题目。
func (p *Pipeline) executeGenerationRun(ctx context.Context, run *domain.GenerationRun, spec *GenerationSpec) {
	ctx = storage.WithQueueAttempt(ctx, storage.QueueGeneration, run.ID, run.Attempts)
	started := time.Now()
	// 终态写入使用脱离当前超时/取消的 ctx，保证执行中断时状态仍能落库
	statusCtx := context.WithoutCancel(ctx)
	execCtx, cancel := context.WithTimeout(ctx, generationWorkTimeout)
	defer cancel()

	genCtx := storage.WithQuestionChange(execCtx, storage.QuestionChange{
		Actor: spec.Actor, OwnerID: spec.OwnerID, ChangeType: "ai_generate", ChangeNote: "AI生成题目草稿",
	})

	// 阶段一：生成草稿（无落库副作用，可重试）
	version := ""
	if spec.Request.Workflow != nil {
		version = spec.Request.Workflow.Version
	}
	genCtx, execution := workflowtrace.Start(genCtx, p.store, "generate:"+run.ID, "generation", run.Attempts, version)
	defer execution.Close()
	if err := validateGenerationExperienceSnapshot(run, spec); err != nil {
		execution.Complete("invalid_snapshot", nil)
		p.failGenerationRun(statusCtx, run, err, false)
		return
	}
	drafts, err := p.generateDrafts(generator.WithGenerationAttempt(llm.WithCallOperation(genCtx, "generate:"+run.ID), run.Attempts), spec.Request)
	if err != nil {
		execution.Complete("generation_failed", nil)
		// 基础医学选题范围调整是终态失败：不消耗退避重试，不换考点补生。
		p.failGenerationRun(statusCtx, run, err, !domain.IsBasicMedicalScopeError(err))
		return
	}

	handoff := workflowtrace.Begin(genCtx, "handoff", -1, drafts)
	defer handoff.FailUnlessEnded("handoff_failed")
	// 阶段二：准备题库归属，事务存储在下方一次性提交全部草稿及首次检查。
	for i := range drafts {
		// Complete generation-side writes before first-check dispatch. A fast
		// checker may advance or delete a draft as soon as it is queued.
		switch {
		case spec.BankID != "":
			drafts[i].BankIDs = []string{spec.BankID}
		case len(spec.ScopedBankIDs) > 0:
			drafts[i].BankIDs = append([]string(nil), spec.ScopedBankIDs...)
		case p.bankAssigner != nil:
			if err := p.bankAssigner.AssignBank(genCtx, &drafts[i]); err != nil {
				slog.Warn("自动归纳题库失败", "run_id", run.ID, "question_id", drafts[i].ID, "error", err)
			}
		}
		if _, atomic := p.store.(storage.GenerationRunCommitter); atomic && p.checker != nil {
			continue
		}
		if err := p.store.SaveQuestion(genCtx, drafts[i]); err != nil {
			p.failGenerationRun(statusCtx, run, fmt.Errorf("保存题目失败: %w", err), false)
			return
		}
	}
	if committer, ok := p.store.(storage.GenerationRunCommitter); ok && p.checker != nil {
		attempts := p.FirstCheckMaxAttempts
		if attempts <= 0 {
			attempts = 3
		}
		if err := committer.CommitGenerationRun(genCtx, storage.GenerationRunCommit{RunID: run.ID, Attempt: run.Attempts, Questions: drafts, CheckMaxAttempts: attempts}); err != nil {
			if errors.Is(err, storage.ErrGenerationRunLeaseLost) {
				handoff.End("failed", "lease_lost", nil)
				return
			}
			p.failGenerationRun(statusCtx, run, fmt.Errorf("保存生成结果及首检任务失败: %w", err), true)
			return
		}

		ids := make([]string, len(drafts))
		for i, q := range drafts {
			ids[i] = q.ID
		}
		handoff.End("committed", "", ids)
		execution.Complete("committed", ids)
		for _, q := range drafts {
			if p.auditor != nil {
				_ = p.auditor.LogCreate(genCtx, q.ID, spec.Actor)
			}
		}
		slog.Info("单题生成完成", "run_id", run.ID, "owner", spec.OwnerID, "count", len(drafts), "attempts", run.Attempts, "duration_ms", time.Since(started).Milliseconds())
		return
	}
	// 先登记已落库题目的 ID，再进入首次 AI 检查。准入统计据此识别短暂
	// 同时处于「单题运行收尾」和「AI 首检」的同一道题，避免重复占用额度。
	if len(drafts) > 0 {
		ids := make([]string, 0, len(drafts))
		for _, q := range drafts {
			ids = append(ids, q.ID)
		}
		if err := p.runs.RecordGenerationRunQuestionIDs(statusCtx, run.ID, ids); err != nil {
			slog.Warn("登记生成题目 ID 失败，配额过渡期可能短暂保守计数", "run_id", run.ID, "error", err)
		}
	}
	// Audit precedes first-check dispatch; no draft save is allowed after dispatch.
	questionIDs := make([]string, 0, len(drafts))
	for i := range drafts {
		q := drafts[i]
		if p.auditor != nil {
			p.auditor.LogCreate(genCtx, q.ID, spec.Actor)
		}
		questionIDs = append(questionIDs, q.ID)
	}
	if p.checker != nil && len(questionIDs) > 0 {
		p.checker.CheckAsync(questionIDs...)
	}

	if err := p.runs.CompleteGenerationRun(statusCtx, run.ID, questionIDs); err != nil {
		slog.Error("标记命题运行完成失败", "run_id", run.ID, "error", err)
		return
	}
	handoff.End("checkpointed", "", questionIDs)
	execution.Complete("checkpointed", questionIDs)
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
		if errors.Is(rerr, storage.ErrGenerationRunLeaseLost) {
			slog.Info("命题失败属于旧执行轮次，保留当前任务", "run_id", run.ID, "attempt", run.Attempts)
			return
		}
		if rerr != nil {
			slog.Error("记录命题重试状态失败", "run_id", run.ID, "error", rerr)
		}
		if scheduled {
			slog.Warn("命题生成失败，将按退避重试",
				"run_id", run.ID, "attempts", run.Attempts, "max_attempts", run.MaxAttempts, "error", err)
			return
		}
		if rerr == nil {
			// RetryGenerationRun already committed the exhausted terminal state.
			// A second running-only write would falsely report a lost attempt.
			slog.Error("命题运行尝试耗尽", "run_id", run.ID, "attempts", run.Attempts, "error", err)
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

func (p *Pipeline) GenerationReady(ctx context.Context) bool {
	if p == nil || p.generator == nil {
		return true
	}
	return p.generator.Ready(ctx)
}
