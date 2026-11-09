package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
)

// localSingleAPIBackend 是本地批量执行器的 backend 标识。
const localSingleAPIBackend = "local_single_api"

// DraftChecker 批量导入落库后的自动 AI 检查入口，由 aicheck.Service 实现。
// 通过 SetDraftChecker 注入，使执行器与检查实现解耦；nil 时不触发（仅测试）。
type DraftChecker interface {
	CheckAsync(questionIDs ...string)
}

// LocalExecutor 把批量任务拆成多个单题生成请求。它不使用百炼 Files/Batches，
// 每道题复用单题 generator.Service，任务进度和生成结果快照落在 batch_jobs。
// 新提交、重启恢复、失败项重跑共享进程内可调并发名额。
// 调低时已有调用继续完成，后续单元等待；调高时立即利用新增名额。
type LocalExecutor struct {
	generator     *generator.Service
	questionStore storage.QuestionStore
	batchJobStore storage.BatchJobStore
	model         string
	// checker 批量导入落库后的自动检查入口（见 SetDraftChecker）。
	checker          DraftChecker
	pipelineStore    storage.BatchPipelineStore
	pipelineEnabled  bool
	checkMaxAttempts int

	// MaxConcurrency retains the constructor fallback for compatibility.
	// Live limits are read from limiter; use Capabilities to inspect the current value.
	MaxConcurrency int
	// MaxAttempts 单个生成项的最多尝试次数（含首次）；网络/限流类失败按
	// RetryDelays 退避后重试，耗尽后该项保持 failed，可经重跑入口再试。
	MaxAttempts int
	// RetryDelays 相邻两次尝试之间的退避间隔；长度不足时重复最后一个。
	RetryDelays      []time.Duration
	RecoveryInterval time.Duration

	limiter       *storage.ConcurrencyLimiter
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	checkpointOps map[string]*batchCheckpointOperation // protected by mu
	active        map[string]bool
	recovery      map[string]RecoveryProgress
	checkpoints   map[string]batchCheckpoint
	closed        bool
	wg            sync.WaitGroup
}

// LocalConfig is applied before recovery starts. Limits are per executor/process.
type LocalConfig struct {
	Concurrency      int
	Context          context.Context
	Limiter          *storage.ConcurrencyLimiter // Shared live controller, injected before recovery.
	Checker          DraftChecker
	Pipeline         bool
	CheckMaxAttempts int
	RecoveryInterval time.Duration
}

type localBatchOutput struct {
	PendingExperiences  map[string]*domain.PersonalExperienceSnapshot `json:"pending_personal_experiences,omitempty"`
	PendingContexts     map[string]*domain.QuestionGenerationContext  `json:"pending_knowledge_contexts,omitempty"`
	GenerationStartedAt int64                                         `json:"generation_started_at,omitempty"`
	Recovery            *RecoveryProgress                             `json:"recovery,omitempty"`
	Questions           []domain.A2Question                           `json:"questions"`
	Items               []ImportItem                                  `json:"items"`
	Pipeline            bool                                          `json:"pipeline,omitempty"`
	Creator             string                                        `json:"creator,omitempty"`
	CheckMaxAttempts    int                                           `json:"check_max_attempts,omitempty"`
}

// localDefaultConcurrency 默认全局并发上限：与 AI 检查 worker 同量级，
// 避免批量任务把共享的生成端点打满。
const localDefaultConcurrency = 2

func NewLocalExecutor(gen *generator.Service, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore, model string) *LocalExecutor {
	return NewLocalExecutorWithConfig(gen, questionStore, batchJobStore, model, LocalConfig{})
}

func NewLocalExecutorWithConfig(gen *generator.Service, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore, model string, cfg LocalConfig) *LocalExecutor {
	concurrency := cfg.Concurrency
	if concurrency < 1 || concurrency > 32 {
		concurrency = localDefaultConcurrency
	}
	parent := cfg.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	executor := &LocalExecutor{
		generator:      gen,
		questionStore:  questionStore,
		batchJobStore:  batchJobStore,
		model:          strings.TrimSpace(model),
		MaxConcurrency: concurrency,
		MaxAttempts:    3,
		RetryDelays:    []time.Duration{2 * time.Second, 5 * time.Second},
		ctx:            ctx, cancel: cancel, active: make(map[string]bool), recovery: make(map[string]RecoveryProgress), checkpoints: make(map[string]batchCheckpoint),
		RecoveryInterval: cfg.RecoveryInterval,
		checker:          cfg.Checker, checkMaxAttempts: cfg.CheckMaxAttempts,
	}
	executor.pipelineStore, _ = batchJobStore.(storage.BatchPipelineStore)
	executor.pipelineEnabled = cfg.Pipeline && executor.pipelineStore != nil
	if executor.checkMaxAttempts < 1 || executor.checkMaxAttempts > 10 {
		executor.checkMaxAttempts = 3
	}
	if executor.MaxConcurrency < 1 {
		executor.MaxConcurrency = 1
	}
	executor.limiter = cfg.Limiter
	if executor.limiter == nil {
		executor.limiter = storage.NewConcurrencyLimiter(concurrency)
	}
	executor.wg.Add(1)
	go func() { defer executor.wg.Done(); executor.recoveryLoop() }()
	return executor
}

// Close interrupts calls/waits and joins this executor's workers. Durable unfinished
// items stay pending and can be recovered by a new executor.
func (s *LocalExecutor) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *LocalExecutor) startQueue(jobID string, units []domain.KnowledgePoint, pending []int, output localBatchOutput, snapshot *domain.GenerationPromptSnapshot, scope *domain.KnowledgeWorkflowSnapshot) {
	s.mu.Lock()
	// Recheck at ownership acquisition: an earlier recovery scan may have read
	// a stale record before a successful reply became a pending checkpoint.
	_, checkpointPending := s.checkpoints[jobID]
	if s.closed || s.active[jobID] || checkpointPending {
		s.mu.Unlock()
		return
	}
	s.active[jobID] = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() { s.mu.Lock(); delete(s.active, jobID); s.mu.Unlock() }()
		s.runQueue(jobID, units, pending, output, snapshot, scope)
	}()
}

// SetDraftChecker 保留旧任务导入后的首检入口。新流水线由存储在每题
// 提交结果时原子入队，Web 与 CLI 都不依赖页面导入来触发首检。
func (s *LocalExecutor) SetDraftChecker(c DraftChecker) { s.checker = c }

func (s *LocalExecutor) Capabilities() Capabilities {
	available := s.generator != nil
	message := fmt.Sprintf("每道题单独调用单题生成 API，任务结果保存在本地，不使用 Qwen 批量平台；全局并发上限 %d", s.concurrency())
	if s.pipelineEnabled {
		message = "后台逐题生成并首检，通过的题目会陆续显示；离开页面后仍会继续处理"
	}
	if !available {
		message = "生成服务未初始化"
	}
	return Capabilities{
		Backend:       localSingleAPIBackend,
		Available:     available,
		ExecutionMode: localSingleAPIBackend,
		Model:         s.currentModel(),
		Message:       message,
		Concurrency:   s.concurrency(),
	}
}

func (s *LocalExecutor) concurrency() int {
	return s.limiter.Snapshot().Limit
}

func (s *LocalExecutor) currentModel() string {
	if model := s.generator.CurrentModel(); model != "" {
		return model
	}
	return s.model // 固定客户端或 mock 沿用初始化名称。
}

func (s *LocalExecutor) GenerateAndSubmit(ctx context.Context, points []domain.KnowledgePoint, countPerPoint int, jobName string) (string, int, error) {
	plan, err := PerPointPlan(points, "", countPerPoint)
	if err != nil {
		return "", 0, err
	}
	return s.SubmitPlan(ctx, PlanSubmission{Plan: plan, JobName: jobName})
}

// GetBatchJobByRequestKey 按提交幂等键读取任务（委托持久层）。
func (s *LocalExecutor) GetBatchJobByRequestKey(ctx context.Context, ownerID, key string) (*storage.BatchJobRecord, error) {
	if keyStore, ok := s.batchJobStore.(storage.BatchRequestKeyStore); ok {
		return keyStore.GetBatchJobByRequestKey(ctx, ownerID, key)
	}
	return nil, nil
}

// SubmitPlan 按显式批量计划提交任务：计划一次成型并随任务持久化，
// 单元清单是恢复与重跑的权威；随机任务在提交前已抽样完成，此后不再重抽。
// RequestKey/RequestHash（若有）随任务写入，用于提交幂等重放。
func (s *LocalExecutor) SubmitPlan(ctx context.Context, submission PlanSubmission) (string, int, error) {
	plan, jobName, requestKey, requestHash := submission.Plan, submission.JobName, submission.RequestKey, submission.RequestHash
	if err := s.ctx.Err(); err != nil {
		return "", 0, err
	}
	if !s.Capabilities().Available {
		return "", 0, fmt.Errorf("%w: %s", ErrUnavailable, s.Capabilities().Message)
	}
	if err := plan.Validate(); err != nil {
		return "", 0, err
	}
	units := plan.PlanUnitsAsPoints()
	if strings.TrimSpace(jobName) == "" {
		jobName = fmt.Sprintf("单题 API 批量任务 %s", time.Now().Format("01-02 15:04"))
	}
	jobID := fmt.Sprintf("local-%d", time.Now().UnixNano())
	// PointsJSON 保留任务涉及的去重知识点快照（诊断与旧工具兼容）；
	// 单元序列以 PlanJSON 为准。
	pointsJSON, _ := json.Marshal(plan.UniquePlanPoints())
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return "", 0, err
	}
	snapshot := generator.PromptSnapshotFromContext(ctx)
	var promptJSON string
	if snapshot != nil {
		raw, e := json.Marshal(snapshot)
		if e != nil {
			return "", 0, e
		}
		promptJSON = string(raw)
	}
	workflow := knowledgebase.SnapshotFromContext(ctx)
	var experienceJSON string
	if experiences := generator.PersonalExperiencesFromContext(ctx); experiences != nil {
		raw, e := json.Marshal(experiences)
		if e != nil {
			return "", 0, e
		}
		experienceJSON = string(raw)
	}
	var workflowJSON string
	if workflow != nil {
		raw, e := json.Marshal(workflow)
		if e != nil {
			return "", 0, e
		}
		workflowJSON = string(raw)
	}
	record := storage.BatchJobRecord{
		ExperienceSnapshotJSON: experienceJSON,
		WorkflowSnapshotJSON:   workflowJSON,
		ID:                     jobID,
		PromptSnapshotJSON:      promptJSON,
		OwnerID:                storage.QuestionChangeFromContext(ctx).OwnerID,
		Backend:                localSingleAPIBackend,
		BackendProfile:         "single-question-api",
		Model:                  s.currentModel(),
		JobName:                jobName,
		Status:                 "in_progress",
		TotalCount:             len(units),
		PointsJSON:             string(pointsJSON),
		PlanJSON:               string(planJSON),
		RequestKey:             requestKey,
		RequestHash:            requestHash,
		OutputJSON:             `{"questions":[],"items":[]}`,
		CreatedAt:              time.Now().Format(time.RFC3339Nano),
	}
	output := localBatchOutput{Questions: []domain.A2Question{}, Items: []ImportItem{}}
	if s.pipelineEnabled {
		record.BackendProfile = "single-question-pipeline"
		output.Pipeline, output.Creator, output.CheckMaxAttempts = true, storage.QuestionChangeFromContext(ctx).Actor, s.checkMaxAttempts
		payload, _ := json.Marshal(output)
		record.OutputJSON = string(payload)
	}
	if s.batchJobStore == nil {
		return "", 0, fmt.Errorf("批量任务持久化服务不可用")
	}
	if err := s.batchJobStore.SaveBatchJob(ctx, record); err != nil {
		return "", 0, err
	}
	// 新提交与重启恢复共用同一执行入口，受全局并发上限约束。
	s.startQueue(jobID, units, resumeUnits(len(units), 0), output, snapshot, workflow)
	return jobID, record.TotalCount, nil
}

// flattenUnits 把“每要点 N 题”摊平成与任务条目一一对应的生成单元序列。
// 输出条目（output.Items）与该序列按下标严格对应，是恢复与重跑定位的依据。
func flattenUnits(points []domain.KnowledgePoint, countPerPoint int) []domain.KnowledgePoint {
	if countPerPoint < 1 {
		countPerPoint = 1
	}
	units := make([]domain.KnowledgePoint, 0, len(points)*countPerPoint)
	for _, point := range points {
		for i := 0; i < countPerPoint; i++ {
			units = append(units, point)
		}
	}
	return units
}

// resumeUnits 返回待执行单元下标：跳过 output.Items 中已落库的前 done 项，
// 其余全部执行。
func resumeUnits(total, done int) []int {
	if done < 0 {
		done = 0
	}
	pending := make([]int, 0, max(total-done, 0))
	for i := done; i < total; i++ {
		pending = append(pending, i)
	}
	return pending
}

// failedUnits 返回 output.Items 中状态非 ok 的单元下标（重跑入口）。
func failedUnits(output localBatchOutput) []int {
	var pending []int
	for i, item := range output.Items {
		if item.Status != "ok" {
			pending = append(pending, i)
		}
	}
	return pending
}

// runQueue uses a bounded pool; only this goroutine aggregates/persists results.
// Pending slots are persisted explicitly, so recovery does not assume a completed prefix.
// normalizeOutput also upgrades legacy snapshots whose items are a completed prefix
// and whose successful questions are in that prefix's submission order.
func normalizeOutput(units []domain.KnowledgePoint, output localBatchOutput) (localBatchOutput, error) {
	if len(output.Items) > len(units) {
		return output, fmt.Errorf("单元数量超过任务快照")
	}
	items := make([]ImportItem, len(units))
	byID := make(map[string]domain.A2Question, len(output.Questions))
	for _, q := range output.Questions {
		byID[q.ID] = q
	}
	legacy := 0
	for pos, unit := range units {
		item := ImportItem{OutlineCode: unit.OutlineCode, Topic: unit.Topic, Status: "pending"}
		if pos < len(output.Items) {
			item = output.Items[pos]
		}
		if item.OutlineCode == "" {
			item.OutlineCode = unit.OutlineCode
		}
		if item.Topic == "" {
			item.Topic = unit.Topic
		}
		if item.Status == "" || item.Status == "running" {
			item.Status = "pending"
		}
		if item.Status == "ok" {
			if item.QuestionID == "" && legacy < len(output.Questions) {
				item.QuestionID = output.Questions[legacy].ID
			}
			legacy++
			q, ok := byID[item.QuestionID]
			if !ok {
				return output, fmt.Errorf("单元%d缺少题目快照", pos)
			}
			item.question = q
			item.Count = 1
		}
		items[pos] = item
	}
	output.Items = items
	output.Questions = orderedQuestions(output)
	return output, nil
}

func orderedQuestions(output localBatchOutput) []domain.A2Question {
	byID := make(map[string]domain.A2Question, len(output.Questions))
	for _, q := range output.Questions {
		byID[q.ID] = q
	}
	questions := make([]domain.A2Question, 0, len(output.Questions))
	for _, item := range output.Items {
		if item.Status == "ok" {
			if item.question.ID != "" {
				questions = append(questions, item.question)
			} else if q, ok := byID[item.QuestionID]; ok {
				questions = append(questions, q)
			}
		}
	}
	return questions
}

func outputCounts(output localBatchOutput) (completed, failed int) {
	for _, item := range output.Items {
		switch item.Status {
		case "ok":
			completed += item.Count
		case "failed":
			failed++
		}
	}
	return
}

func unfinishedUnits(total int, output localBatchOutput) []int {
	pending := make([]int, 0, total)
	for pos := 0; pos < total; pos++ {
		if pos >= len(output.Items) || (output.Items[pos].Status != "ok" && output.Items[pos].Status != "failed") {
			pending = append(pending, pos)
		}
	}
	return pending
}

func (s *LocalExecutor) persistOutput(ctx context.Context, jobID, status string, output localBatchOutput) error {
	completed, failed := outputCounts(output)
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = s.updateJob(ctx, jobID, status, completed, failed, output)
		if err == nil {
			return nil
		}
		if attempt < 2 {
			select {
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	slog.Error("批量进度保存失败，保留快照并等待自动恢复", "job_id", jobID, "error", err)
	s.rememberCheckpoint(jobID, status, output)
	s.noteRecovery(jobID)
	return err
}

func (s *LocalExecutor) maxAttempts() int {
	if s.MaxAttempts < 1 {
		return 1
	}
	return s.MaxAttempts
}

// resumePending 接管服务重启前尚未完成的本地任务。题目与知识点快照均已
// 落库，已完成的单题不会重复调用，剩余单题继续排队执行；并发受同一
// 信号量约束，重启时不会出现恢复风暴。
func (s *LocalExecutor) resumePending() {
	if s.batchJobStore == nil {
		return
	}
	var records []storage.BatchJobRecord
	var err error
	if pager, ok := s.batchJobStore.(storage.BatchJobHistoryStore); ok {
		for offset := 0; ; offset += 200 {
			var page []storage.BatchJobRecord
			var total int
			page, total, err = pager.ListBatchJobsPage(s.ctx, "", "", "in_progress", 200, offset)
			if err != nil {
				break
			}
			records = append(records, page...)
			if len(page) == 0 || len(records) >= total {
				break
			}
		}
	} else {
		records, err = s.batchJobStore.ListBatchJobs(s.ctx, 200)
	}
	if err != nil {
		return
	}
	for _, record := range records {
		if record.Backend != localSingleAPIBackend || record.Status != "in_progress" {
			continue
		}
		s.mu.Lock()
		busy := s.active[record.ID]
		_, checkpointPending := s.checkpoints[record.ID]
		r, recovering := s.recovery[record.ID]
		s.mu.Unlock()
		if busy || checkpointPending || (recovering && time.Now().UnixMilli() < r.RetryAt) {
			continue
		}
		var output localBatchOutput
		if record.OutputJSON != "" {
			if json.Unmarshal([]byte(record.OutputJSON), &output) != nil {
				s.failCorruptQueue(s.ctx, record, "任务结果记录损坏，无法继续生成")
				continue
			}
		}
		// 显式计划优先：随机任务的单元清单在提交时冻结，恢复绝不重新抽样；
		// 旧任务无计划时按 PointsJSON × 每点数旧规则摊平。
		units, unitsErr := BatchUnitsFromRecord(record)
		if unitsErr != nil {
			s.failCorruptQueue(s.ctx, record, "任务单元记录无效，无法继续生成")
			continue
		}
		pending := unfinishedUnits(len(units), output)
		if len(pending) == 0 {
			// 恢复时发现所有单元均已完成（如重启发生在终态写入前），直接收口。
			s.finishQueue(record.ID, output)
			continue
		}
		snapshot, e := decodePromptSnapshot(record.PromptSnapshotJSON)
		if e != nil {
			s.updateJob(context.Background(), record.ID, "failed", record.Completed, record.Failed, output)
			continue
		}
		workflow, e := decodeWorkflowSnapshot(record.WorkflowSnapshotJSON)
		if e != nil {
			s.updateJob(context.Background(), record.ID, "failed", record.Completed, record.Failed, output)
			continue
		}
		s.startQueue(record.ID, units, pending, output, snapshot, workflow)
	}
}

// finishQueue 对无需再执行的任务写终态（幂等）。
func (s *LocalExecutor) finishQueue(jobID string, output localBatchOutput) {
	_ = s.persistOutput(s.ctx, jobID, "completed", output)
}

// RetryFailed 重跑失败生成单元。新流水线保留已首检单元；旧任务仍仅在导入前允许补生成：
// 导入后失败项视为放弃（成功部分已入题库），避免重跑产物与已导入结果脱节。
// 任务转回 in_progress 并异步执行，前端按既有轮询/自动导入流程继续。
func (s *LocalExecutor) RetryFailed(ctx context.Context, jobID string) (*BatchJob, error) {
	release, lockErr := s.lockCheckpointOperation(ctx, jobID)
	if lockErr != nil {
		return nil, lockErr
	}
	defer release()
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	stored, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Backend != localSingleAPIBackend {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, jobID)
	}
	if stored.ImportedAt != "" && stored.BackendProfile != "single-question-pipeline" {
		return nil, fmt.Errorf("%w：任务结果已导入，失败项不能重跑；如仍需出题请重新提交任务", ErrNotReady)
	}
	if stored.Status != "completed" && stored.Status != "complete" && stored.Status != "failed" {
		return nil, fmt.Errorf("%w，当前状态: %s", ErrNotReady, stored.Status)
	}
	var output localBatchOutput
	if stored.OutputJSON != "" {
		if err := json.Unmarshal([]byte(stored.OutputJSON), &output); err != nil {
			return nil, fmt.Errorf("解析任务结果失败: %w", err)
		}
	}
	pending := failedUnits(output)
	if len(pending) == 0 {
		return nil, fmt.Errorf("%w：该任务没有失败项可重跑", ErrNotReady)
	}
	// 显式计划优先：重跑只执行原冻结单元，随机任务不重新抽样。
	units, unitsErr := BatchUnitsFromRecord(*stored)
	if unitsErr != nil {
		return nil, unitsErr
	}
	output, err = normalizeOutput(units, output)
	if err != nil {
		return nil, err
	}
	stoppedPending := false
	for pos, point := range units {
		if !domain.IsEligibleClinicalPoint(point) && output.Items[pos].Status != "ok" && output.Items[pos].Status != "failed" {
			output.Items[pos].Status = "failed"
			output.Items[pos].Error = "选题范围调整：" + domain.ErrBasicMedicalScope.Error()
			output.Items[pos].NextAttemptAt = 0
			stoppedPending = true
		}
	}
	pending = failedUnits(output)
	retryable := make([]int, 0, len(pending))
	for _, pos := range pending {
		if pos >= len(units) {
			return nil, fmt.Errorf("任务单元与知识点快照不一致，不能重跑")
		}
		if domain.IsEligibleClinicalPoint(units[pos]) {
			retryable = append(retryable, pos)
		}
	}
	pending = retryable
	if len(pending) == 0 {
		if stoppedPending {
			if err := s.persistOutput(ctx, jobID, stored.Status, output); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%w：剩余失败项均因选题范围调整而终态停止，不能重跑", ErrNotReady)
	}
	snapshot, e := decodePromptSnapshot(stored.PromptSnapshotJSON)
	if e != nil {
		return nil, e
	}
	workflow, e := decodeWorkflowSnapshot(stored.WorkflowSnapshotJSON)
	if e != nil {
		return nil, e
	}
	// Commit retryable units as pending before exposing in_progress. Recovery
	// may run between this commit and startQueue, or after a process restart;
	// leaving old failed statuses would let it prematurely finish the retry.
	for _, pos := range pending {
		if pos < len(units) && output.Items[pos].Status == "failed" {
			output.Items[pos] = ImportItem{OutlineCode: units[pos].OutlineCode, Topic: units[pos].Topic, Status: "pending"}
		}
	}
	output.GenerationStartedAt = time.Now().UnixMilli()
	phaseOutput, err := json.Marshal(output)
	if err != nil {
		return nil, err
	}
	// 标记回执行中；前端据此恢复轮询，完成后再走导入流程。
	reactivation := func() storage.BatchJobRecord {
		record := *stored
		record.Status = "in_progress"
		_, record.Failed = outputCounts(output) // 政策停止项仍为终态，不重新占用配额。
		record.FinishedAt = ""
		record.OutputJSON = string(phaseOutput)
		return record
	}()
	if admitter, ok := s.batchJobStore.(storage.BatchJobRetryAdmitter); ok {
		err = admitter.ReactivateBatchJob(ctx, reactivation)
	} else {
		err = s.batchJobStore.UpdateBatchJob(ctx, reactivation)
	}
	if err != nil {
		return nil, err
	}
	// A terminal DB commit becomes visible just before the old queue releases
	// ownership. Wait for that final handoff rather than dropping an immediate retry.
	for {
		s.mu.Lock()
		active := s.active[jobID]
		s.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	// The previous terminal write may have committed but lost its reply. Once
	// its queue has exited, its checkpoint must not supersede this retry epoch.
	s.mu.Lock()
	delete(s.checkpoints, jobID)
	delete(s.recovery, jobID)
	s.mu.Unlock()
	s.startQueue(jobID, units, pending, output, snapshot, workflow)
	job, err := s.GetJobStatus(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *LocalExecutor) updateJob(ctx context.Context, jobID, status string, completed, failed int, output localBatchOutput) error {
	if s.batchJobStore == nil {
		return fmt.Errorf("批量任务存储不可用")
	}
	payload, err := json.Marshal(output)
	if err != nil {
		return err
	}
	job, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("批量任务不存在: %s", jobID)
	}
	job.Status = status
	job.Completed = completed
	job.Failed = failed
	job.OutputJSON = string(payload)
	// 终态记录完成时间（前端据此冻结已用时）；重跑回到执行中时清空。
	if isTerminalStatus(status) {
		job.FinishedAt = time.Now().Format(time.RFC3339Nano)
	} else {
		job.FinishedAt = ""
	}
	if output.Pipeline {
		if s.pipelineStore == nil {
			return fmt.Errorf("逐题流水线存储不可用")
		}
		return s.pipelineStore.SaveBatchPipelineProgress(ctx, *job)
	}
	return s.batchJobStore.UpdateBatchJob(ctx, *job)
}

// isTerminalStatus 判断批量任务状态是否为终态（前端冻结已用时的依据）。
func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "complete", "failed", "cancelled", "expired":
		return true
	}
	return false
}

func (s *LocalExecutor) GetJobStatus(ctx context.Context, jobID string) (*BatchJob, error) {
	job, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job == nil || job.Backend != localSingleAPIBackend {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, jobID)
	}
	result := localBatchJob(*job)
	result.Concurrency = s.concurrency()
	result.ProviderWaiting = !s.generator.Ready(ctx)
	// 状态查询携带逐单元明细（含失败原因），供前端展示每个知识点的进度；
	// 列表接口不解析结果快照，避免多任务列表的重复解析开销。
	var outputItems struct {
		Items    []ImportItem      `json:"items"`
		Recovery *RecoveryProgress `json:"recovery"`
	}
	if json.Unmarshal([]byte(job.OutputJSON), &outputItems) == nil {
		result.Items = outputItems.Items
		result.Recovery = outputItems.Recovery
	}
	s.mu.Lock()
	if r, ok := s.recovery[jobID]; ok {
		result.Recovery = &r
	}
	s.mu.Unlock()
	if result.Pipeline && job.ImportResult != "" {
		var live ImportResult
		if err := json.Unmarshal([]byte(job.ImportResult), &live); err != nil {
			return nil, err
		}
		result.PipelineResult = &live
	}
	return &result, nil
}

func (s *LocalExecutor) ListJobs(ctx context.Context, name, status string, limit int) ([]BatchJob, error) {
	records, err := func() ([]storage.BatchJobRecord, error) {
		if strings.TrimSpace(name) != "" {
			return s.batchJobStore.SearchBatchJobs(ctx, name, limit)
		}
		return s.batchJobStore.ListBatchJobs(ctx, limit)
	}()
	if err != nil {
		return nil, err
	}
	result := make([]BatchJob, 0, len(records))
	for _, record := range records {
		if record.Backend != localSingleAPIBackend || (status != "" && record.Status != status) {
			continue
		}
		result = append(result, localBatchJob(record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *LocalExecutor) ListJobsPage(ctx context.Context, ownerID, name, status string, limit, offset int) ([]BatchJob, int, error) {
	pager, ok := s.batchJobStore.(storage.BatchJobHistoryStore)
	if !ok {
		return nil, 0, ErrUnavailable
	}
	records, total, err := pager.ListBatchJobsPage(ctx, ownerID, name, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	jobs := make([]BatchJob, 0, len(records))
	for _, record := range records {
		jobs = append(jobs, localBatchJob(record))
	}
	return jobs, total, nil
}

func (s *LocalExecutor) ImportResults(ctx context.Context, jobID string, _ []domain.KnowledgePoint) (*ImportResult, error) {
	stored, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Backend != localSingleAPIBackend {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, jobID)
	}
	if stored.BackendProfile == "single-question-pipeline" {
		if stored.Status != "completed" && stored.Status != "complete" {
			return nil, fmt.Errorf("%w，生成仍在进行，请查看实时首检结果", ErrNotReady)
		}
		var result ImportResult
		if err := json.Unmarshal([]byte(stored.ImportResult), &result); err != nil {
			return nil, err
		}
		return &result, nil // 逐题已处理；读取结果不会恢复已淘汰题或创建第二轮首检。
	}
	if stored.ImportedAt != "" && stored.ImportResult != "" {
		var replay ImportResult
		if json.Unmarshal([]byte(stored.ImportResult), &replay) == nil {
			return &replay, nil
		}
	}
	if stored.Status != "completed" && stored.Status != "complete" {
		return nil, fmt.Errorf("%w，当前状态: %s", ErrNotReady, stored.Status)
	}
	if importer, ok := s.batchJobStore.(storage.LegacyBatchImportStore); ok {
		raw, err := importer.CommitLegacyBatchImport(ctx, jobID, storage.QuestionChangeFromContext(ctx), s.checkMaxAttempts)
		if err != nil {
			return nil, err
		}
		var result ImportResult
		if err = json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}
	claimed, err := s.batchJobStore.ClaimBatchJobImport(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, fmt.Errorf("该本地任务已导入或正在导入")
	}
	var output localBatchOutput
	if err := json.Unmarshal([]byte(stored.OutputJSON), &output); err != nil {
		_ = s.batchJobStore.ReleaseBatchJobImport(ctx, jobID)
		return nil, fmt.Errorf("解析本地批量结果失败: %w", err)
	}
	change := storage.QuestionChangeFromContext(ctx)
	result := &ImportResult{Items: append([]ImportItem(nil), output.Items...)}
	for _, question := range output.Questions {
		question.GenerationContext = output.PendingContexts[question.ID]
		question.PersonalExperiences = output.PendingExperiences[question.ID]
		question.OwnerID = change.OwnerID
		question.CreatedBy = change.Actor
		question.Status = domain.StatusAIDraft
		questionCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{
			Actor: change.Actor, OwnerID: change.OwnerID, ChangeType: "batch_generate", ChangeNote: "单题 API 批量生成",
		})
		if err := s.questionStore.SaveQuestion(questionCtx, question); err != nil {
			result.Failed++
			continue
		}
		result.Saved++
		result.QuestionIDs = append(result.QuestionIDs, question.ID)
	}
	// 导入即检查：落库成功的题目立刻排队首次 AI 质量检查（幂等，重复导入重放不重复入队）。
	// 触发点在执行器内部而非调用方 handler，保证 Web 与 CLI 两条链路行为一致。
	if s.checker != nil && len(result.QuestionIDs) > 0 {
		s.checker.CheckAsync(result.QuestionIDs...)
	}
	payload, _ := json.Marshal(result)
	if err := s.batchJobStore.SaveBatchJobImportResult(ctx, jobID, string(payload)); err != nil {
		return nil, err
	}
	return result, nil
}

func localBatchJob(record storage.BatchJobRecord) BatchJob {
	result := BatchJob{
		JobID: record.ID, OwnerID: record.OwnerID, Backend: localSingleAPIBackend, Model: record.Model,
		JobName: record.JobName, Status: record.Status, TotalCount: record.TotalCount,
		Completed: record.Completed, Failed: record.Failed, CreatedAt: parseBatchTimestamp(record.CreatedAt),
		CompletedAt: parseBatchTimestamp(record.FinishedAt), ImportedAt: record.ImportedAt, Tracked: true,
		Pipeline: record.BackendProfile == "single-question-pipeline",
	}
	if strings.Contains(record.OutputJSON, `"recovery"`) || strings.Contains(record.OutputJSON, `"generation_started_at"`) {
		var metadata struct {
			Recovery            *RecoveryProgress `json:"recovery"`
			GenerationStartedAt int64             `json:"generation_started_at"`
		}
		if json.Unmarshal([]byte(record.OutputJSON), &metadata) == nil {
			result.Recovery = metadata.Recovery
			result.GenerationStartedAt = metadata.GenerationStartedAt
		}
	}
	if plan, err := BatchPlanFromRecord(record); err == nil && plan != nil {
		result.PlanMode = plan.Mode
		result.PlanCandidates = plan.CandidateCount
		result.PlanRequestedTotal = plan.RequestedTotal
	}
	return result
}

func parseBatchTimestamp(value string) int64 {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

var _ Executor = (*LocalExecutor)(nil)

func decodePromptSnapshot(raw string) (*domain.GenerationPromptSnapshot, error) {
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var s domain.GenerationPromptSnapshot
	if e := json.Unmarshal([]byte(raw), &s); e != nil {
		return nil, fmt.Errorf("任务个人规则快照损坏: %w", e)
	}
	return &s, nil
}

func decodeWorkflowSnapshot(raw string) (*domain.KnowledgeWorkflowSnapshot, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var scope domain.KnowledgeWorkflowSnapshot
	if err := json.Unmarshal([]byte(raw), &scope); err != nil {
		return nil, fmt.Errorf("知识库工作流快照损坏")
	}
	if err := knowledgebase.ValidateWorkflowPresentation(scope.Version, scope.Presentation); err != nil {
		return nil, fmt.Errorf("知识库工作流呈现快照损坏: %w", err)
	}
	return &scope, nil
}
