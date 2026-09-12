package testutil

import (
	"context"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestMemoryGenerationRunLifecycleAndIdempotency(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	startedAt := time.Now().Add(-time.Second)
	run := domain.GenerationRun{
		ID: "gen-test-run-001", OwnerID: "user-1", Status: domain.GenerationRunRunning,
		RequestedCount: 2, StartedAt: startedAt,
	}

	created, err := store.CreateGenerationRun(ctx, run)
	if err != nil || !created {
		t.Fatalf("首次创建运行记录失败: created=%v err=%v", created, err)
	}
	created, err = store.CreateGenerationRun(ctx, run)
	if err != nil || created {
		t.Fatalf("重复运行 ID 应幂等返回既有记录: created=%v err=%v", created, err)
	}

	if err := store.CompleteGenerationRun(ctx, run.ID, []string{"q-1", "q-2"}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetGenerationRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Status != domain.GenerationRunSucceeded || stored.CompletedAt == nil {
		t.Fatalf("完成状态不正确: %+v", stored)
	}
	if len(stored.QuestionIDs) != 2 || stored.QuestionIDs[0] != "q-1" || stored.QuestionIDs[1] != "q-2" {
		t.Fatalf("题目 ID 快照不正确: %v", stored.QuestionIDs)
	}
}

func TestMemoryGenerationRunFailure(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	run := domain.GenerationRun{
		ID: "gen-test-run-002", OwnerID: "user-1", Status: domain.GenerationRunRunning,
		RequestedCount: 1, StartedAt: time.Now(),
	}
	if _, err := store.CreateGenerationRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.FailGenerationRun(ctx, run.ID, "模型超时"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetGenerationRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.GenerationRunFailed || stored.Error != "模型超时" || stored.CompletedAt == nil {
		t.Fatalf("失败状态不正确: %+v", stored)
	}
}
