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

	result, err := executor.ImportResults(ctx, jobID, nil)
	if err != nil || result.Saved != 2 || len(result.QuestionIDs) != 2 {
		t.Fatalf("import local batch: result=%+v err=%v", result, err)
	}
	questions, err := questionStore.ListQuestions(context.Background())
	if err != nil || len(questions) != 2 || questions[0].OwnerID != "owner-1" || questions[1].OwnerID != "owner-1" {
		t.Fatalf("local batch owner snapshot lost: questions=%+v err=%v", questions, err)
	}
}
