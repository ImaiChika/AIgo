package batch

import (
	"context"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

type localTestLLM struct{}

func (localTestLLM) Complete(context.Context, []llm.Message, llm.GenerateOptions) (string, error) {
	return `[{"clinical_stem":"男，50岁。突发胸痛2小时。该患者最可能的诊断是","options":[{"label":"A","text":"急性心肌梗死"},{"label":"B","text":"主动脉夹层"},{"label":"C","text":"肺栓塞"},{"label":"D","text":"气胸"},{"label":"E","text":"心包炎"}],"answer":"A","explanation":"正确答案为A。病例表现符合急性心肌梗死。B项、C项、D项、E项均与现有表现不符。故选A。","difficulty":"0.65","cognitive_level":"应用","exam_points":"诊断与鉴别诊断"}]`, nil
}

type localBatchStore struct {
	mu  sync.Mutex
	job storage.BatchJobRecord
}

func (s *localBatchStore) SaveBatchJob(_ context.Context, job storage.BatchJobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job = job
	return nil
}

func (s *localBatchStore) UpdateBatchJob(_ context.Context, job storage.BatchJobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job = job
	return nil
}

func (s *localBatchStore) GetBatchJob(context.Context, string) (*storage.BatchJobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.job
	if job.ID == "" {
		return nil, nil
	}
	return &job, nil
}

func (s *localBatchStore) ListBatchJobs(context.Context, int) ([]storage.BatchJobRecord, error) {
	job, _ := s.GetBatchJob(context.Background(), "")
	if job == nil {
		return []storage.BatchJobRecord{}, nil
	}
	return []storage.BatchJobRecord{*job}, nil
}

func (s *localBatchStore) SearchBatchJobs(ctx context.Context, _ string, _ int) ([]storage.BatchJobRecord, error) {
	return s.ListBatchJobs(ctx, 0)
}

func (s *localBatchStore) ClaimBatchJobImport(_ context.Context, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.ImportedAt != "" {
		return false, nil
	}
	s.job.ImportedAt = time.Now().Format(time.RFC3339Nano)
	return true, nil
}

func (s *localBatchStore) SaveBatchJobImportResult(_ context.Context, _ string, resultJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job.ImportResult = resultJSON
	return nil
}

func (s *localBatchStore) ReleaseBatchJobImport(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job.ImportedAt = ""
	return nil
}

func TestLocalExecutorUsesSingleQuestionAPIAndImportsResults(t *testing.T) {
	questionStore := testutil.NewMemoryStore()
	jobStore := &localBatchStore{}
	gen := generator.NewService(localTestLLM{})
	executor := NewLocalExecutor(gen, questionStore, jobStore, "test-model")
	ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{Actor: "teacher", OwnerID: "owner-1"})
	points := []domain.KnowledgePoint{{ID: "kp-1", Topic: "胸痛", Subject: "心血管系统", OutlineCode: "1.1"}, {ID: "kp-2", Topic: "呼吸困难", Subject: "呼吸系统", OutlineCode: "1.2"}}

	jobID, count, err := executor.GenerateAndSubmit(ctx, points, 1, "本地单题测试")
	if err != nil || count != 2 {
		t.Fatalf("submit local batch: id=%s count=%d err=%v", jobID, count, err)
	}
	var job *BatchJob
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err = executor.GetJobStatus(context.Background(), jobID)
		if err == nil && job.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || job == nil || job.Status != "completed" || job.Completed != 2 || job.Failed != 0 {
		t.Fatalf("local batch did not complete: job=%+v err=%v", job, err)
	}
	// 终态任务必须携带完成时间，前端据此冻结已用时。
	if job.CompletedAt == 0 || job.CompletedAt < job.CreatedAt {
		t.Fatalf("completed job missing finished_at: created=%d completed=%d", job.CreatedAt, job.CompletedAt)
	}

	result, err := executor.ImportResults(ctx, jobID, nil)
	if err != nil || result.Saved != 2 || len(result.QuestionIDs) != 2 {
		t.Fatalf("import local batch: result=%+v err=%v", result, err)
	}
	questions, err := questionStore.ListQuestions(context.Background())
	if err != nil || len(questions) != 2 || questions[0].OwnerID != "owner-1" || questions[1].OwnerID != "owner-1" {
		t.Fatalf("local batch owner snapshot lost: questions=%+v err=%v", questions, err)
	}
}

// recordingChecker 记录 CheckAsync 收到的题目 ID，用于验证导入后检查触发。
type recordingChecker struct {
	mu    sync.Mutex
	ids   []string
	calls int
}

func (c *recordingChecker) CheckAsync(questionIDs ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.ids = append(c.ids, questionIDs...)
}

// TestLocalExecutorImportTriggersDraftChecker 导入落库必须由执行器统一触发首次
// AI 检查：Web 与 CLI 共用 ImportResults，任何入口导入成功都应排队检查，
// 且重复导入（幂等重放）不重复触发。
func TestLocalExecutorImportTriggersDraftChecker(t *testing.T) {
	questionStore := testutil.NewMemoryStore()
	jobStore := &localBatchStore{}
	gen := generator.NewService(localTestLLM{})
	executor := NewLocalExecutor(gen, questionStore, jobStore, "test-model")
	checker := &recordingChecker{}
	executor.SetDraftChecker(checker)
	ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{Actor: "teacher", OwnerID: "owner-1"})
	points := []domain.KnowledgePoint{{ID: "kp-1", Topic: "胸痛", Subject: "心血管系统", OutlineCode: "1.1"}}

	jobID, _, err := executor.GenerateAndSubmit(ctx, points, 1, "导入触发检查")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, statusErr := executor.GetJobStatus(context.Background(), jobID)
		if statusErr == nil && job != nil && job.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	result, err := executor.ImportResults(ctx, jobID, nil)
	if err != nil || result.Saved != 1 || len(result.QuestionIDs) != 1 {
		t.Fatalf("import: result=%+v err=%v", result, err)
	}
	checker.mu.Lock()
	firstCalls, firstIDs := checker.calls, append([]string(nil), checker.ids...)
	checker.mu.Unlock()
	if firstCalls != 1 || len(firstIDs) != 1 || firstIDs[0] != result.QuestionIDs[0] {
		t.Fatalf("导入后应恰好触发一次检查: calls=%d ids=%v", firstCalls, firstIDs)
	}

	// 幂等重放：第二次导入返回缓存结果，不再触发检查
	if _, err := executor.ImportResults(ctx, jobID, nil); err != nil {
		t.Fatalf("replay import: %v", err)
	}
	checker.mu.Lock()
	defer checker.mu.Unlock()
	if checker.calls != 1 {
		t.Fatalf("重放导入不应重复触发检查: calls=%d", checker.calls)
	}
}
