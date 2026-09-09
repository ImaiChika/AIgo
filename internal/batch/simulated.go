package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
)

// SimulatedExecutor 用于 Web 批量推理流程验收。
// 它读取当前活动 AI 配置用于展示模型/端点状态，但不会发起任何外部请求，
// 也不会生成或写入真实题目；只模拟提交、轮询、完成和结果导入幂等流程。
type SimulatedExecutor struct {
	resolver      llm.RuntimeConfigResolver
	fallback      DashScopeConfig
	batchJobStore storage.BatchJobStore

	mu   sync.Mutex
	jobs map[string]storage.BatchJobRecord
}

// NewSimulatedExecutor 创建 Web 专用的批量流程模拟器。
func NewSimulatedExecutor(resolver llm.RuntimeConfigResolver, fallback DashScopeConfig, batchJobStore storage.BatchJobStore) *SimulatedExecutor {
	return &SimulatedExecutor{
		resolver:      resolver,
		fallback:      fallback,
		batchJobStore: batchJobStore,
		jobs:          make(map[string]storage.BatchJobRecord),
	}
}

func (s *SimulatedExecutor) resolveConfig(ctx context.Context) DashScopeConfig {
	cfg := s.fallback
	if s.resolver != nil {
		if qwen, ok, err := s.resolver.ResolveQwenConfig(ctx, llm.PurposeBatch); err == nil && ok {
			cfg.APIKey = qwen.APIKey
			cfg.BaseURL = qwen.BaseURL
			cfg.Model = qwen.Model
			cfg.Profile = "active-ai-config"
		}
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "qwen3.5-flash"
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	}
	return cfg
}

// Capabilities 返回当前活动配置的脱敏状态；模拟模式不要求真实 API Key。
func (s *SimulatedExecutor) Capabilities() Capabilities {
	cfg := s.resolveConfig(context.Background())
	return Capabilities{
		Backend:       "simulation",
		Available:     strings.TrimSpace(cfg.BaseURL) != "" && strings.TrimSpace(cfg.Model) != "",
		ExecutionMode: "simulated",
		Model:         cfg.Model,
		Message:       "模拟批量流程：只验证提交、进度、完成和导入，不调用外部 API，也不写入真实题目",
		Endpoint:      cfg.BaseURL,
	}
}

func (s *SimulatedExecutor) GenerateAndSubmit(ctx context.Context, points []domain.KnowledgePoint, countPerPoint int, jobName string) (string, int, error) {
	if !s.Capabilities().Available {
		return "", 0, fmt.Errorf("%w: 模拟批量配置缺少模型或 API 地址", ErrUnavailable)
	}
	if len(points) == 0 {
		return "", 0, fmt.Errorf("知识点列表为空")
	}
	if countPerPoint <= 0 {
		countPerPoint = 1
	}
	if strings.TrimSpace(jobName) == "" {
		jobName = fmt.Sprintf("模拟批量任务 %s", time.Now().Format("01-02 15:04"))
	}
	cfg := s.resolveConfig(ctx)
	jobID := fmt.Sprintf("sim-%d", time.Now().UnixNano())
	total := len(points) * countPerPoint
	pointsJSON, _ := json.Marshal(points)
	record := storage.BatchJobRecord{
		ID:             jobID,
		OwnerID:        storage.QuestionChangeFromContext(ctx).OwnerID,
		Backend:        "simulation",
		BackendProfile: "active-ai-config",
		Model:          cfg.Model,
		JobName:        jobName,
		Status:         "validating",
		TotalCount:     total,
		PointsJSON:     string(pointsJSON),
		OutputFileID:   "simulated-output-" + jobID,
		CreatedAt:      time.Now().Format(time.RFC3339Nano),
	}
	if err := s.saveJob(ctx, record); err != nil {
		return "", 0, err
	}
	return jobID, total, nil
}

func (s *SimulatedExecutor) GetJobStatus(ctx context.Context, jobID string) (*BatchJob, error) {
	record, err := s.getJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Backend != "simulation" {
		return nil, fmt.Errorf("模拟批量任务不存在: %s", jobID)
	}
	createdAt := parseSimulationTime(record.CreatedAt)
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if record.Status != "completed" && record.Status != "complete" {
		elapsed := time.Since(createdAt)
		switch {
		case elapsed < 2*time.Second:
			record.Status = "validating"
			record.Completed = 0
		case elapsed < 5*time.Second:
			record.Status = "in_progress"
			steps := int(elapsed/time.Second) - 1
			if steps < 1 {
				steps = 1
			}
			record.Completed = record.TotalCount * steps / 3
			if record.Completed >= record.TotalCount {
				record.Completed = record.TotalCount - 1
			}
		default:
			record.Status = "completed"
			record.Completed = record.TotalCount
		}
		if record.Status == "completed" {
			record.Completed = record.TotalCount
		}
		if err := s.saveJob(ctx, *record); err != nil {
			return nil, err
		}
	}
	job := simulatedBatchJob(*record)
	return &job, nil
}

func (s *SimulatedExecutor) ListJobs(ctx context.Context, name, status string, limit int) ([]BatchJob, error) {
	var records []storage.BatchJobRecord
	var err error
	if s.batchJobStore != nil {
		if strings.TrimSpace(name) != "" {
			records, err = s.batchJobStore.SearchBatchJobs(ctx, name, limit)
		} else {
			records, err = s.batchJobStore.ListBatchJobs(ctx, limit)
		}
		if err != nil {
			return nil, err
		}
	} else {
		s.mu.Lock()
		for _, record := range s.jobs {
			records = append(records, record)
		}
		s.mu.Unlock()
	}
	result := make([]BatchJob, 0, len(records))
	for _, record := range records {
		if record.Backend != "simulation" || (status != "" && record.Status != status) {
			continue
		}
		if name != "" && !strings.Contains(strings.ToLower(record.JobName), strings.ToLower(name)) {
			continue
		}
		result = append(result, simulatedBatchJob(record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *SimulatedExecutor) ImportResults(ctx context.Context, jobID string, _ []domain.KnowledgePoint) (*ImportResult, error) {
	record, err := s.getJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Backend != "simulation" {
		return nil, fmt.Errorf("模拟批量任务不存在: %s", jobID)
	}
	if record.ImportedAt != "" && record.ImportResult != "" {
		var replay ImportResult
		if json.Unmarshal([]byte(record.ImportResult), &replay) == nil {
			return &replay, nil
		}
	}
	if record.Status != "completed" && record.Status != "complete" {
		return nil, fmt.Errorf("%w，当前状态: %s", ErrNotReady, record.Status)
	}
	if s.batchJobStore != nil {
		claimed, err := s.batchJobStore.ClaimBatchJobImport(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if !claimed {
			return nil, fmt.Errorf("该模拟任务已导入或正在导入")
		}
	}
	result := &ImportResult{
		Simulated: true,
		Message:   "模拟流程已完成：未调用外部批量 API，未写入真实题目",
		Items:     []ImportItem{},
	}
	payload, _ := json.Marshal(result)
	if s.batchJobStore != nil {
		if err := s.batchJobStore.SaveBatchJobImportResult(ctx, jobID, string(payload)); err != nil {
			return nil, err
		}
	} else {
		s.mu.Lock()
		if stored, ok := s.jobs[jobID]; ok {
			stored.ImportedAt = time.Now().Format(time.RFC3339Nano)
			stored.ImportResult = string(payload)
			s.jobs[jobID] = stored
		}
		s.mu.Unlock()
	}
	return result, nil
}

func (s *SimulatedExecutor) saveJob(ctx context.Context, record storage.BatchJobRecord) error {
	if s.batchJobStore != nil {
		if existing, _ := s.batchJobStore.GetBatchJob(ctx, record.ID); existing != nil {
			return s.batchJobStore.UpdateBatchJob(ctx, record)
		}
		return s.batchJobStore.SaveBatchJob(ctx, record)
	}
	s.mu.Lock()
	s.jobs[record.ID] = record
	s.mu.Unlock()
	return nil
}

func (s *SimulatedExecutor) getJob(ctx context.Context, id string) (*storage.BatchJobRecord, error) {
	if s.batchJobStore != nil {
		return s.batchJobStore.GetBatchJob(ctx, id)
	}
	s.mu.Lock()
	record, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return nil, nil
	}
	return &record, nil
}

func simulatedBatchJob(record storage.BatchJobRecord) BatchJob {
	return BatchJob{
		JobID:        record.ID,
		OwnerID:      record.OwnerID,
		Backend:      "simulation",
		Model:        record.Model,
		JobName:      record.JobName,
		Status:       record.Status,
		TotalCount:   record.TotalCount,
		Completed:    record.Completed,
		Failed:       record.Failed,
		OutputFileID: record.OutputFileID,
		CreatedAt:    parseBatchCreatedAt(record.CreatedAt),
		ImportedAt:   record.ImportedAt,
		Tracked:      true,
	}
}

func parseBatchCreatedAt(value string) int64 {
	created := parseSimulationTime(value)
	if created.IsZero() {
		return 0
	}
	return created.Unix()
}

func parseSimulationTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

var _ Executor = (*SimulatedExecutor)(nil)
