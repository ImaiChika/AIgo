package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
)

// LocalExecutor 把批量任务拆成多个单题生成请求。它不使用百炼 Files/Batches，
// 每道题复用单题 generator.Service，任务进度和生成结果快照落在 batch_jobs。
type LocalExecutor struct {
	generator     *generator.Service
	questionStore storage.QuestionStore
	batchJobStore storage.BatchJobStore
	model         string
}

type localBatchOutput struct {
	Questions []domain.A2Question `json:"questions"`
	Items     []ImportItem        `json:"items"`
}

func NewLocalExecutor(gen *generator.Service, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore, model string) *LocalExecutor {
	executor := &LocalExecutor{
		generator:     gen,
		questionStore: questionStore,
		batchJobStore: batchJobStore,
		model:         strings.TrimSpace(model),
	}
	go executor.resumePending()
	return executor
}

func (s *LocalExecutor) Capabilities() Capabilities {
	available := s.generator != nil
	message := "每道题单独调用单题生成 API，任务结果保存在本地，不使用 Qwen 批量平台"
	if !available {
		message = "单题生成服务未初始化"
	}
	return Capabilities{
		Backend:       "local_single_api",
		Available:     available,
		ExecutionMode: "local_single_api",
		Model:         s.model,
		Message:       message,
	}
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
		Backend:        "local_single_api",
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
	go s.runJob(jobID, points, countPerPoint)
	return jobID, record.TotalCount, nil
}

func (s *LocalExecutor) runJob(jobID string, points []domain.KnowledgePoint, countPerPoint int) {
	s.runJobFrom(jobID, points, countPerPoint, localBatchOutput{Questions: []domain.A2Question{}, Items: []ImportItem{}}, 0)
}

func (s *LocalExecutor) runJobFrom(jobID string, points []domain.KnowledgePoint, countPerPoint int, output localBatchOutput, processed int) {
	ctx := context.Background()
	completed, failed := 0, 0
	for _, item := range output.Items {
		if item.Status == "ok" {
			completed += item.Count
		} else {
			failed++
		}
	}
	index := 0
	for _, point := range points {
		for i := 0; i < countPerPoint; i++ {
			if index < processed {
				index++
				continue
			}
			questions, err := s.generator.Generate(ctx, domain.GenerationRequest{
				Subject:         point.Subject,
				Difficulty:      domain.Difficulty("0.65"),
				KnowledgePoints: []domain.KnowledgePoint{point},
				Count:           1,
			})
			item := ImportItem{OutlineCode: point.OutlineCode}
			if err != nil || len(questions) == 0 {
				failed++
				if err != nil {
					item.Error = err.Error()
				} else {
					item.Error = "单题生成结果为空"
				}
				item.Status = "failed"
			} else {
				completed++
				item.Status = "ok"
				item.Count = 1
				output.Questions = append(output.Questions, questions[0])
			}
			output.Items = append(output.Items, item)
			s.updateJob(ctx, jobID, "in_progress", completed, failed, output)
			index++
		}
	}
	s.updateJob(ctx, jobID, "completed", completed, failed, output)
}

// resumePending 接管服务重启前尚未完成的本地任务。题目与知识点快照均已
// 落库，已完成的单题不会重复调用，剩余单题继续排队执行。
func (s *LocalExecutor) resumePending() {
	if s.batchJobStore == nil {
		return
	}
	records, err := s.batchJobStore.ListBatchJobs(context.Background(), 200)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.Backend != "local_single_api" || record.Status != "in_progress" || record.PointsJSON == "" {
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
		processed := len(output.Items)
		countPerPoint := record.TotalCount / len(points)
		if countPerPoint < 1 {
			countPerPoint = 1
		}
		go s.runJobFrom(record.ID, points, countPerPoint, output, processed)
	}
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
	if job == nil || job.Backend != "local_single_api" {
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
		if record.Backend != "local_single_api" || (status != "" && record.Status != status) {
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
	if stored == nil || stored.Backend != "local_single_api" {
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
		JobID: record.ID, OwnerID: record.OwnerID, Backend: "local_single_api", Model: record.Model,
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
