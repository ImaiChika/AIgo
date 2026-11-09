package aicheck

import (
	"context"
	"testing"
	"time"

	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

type checkWriteFenceStore struct {
	*testutil.MemoryAICheckTaskStore
	observed []int
}

func (s *checkWriteFenceStore) CompleteCheckTask(ctx context.Context, id string) error {
	s.observed = append(s.observed, storage.QueueAttemptFromContext(ctx, storage.QueueAICheck, id))
	return s.MemoryAICheckTaskStore.CompleteCheckTask(ctx, id)
}
func (s *checkWriteFenceStore) FailCheckTask(ctx context.Context, id, message string, backoff time.Duration) error {
	s.observed = append(s.observed, storage.QueueAttemptFromContext(ctx, storage.QueueAICheck, id))
	return s.MemoryAICheckTaskStore.FailCheckTask(ctx, id, message, backoff)
}
func TestCheckWorkerBindsClaimedAttemptToEveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client llm.Client
	}{{"pass", &recordingClient{}}, {"reject", &failingCheckClient{}}, {"failure", &errorClient{}}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, questions, _, tasks := newTestService(tc.client)
			observed := &checkWriteFenceStore{MemoryAICheckTaskStore: tasks}
			svc.taskStore = observed
			q := testDraft("fence-worker-"+tc.name, 1)
			if err := questions.SaveQuestion(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			svc.CheckAsync(q.ID)
			task, err := tasks.ClaimNextCheckTask(t.Context(), time.Minute)
			if err != nil || task == nil {
				t.Fatal(task, err)
			}
			svc.executeTask(t.Context(), task)
			if len(observed.observed) != 1 || observed.observed[0] != task.Attempts {
				t.Fatalf("worker outcome missing current attempt fence: %v expected=%d", observed.observed, task.Attempts)
			}
		})
	}
}
