package batch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

func TestRetryAfterAmbiguousTerminalCheckpointDoesNotLoseReactivation(t *testing.T) {
	point := domain.KnowledgePoint{ID: "ambiguous-retry", Subject: "内科", Topic: "恢复失败项", OutlineCode: "ambiguous-retry"}
	points, _ := json.Marshal([]domain.KnowledgePoint{point})
	old := localBatchOutput{GenerationStartedAt: time.Now().Add(-time.Second).UnixMilli(), Items: []ImportItem{{Status: "failed", Attempts: 3, Error: "provider unavailable"}}}
	raw, _ := json.Marshal(old)
	st := &localBatchStore{job: storage.BatchJobRecord{ID: "ambiguous-retry", Backend: localSingleAPIBackend, Status: "completed", TotalCount: 1, Failed: 1, PointsJSON: string(points), OutputJSON: string(raw)}}
	client := &robustLLM{}
	ctx, cancel := context.WithCancel(t.Context())
	ex := &LocalExecutor{generator: generator.NewService(client), questionStore: testutil.NewMemoryStore(), batchJobStore: st, ctx: ctx, cancel: cancel, limiter: storage.NewConcurrencyLimiter(1), MaxAttempts: 3, active: map[string]bool{}, recovery: map[string]RecoveryProgress{}, checkpoints: map[string]batchCheckpoint{}}
	defer ex.Close()
	// The terminal commit reached durable storage, but its reply was lost. The
	// executor retained the old terminal snapshot for a later checkpoint retry.
	ex.rememberCheckpoint("ambiguous-retry", "completed", old)
	if _, err := ex.RetryFailed(t.Context(), "ambiguous-retry"); err != nil {
		t.Fatal(err)
	}
	ex.restoreCheckpoints()
	job := waitForJobs(t, ex, []string{"ambiguous-retry"}, time.Second)[0]
	if job.Completed != 1 || job.Failed != 0 || client.calls() != 1 {
		t.Fatalf("retry swallowed by old checkpoint: %+v calls=%d", job, client.calls())
	}
}

type blockedCheckpointReadStore struct {
	localBatchStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockedCheckpointReadStore) GetBatchJob(ctx context.Context, id string) (*storage.BatchJobRecord, error) {
	if id == "slow-checkpoint" {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.localBatchStore.GetBatchJob(ctx, id)
}
func TestSlowCheckpointDoesNotBlockRetryOfDifferentJob(t *testing.T) {
	point := domain.KnowledgePoint{ID: "p", Subject: "内科", Topic: "恢复", OutlineCode: "p"}
	points, _ := json.Marshal([]domain.KnowledgePoint{point})
	old := localBatchOutput{Items: []ImportItem{{Status: "failed", Attempts: 3}}}
	raw, _ := json.Marshal(old)
	st := &blockedCheckpointReadStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
	for _, id := range []string{"slow-checkpoint", "other-retry"} {
		if err := st.SaveBatchJob(t.Context(), storage.BatchJobRecord{ID: id, Backend: localSingleAPIBackend, Status: "completed", TotalCount: 1, Failed: 1, PointsJSON: string(points), OutputJSON: string(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	ex := &LocalExecutor{generator: generator.NewService(&robustLLM{}), questionStore: testutil.NewMemoryStore(), batchJobStore: st, ctx: ctx, cancel: cancel, limiter: storage.NewConcurrencyLimiter(1), MaxAttempts: 3, active: map[string]bool{}, recovery: map[string]RecoveryProgress{}, checkpoints: map[string]batchCheckpoint{}}
	defer ex.Close()
	defer close(st.release)
	ex.rememberCheckpoint("slow-checkpoint", "completed", old)
	done := make(chan struct{})
	go func() { ex.restoreCheckpoints(); close(done) }()
	select {
	case <-st.entered:
	case <-time.After(time.Second):
		t.Fatal("checkpoint read did not start")
	}
	retry := make(chan error, 1)
	go func() { _, err := ex.RetryFailed(t.Context(), "other-retry"); retry <- err }()
	select {
	case err := <-retry:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated retry blocked behind a slow checkpoint")
	}
}
