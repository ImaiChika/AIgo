package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
)

// localSingleAPIBackend 是本地批量执行器的 backend 标识。
const localSingleAPIBackend = "local_single_api"

// LocalExecutor 把批量任务拆成多个单题生成请求。它不使用百炼 Files/Batches，
// 每道题复用单题 generator.Service，任务进度和生成结果快照落在 batch_jobs。
// 执行受全局信号量约束：新提交、重启恢复、失败项重跑共用同一并发上限，
// 多任务同时执行时实际并发生成调用不会超过 MaxConcurrency。
type LocalExecutor struct {
	generator     *generator.Service
	questionStore storage.QuestionStore
	batchJobStore storage.BatchJobStore
	model         string

	// MaxConcurrency 全局同时执行的生成请求数上限（信号量容量）。
	// 零值时由 NewLocalExecutor 置为 2；测试可调小调大。
	MaxConcurrency int
	// MaxAttempts 单个生成项的最多尝试次数（含首次）；网络/限流类失败按
	// RetryDelays 退避后重试，耗尽后该项保持 failed，可经重跑入口再试。
	MaxAttempts int
	// RetryDelays 相邻两次尝试之间的退避间隔；长度不足时重复最后一个。
	RetryDelays []time.Duration

	sem chan struct{}
}

type localBatchOutput struct {
	Questions []domain.A2Question `json:"questions"`
	Items     []ImportItem        `json:"items"`
}

// localDefaultConcurrency 默认全局并发上限：与 AI 检查 worker 同量级，
// 避免批量任务把共享的生成端点打满。
const localDefaultConcurrency = 2

func NewLocalExecutor(gen *generator.Service, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore, model string) *LocalExecutor {
	executor := &LocalExecutor{
		generator:      gen,
		questionStore:  questionStore,
		batchJobStore:  batchJobStore,
		model:          strings.TrimSpace(model),
		MaxConcurrency: localDefaultConcurrency,
		MaxAttempts:    3,
		RetryDelays:    []time.Duration{2 * time.Second, 5 * time.Second},
	}
	if executor.MaxConcurrency < 1 {
		executor.MaxConcurrency = 1
	}
	executor.sem = make(chan struct{}, executor.MaxConcurrency)
	go executor.resumePending()
	return executor
}

func (s *LocalExecutor) Capabilities() Capabilities {
	available := s.generator != nil
	message := fmt.Sprintf("每道题单独调用单题生成 API，任务结果保存在本地，不使用 Qwen 批量平台；全局并发上限 %d", s.concurrency())
	if !available {
		message = "单题生成服务未初始化"
	}
	return Capabilities{
		Backend:       localSingleAPIBackend,
		Available:     available,
		ExecutionMode: localSingleAPIBackend,
		Model:         s.model,
		Message:       message,
	}
}

func (s *LocalExecutor) concurrency() int {
	if s.sem == nil {
		return s.MaxConcurrency
	}
	return cap(s.sem)
}

func (s *LocalExecutor) GenerateAndSubmit(ctx context.Context, points []domain.KnowledgePoint, countPerPoint int, jobName string) (string, int, error) {
	if !s.Capabilities().Available {
		return "", 0, fmt.Errorf("%w: %s", ErrUnavailable, s.Capabilities().Message)
	}
	if len(points) == 0 {
		return "", 0, fmt.Errorf("知识点列表为空")
	}
	if countPerPoint <= 0 {
		countPerPoint = 1
	}
	if strings.TrimSpace(jobName) == "" {
		jobName = fmt.Sprintf("单题 API 批量任务 %s", time.Now().Format("01-02 15:04"))
	}
	jobID := fmt.Sprintf("local-%d", time.Now().UnixNano())
	pointsJSON, _ := json.Marshal(points)
	record := storage.BatchJobRecord{
		ID:             jobID,
		OwnerID:        storage.QuestionChangeFromContext(ctx).OwnerID,
		Backend:        localSingleAPIBackend,
		BackendProfile: "single-question-api",
		Model:          s.model,
		JobName:        jobName,
		Status:         "in_progress",
		TotalCount:     len(points) * countPerPoint,
		PointsJSON:     string(pointsJSON),
		OutputJSON:     `{"questions":[],"items":[]}`,
		CreatedAt:      time.Now().Format(time.RFC3339Nano),
	}
	if s.batchJobStore == nil {
		return "", 0, fmt.Errorf("批量任务持久化服务不可用")
	}
	if err := s.batchJobStore.SaveBatchJob(ctx, record); err != nil {
		return "", 0, err
	}
	// 新提交与重启恢复共用同一执行入口，受全局并发上限约束。
	go s.runQueue(jobID, flattenUnits(points, countPerPoint), resumeUnits(len(points)*countPerPoint, 0), localBatchOutput{Questions: []domain.A2Question{}, Items: []ImportItem{}})
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

// runQueue 顺序执行 pending 中的生成单元，结果按单元下标并入 output。
// 每个单元在执行前占用全局信号量名额，执行完释放；多个任务并行时
// 实际同时进行的生成调用数不超过并发上限。
func (s *LocalExecutor) runQueue(jobID string, units []domain.KnowledgePoint, pending []int, output localBatchOutput) {
	ctx := context.Background()
	for _, pos := range pending {
		if pos >= len(units) {
			continue
		}
		point := units[pos]
		item := s.generateUnit(ctx, point)
		// 恢复/重跑场景按位置替换，新提交场景顺序追加。
		if pos < len(output.Items) {
			output.Items[pos] = item
		} else {
			output.Items = append(output.Items, item)
		}
		completed, failed := 0, 0
		for _, existing := range output.Items {
			if existing.Status == "ok" {
				completed += existing.Count
			} else {
				failed++
			}
		}
		if item.Status == "ok" {
			output.Questions = append(output.Questions, item.question)
		}
		s.updateJob(ctx, jobID, "in_progress", completed, failed, output)
	}
	// 终态重新计数：重跑替换条目后以当前 Items 为准。
	completed, failed := 0, 0
	for _, existing := range output.Items {
		if existing.Status == "ok" {
			completed += existing.Count
		} else {
			failed++
		}
	}
	s.updateJob(ctx, jobID, "completed", completed, failed, output)
}

// generateUnit 执行单个生成单元：占用并发名额，按退避策略重试可重试错误。
func (s *LocalExecutor) generateUnit(ctx context.Context, point domain.KnowledgePoint) ImportItem {
	item := ImportItem{OutlineCode: point.OutlineCode}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	var lastErr error
	for attempt := 0; attempt < s.maxAttempts(); attempt++ {
		if attempt > 0 {
			delays := s.RetryDelays
			delay := time.Second
			if len(delays) > 0 {
				idx := attempt - 1
				if idx >= len(delays) {
					idx = len(delays) - 1
				}
				delay = delays[idx]
			}
			select {
			case <-ctx.Done():
				item.Error = ctx.Err().Error()
				item.Status = "failed"
				return item
			case <-time.After(delay):
			}
		}
		questions, err := s.generator.Generate(ctx, domain.GenerationRequest{
			Subject:         point.Subject,
			Difficulty:      domain.Difficulty("0.65"),
			KnowledgePoints: []domain.KnowledgePoint{point},
			Count:           1,
		})
		if err == nil && len(questions) > 0 {
			item.Status = "ok"
			item.Count = 1
			item.question = questions[0]
			item.Error = ""
			return item
		}
		if err == nil {
			// 返回为空属确定性失败，重试同样为空，直接落败。
			item.Error = "单题生成结果为空"
			item.Status = "failed"
			return item
		}
		lastErr = err
		if errors.Is(err, context.Canceled) {
			break
		}
	}
	item.Error = lastErr.Error()
	item.Status = "failed"
	return item
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
	records, err := s.batchJobStore.ListBatchJobs(context.Background(), 200)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.Backend != localSingleAPIBackend || record.Status != "in_progress" || record.PointsJSON == "" {
			continue
		}
		var points []domain.KnowledgePoint
		if json.Unmarshal([]byte(record.PointsJSON), &points) != nil || len(points) == 0 {
			continue
		}
		var output localBatchOutput
		if record.OutputJSON != "" {
			_ = json.Unmarshal([]byte(record.OutputJSON), &output)
		}
		total := len(points)
		if record.TotalCount > 0 {
			total = record.TotalCount
		}
		countPerPoint := total / len(points)
		if countPerPoint < 1 {
			countPerPoint = 1
		}
		units := flattenUnits(points, countPerPoint)
		done := len(output.Items)
		if done > len(units) {
			done = len(units)
		}
		pending := resumeUnits(len(units), done)
		if len(pending) == 0 {
			// 恢复时发现所有单元均已完成（如重启发生在终态写入前），直接收口。
			s.finishQueue(record.ID, output)
			continue
		}
		go s.runQueue(record.ID, units, pending, output)
	}
}

// finishQueue 对无需再执行的任务写终态（幂等）。
func (s *LocalExecutor) finishQueue(jobID string, output localBatchOutput) {
	completed, failed := 0, 0
	for _, existing := range output.Items {
		if existing.Status == "ok" {
			completed += existing.Count
		} else {
			failed++
		}
	}
	s.updateJob(context.Background(), jobID, "completed", completed, failed, output)
}

// RetryFailed 重跑任务中的失败生成单元。仅任务所有者可在结果导入前调用：
// 导入后失败项视为放弃（成功部分已入题库），避免重跑产物与已导入结果脱节。
// 任务转回 in_progress 并异步执行，前端按既有轮询/自动导入流程继续。
func (s *LocalExecutor) RetryFailed(ctx context.Context, jobID string) (*BatchJob, error) {
	stored, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Backend != localSingleAPIBackend {
		return nil, fmt.Errorf("本地单题批量任务不存在: %s", jobID)
	}
	if stored.ImportedAt != "" {
		return nil, fmt.Errorf("任务结果已导入，失败项不能重跑；如仍需出题请重新提交任务")
	}
	if stored.Status != "completed" && stored.Status != "complete" && stored.Status != "failed" {
		return nil, fmt.Errorf("%w，当前状态: %s", ErrNotReady, stored.Status)
	}
	var points []domain.KnowledgePoint
	if json.Unmarshal([]byte(stored.PointsJSON), &points) != nil || len(points) == 0 {
		return nil, fmt.Errorf("任务缺少知识点快照，不能重跑")
	}
	var output localBatchOutput
	if stored.OutputJSON != "" {
		if err := json.Unmarshal([]byte(stored.OutputJSON), &output); err != nil {
			return nil, fmt.Errorf("解析任务结果失败: %w", err)
		}
	}
	pending := failedUnits(output)
	if len(pending) == 0 {
		return nil, fmt.Errorf("该任务没有失败项可重跑")
	}
	total := len(points)
	if stored.TotalCount > 0 {
		total = stored.TotalCount
	}
	countPerPoint := total / len(points)
	if countPerPoint < 1 {
		countPerPoint = 1
	}
	units := flattenUnits(points, countPerPoint)
	// 标记回执行中；前端据此恢复轮询，完成后再走导入流程。
	if err := s.batchJobStore.UpdateBatchJob(ctx, func() storage.BatchJobRecord {
		record := *stored
		record.Status = "in_progress"
		return record
	}()); err != nil {
		return nil, err
	}
	go s.runQueue(jobID, units, pending, output)
	job, err := s.GetJobStatus(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *LocalExecutor) updateJob(ctx context.Context, jobID, status string, completed, failed int, output localBatchOutput) {
	if s.batchJobStore == nil {
		return
	}
	payload, _ := json.Marshal(output)
	job, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil || job == nil {
		return
	}
	job.Status = status
	job.Completed = completed
	job.Failed = failed
	job.OutputJSON = string(payload)
	_ = s.batchJobStore.UpdateBatchJob(ctx, *job)
}

func (s *LocalExecutor) GetJobStatus(ctx context.Context, jobID string) (*BatchJob, error) {
	job, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job == nil || job.Backend != localSingleAPIBackend {
		return nil, fmt.Errorf("本地单题批量任务不存在: %s", jobID)
	}
	result := localBatchJob(*job)
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

func (s *LocalExecutor) ImportResults(ctx context.Context, jobID string, _ []domain.KnowledgePoint) (*ImportResult, error) {
	stored, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Backend != localSingleAPIBackend {
		return nil, fmt.Errorf("本地单题批量任务不存在: %s", jobID)
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
	payload, _ := json.Marshal(result)
	if err := s.batchJobStore.SaveBatchJobImportResult(ctx, jobID, string(payload)); err != nil {
		return nil, err
	}
	return result, nil
}

func localBatchJob(record storage.BatchJobRecord) BatchJob {
	return BatchJob{
		JobID: record.ID, OwnerID: record.OwnerID, Backend: localSingleAPIBackend, Model: record.Model,
		JobName: record.JobName, Status: record.Status, TotalCount: record.TotalCount,
		Completed: record.Completed, Failed: record.Failed, CreatedAt: parseLocalBatchCreatedAt(record.CreatedAt),
		ImportedAt: record.ImportedAt, Tracked: true,
	}
}

func parseLocalBatchCreatedAt(value string) int64 {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

var _ Executor = (*LocalExecutor)(nil)
