package learning

import (
	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLearningDoesNotSendOutputOrThinkingTokenCaps(t *testing.T) {
	items := candidateFixture()
	text := strings.Repeat("evidence ", 55)
	for i := range items {
		items[i].Rules = nil
		for j := 0; j < 6; j++ {
			items[i].Rules = append(items[i].Rules, domain.LearningRule{
				ID: fmt.Sprintf("%s-%d", items[i].ID, j), Field: "clinical_stem",
				Text: text, Reason: text, Before: text, After: text,
			})
		}
	}
	raw, err := json.Marshal(map[string]any{"candidates": items})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["stream"] != true {
			t.Error("learning did not request background streaming")
		}
		for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens", "thinking_budget"} {
			if _, exists := body[field]; exists {
				t.Errorf("learning request must omit %s, got %v", field, body[field])
			}
		}
		if body["enable_thinking"] != true || body["temperature"] != 0.2 {
			t.Error("removing output cap changed thinking or temperature")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(raw)}}},
		})
	}))
	defer server.Close()
	enabled := true
	client := llm.NewQwenClient(llm.QwenConfig{APIKey: "test-only", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash", EnableThinking: &enabled})
	store, job := budgetJob()
	NewService(store, client).execute(t.Context(), job)
	if store.outcome.Status != "succeeded" || len(store.outcome.Candidates) != 3 {
		t.Fatal("complete bounded candidate response was lost", store.outcome)
	}
	for _, item := range store.outcome.Candidates {
		if len(item.Rules) != 6 || item.Rules[5].After != text {
			t.Fatal("candidate response was truncated")
		}
	}
}

type preflightTimeoutStore struct {
	storage.LearningStore
	outcome storage.LearningOutcome
}

func (s *preflightTimeoutStore) WithLearningAccess(context.Context, string, string, func(context.Context, []string) error) error {
	return context.DeadlineExceeded
}
func (s *preflightTimeoutStore) FinishLearningJob(_ context.Context, _ string, _ int, _ string, outcome storage.LearningOutcome) error {
	s.outcome = outcome
	return nil
}

type preflightCountClient struct{ calls int }

func (c *preflightCountClient) Complete(context.Context, []llm.Message, llm.GenerateOptions) (string, error) {
	c.calls++
	return "", nil
}

func TestPreflightDeadlineIsTimeoutWithoutCallingModel(t *testing.T) {
	first := &domain.A2Question{ID: "q", Version: 1, ClinicalStem: "initial", Status: domain.StatusAIReviewed}
	current := *first
	current.Version = 2
	current.ClinicalStem = "saved revision"
	_, originalHash, currentHash := CompareContent(*first, current)
	deadline := time.Now().Add(120 * time.Second)
	job := &domain.LearningJob{ID: "job", OwnerID: "owner", RoleID: "teacher", QuestionID: "q", CurrentVersion: 2, OriginalHash: originalHash, CurrentHash: currentHash, ProtocolVersion: domain.LearningProtocolVersion, Attempts: 1, MaxAttempts: 2, LeaseToken: "current-token", DeadlineAt: &deadline, Source: &domain.LearningSource{Original: first, Current: &current}}
	store := &preflightTimeoutStore{}
	client := &preflightCountClient{}
	NewService(store, client).execute(t.Context(), job)
	if store.outcome.Status != "failed" || store.outcome.ErrorKind != "timeout" || client.calls != 0 {
		t.Fatal(store.outcome, client.calls)
	}
}

type changingLearningPolicy struct {
	storage.LearningStore
	limit       atomic.Int64
	unavailable atomic.Bool
}

func (s *changingLearningPolicy) LearningConcurrencyLimit(context.Context) (int, error) {
	if s.unavailable.Load() {
		return 0, context.DeadlineExceeded
	}
	return int(s.limit.Load()), nil
}
func (s *changingLearningPolicy) ClaimLearningJob(context.Context, time.Duration) (*domain.LearningJob, error) {
	return nil, nil
}
func TestWorkerRefreshesLearningLimitWhileAllLocalSlotsAreBusy(t *testing.T) {
	store := &changingLearningPolicy{}
	store.limit.Store(1)
	service := NewService(store, &preflightCountClient{})
	service.PollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service.StartWorkers(ctx, 1)
	release, err := service.Limiter.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waitLimit := func(n int) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if service.Limiter.Snapshot().Limit == n {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("policy did not update", service.Limiter.Snapshot())
	}
	// Raising must not depend on a completed model request freeing the first slot.
	store.limit.Store(2)
	waitLimit(2)
	store.unavailable.Store(true)
	waitLimit(1)
	if service.Limiter.Snapshot().Active != 1 {
		t.Fatal("lowering cancelled active work")
	}
	cancel()
	select {
	case <-service.WorkersDone():
	case <-time.After(time.Second):
		t.Fatal("policy watcher or dispatcher leaked")
	}
}
