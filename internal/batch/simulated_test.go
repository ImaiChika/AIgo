package batch

import (
	"context"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestSimulatedExecutorRunsWithoutExternalAPI(t *testing.T) {
	executor := NewSimulatedExecutor(nil, DashScopeConfig{
		BaseURL: "https://example.invalid/v1",
		Model:   "test-model",
	}, nil)
	ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{OwnerID: "owner-1"})
	jobID, total, err := executor.GenerateAndSubmit(ctx, []domain.KnowledgePoint{{ID: "kp-1"}}, 2, "模拟任务")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("request count=%d, want 2", total)
	}

	executor.mu.Lock()
	record := executor.jobs[jobID]
	record.CreatedAt = time.Now().Add(-10 * time.Second).Format(time.RFC3339Nano)
	executor.jobs[jobID] = record
	executor.mu.Unlock()

	job, err := executor.GetJobStatus(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" || job.Completed != 2 {
		t.Fatalf("simulated job=%+v", job)
	}
	result, err := executor.ImportResults(ctx, jobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Simulated || result.Saved != 0 || len(result.QuestionIDs) != 0 {
		t.Fatalf("unexpected simulated import result=%+v", result)
	}
}
