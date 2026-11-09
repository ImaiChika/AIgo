package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

type generationFenceWrite struct {
	operation string
	attempt   int
	ctxErr    error
}
type generationWriteFenceStore struct {
	*testutil.MemoryStore
	writes []generationFenceWrite
}

func (s *generationWriteFenceStore) capture(ctx context.Context, id, op string) {
	s.writes = append(s.writes, generationFenceWrite{op, storage.QueueAttemptFromContext(ctx, storage.QueueGeneration, id), ctx.Err()})
}
func (s *generationWriteFenceStore) RetryGenerationRun(ctx context.Context, id, message string, delay time.Duration) (bool, error) {
	s.capture(ctx, id, "retry")
	return s.MemoryStore.RetryGenerationRun(ctx, id, message, delay)
}
func (s *generationWriteFenceStore) FailGenerationRun(ctx context.Context, id, message string) error {
	s.capture(ctx, id, "fail")
	return s.MemoryStore.FailGenerationRun(ctx, id, message)
}
func (s *generationWriteFenceStore) CompleteGenerationRun(ctx context.Context, id string, ids []string) error {
	s.capture(ctx, id, "complete")
	return s.MemoryStore.CompleteGenerationRun(ctx, id, ids)
}
func (s *generationWriteFenceStore) RecordGenerationRunQuestionIDs(ctx context.Context, id string, ids []string) error {
	s.capture(ctx, id, "question_ids")
	return s.MemoryStore.RecordGenerationRunQuestionIDs(ctx, id, ids)
}

type interruptedGenerationClient struct{ cancel context.CancelFunc }

func (c interruptedGenerationClient) Complete(ctx context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.cancel()
	return "", ctx.Err()
}

func TestGenerationWorkerKeepsAttemptFenceForSuccessFailureAndCanceledCleanup(t *testing.T) {
	for _, name := range []string{"success", "retry", "invalid_snapshot", "interrupted"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var client llm.Client = &fakeLLM{}
			if name == "retry" {
				client = &alwaysFailLLM{}
			}
			if name == "interrupted" {
				client = interruptedGenerationClient{cancel: cancel}
			}
			store := &generationWriteFenceStore{MemoryStore: testutil.NewMemoryStore()}
			p := New(generator.NewService(client), nil, nil, nil, store)
			submitTestRun(t, p, "fenced-worker-"+name)
			run, raw, err := store.ClaimNextGenerationRun(ctx, time.Minute)
			if err != nil || run == nil {
				t.Fatal(run, err)
			}
			var spec GenerationSpec
			if err = json.Unmarshal(raw, &spec); err != nil {
				t.Fatal(err)
			}
			if name == "invalid_snapshot" {
				spec.Request.PersonalExperiences = &domain.PersonalExperienceSnapshot{Version: "invalid"}
			}
			p.executeGenerationRun(ctx, run, &spec)
			if len(store.writes) == 0 {
				t.Fatal("worker never wrote outcome")
			}
			for _, write := range store.writes {
				if write.attempt != run.Attempts || write.ctxErr != nil {
					t.Fatalf("%s write lost safe claimed identity: %+v", name, write)
				}
			}
		})
	}
}
