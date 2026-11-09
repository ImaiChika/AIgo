package learning

import (
	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

type budgetTestStore struct {
	storage.LearningStore
	source  *domain.LearningSource
	outcome storage.LearningOutcome
}

func (s *budgetTestStore) WithLearningAccess(ctx context.Context, _, _ string, fn func(context.Context, []string) error) error {
	return fn(ctx, []string{domain.PermQuestionView, domain.PermLearningAnalyze})
}
func (s *budgetTestStore) LoadLearningSource(context.Context, string, string) (*domain.LearningSource, error) {
	return s.source, nil
}
func (s *budgetTestStore) FinishLearningJob(_ context.Context, _ string, _ int, _ string, o storage.LearningOutcome) error {
	s.outcome = o
	return nil
}

type timedBudgetClient struct {
	delays         []time.Duration
	calls          int
	malformedFirst bool
	temperatures   []float64
}

func (c *timedBudgetClient) Complete(ctx context.Context, _ []llm.Message, options llm.GenerateOptions) (string, error) {
	c.temperatures = append(c.temperatures, options.Temperature)
	i := c.calls
	c.calls++
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(c.delays[i]):
	}
	if c.malformedFirst && i == 0 {
		return `{"candidates":[]}`, nil
	}
	raw, _ := json.Marshal(map[string]any{"candidates": candidateFixture()})
	return string(raw), nil
}
func budgetJob() (*budgetTestStore, *domain.LearningJob) {
	original := &domain.A2Question{ID: "budget-q", Version: 1, ClinicalStem: "original", Status: domain.StatusAIReviewed}
	current := *original
	current.Version = 2
	current.ClinicalStem = "revised"
	_, a, b := CompareContent(*original, current)
	source := &domain.LearningSource{Original: original, Current: &current, InitialCheckPassed: true}
	job := &domain.LearningJob{ID: "budget-job", OwnerID: "owner", RoleID: "teacher", QuestionID: original.ID, CurrentVersion: 2, OriginalHash: a, CurrentHash: b, ProtocolVersion: domain.LearningProtocolVersion, Attempts: 1, MaxAttempts: 2, Source: source, ExecutionBudgetSeconds: 420, AttemptTimeoutSeconds: 180}
	return &budgetTestStore{source: source}, job
}
func TestLearningBudgetAllowsValidReplyAfterOldCutoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		c := &timedBudgetClient{delays: []time.Duration{135 * time.Second}}
		NewService(s, c).execute(t.Context(), j)
		if s.outcome.Status != "succeeded" || len(s.outcome.Candidates) != 3 || c.calls != 1 {
			t.Fatalf("135-second valid reply was cancelled: %+v calls=%d", s.outcome, c.calls)
		}
	})
}
func TestLearningAttemptTimeoutLeavesRoomForSecondCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		deadline := time.Now().Add(420 * time.Second)
		j.DeadlineAt = &deadline
		c := &timedBudgetClient{delays: []time.Duration{10 * time.Minute, 65 * time.Second}}
		svc := NewService(s, c)
		start := time.Now()
		svc.execute(t.Context(), j)
		if s.outcome.Status != "pending" || s.outcome.ErrorKind != "attempt_timeout" || time.Since(start) != 180*time.Second {
			t.Fatalf("first timeout consumed retry budget: %+v elapsed=%s", s.outcome, time.Since(start))
		}
		time.Sleep(time.Second)
		j.Attempts = 2
		svc.execute(t.Context(), j)
		if s.outcome.Status != "succeeded" || c.calls != 2 {
			t.Fatalf("second valid response lost: %+v calls=%d", s.outcome, c.calls)
		}
	})
}
func TestLearningLegacyDeadlineStillExpiresAt120Seconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		j.ExecutionBudgetSeconds = 0
		j.AttemptTimeoutSeconds = 0
		deadline := time.Now().Add(120 * time.Second)
		j.DeadlineAt = &deadline
		c := &timedBudgetClient{delays: []time.Duration{135 * time.Second}}
		start := time.Now()
		NewService(s, c).execute(t.Context(), j)
		if s.outcome.Status != "failed" || s.outcome.ErrorKind != "timeout" || time.Since(start) != 120*time.Second {
			t.Fatalf("legacy deadline changed: %+v elapsed=%s", s.outcome, time.Since(start))
		}
	})
}

func TestLearningSlowMalformedReplyStillAllowsValidRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		deadline := time.Now().Add(420 * time.Second)
		j.DeadlineAt = &deadline
		c := &timedBudgetClient{delays: []time.Duration{110 * time.Second, 70 * time.Second}, malformedFirst: true}
		svc := NewService(s, c)
		svc.execute(t.Context(), j)
		if s.outcome.Status != "pending" || s.outcome.ErrorKind != "protocol_error" {
			t.Fatal(s.outcome)
		}
		time.Sleep(time.Second)
		j.Attempts = 2
		svc.execute(t.Context(), j)
		if s.outcome.Status != "succeeded" || c.calls != 2 {
			t.Fatal("slow first reply starved second attempt", s.outcome, c.calls)
		}
	})
}

func TestLearningTwoHungAttemptsStopWithoutThirdCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		deadline := time.Now().Add(420 * time.Second)
		j.DeadlineAt = &deadline
		c := &timedBudgetClient{delays: []time.Duration{10 * time.Minute, 10 * time.Minute}}
		svc := NewService(s, c)
		start := time.Now()
		svc.execute(t.Context(), j)
		if s.outcome.Status != "pending" {
			t.Fatal(s.outcome)
		}
		time.Sleep(time.Second)
		j.Attempts = 2
		svc.execute(t.Context(), j)
		if s.outcome.Status != "failed" || s.outcome.ErrorKind != "attempt_timeout" || c.calls != 2 || time.Since(start) != 361*time.Second {
			t.Fatal("hung model was not bounded", s.outcome, c.calls, time.Since(start))
		}
	})
}

func TestLearningRetryHonorsRemainingFrozenDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		j.Attempts = 2
		deadline := time.Now().Add(40 * time.Second)
		j.DeadlineAt = &deadline
		c := &timedBudgetClient{delays: []time.Duration{70 * time.Second}}
		start := time.Now()
		NewService(s, c).execute(t.Context(), j)
		if s.outcome.Status != "failed" || s.outcome.ErrorKind != "timeout" || time.Since(start) != 40*time.Second {
			t.Fatal("retry reset frozen deadline", s.outcome, time.Since(start))
		}
	})
}

type transientLearningCommitStore struct {
	*budgetTestStore
	writes int
}

func (s *transientLearningCommitStore) FinishLearningJob(ctx context.Context, id string, attempt int, token string, outcome storage.LearningOutcome) error {
	s.writes++
	if s.writes < 3 {
		return fmt.Errorf("controlled temporary commit failure")
	}
	return s.budgetTestStore.FinishLearningJob(ctx, id, attempt, token, outcome)
}
func TestLearningSuccessfulResponseRetriesCommitWithoutRepeatingModel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base, job := budgetJob()
		store := &transientLearningCommitStore{budgetTestStore: base}
		client := &timedBudgetClient{delays: []time.Duration{time.Second}}
		NewService(store, client).execute(t.Context(), job)
		if client.calls != 1 || store.writes != 3 || store.outcome.Status != "succeeded" || len(store.outcome.Candidates) != 3 {
			t.Fatal("transient result write repeated paid analysis", client.calls, store.writes, store.outcome)
		}
	})
}

func TestLearningSamplingPolicyChangesOnlyNewTaskRetry(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		timeout      bool
		wantRetry    float64
	}{
		{"legacy_protocol", "", false, 0.2}, {"legacy_timeout", "", true, 0.2},
		{"new_protocol", domain.LearningDiverseRetrySampling, false, 0.2}, {"new_timeout", domain.LearningDiverseRetrySampling, true, 0.6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, j := budgetJob()
				j.SamplingPolicy = tc.policy
				deadline := time.Now().Add(420 * time.Second)
				j.DeadlineAt = &deadline
				delay := time.Second
				if tc.timeout {
					delay = 10 * time.Minute
				}
				c := &timedBudgetClient{delays: []time.Duration{delay, time.Second}, malformedFirst: !tc.timeout}
				svc := NewService(s, c)
				svc.execute(t.Context(), j)
				if s.outcome.Status != "pending" {
					t.Fatal(s.outcome)
				}
				time.Sleep(time.Second)
				j.Attempts = 2
				j.ErrorKind = s.outcome.ErrorKind
				svc.execute(t.Context(), j)
				if s.outcome.Status != "succeeded" || c.calls != 2 || len(c.temperatures) != 2 || c.temperatures[0] != 0.2 || c.temperatures[1] != tc.wantRetry {
					t.Fatal("sampling policy was not frozen across retry", s.outcome, c.temperatures)
				}
			})
		})
	}
}

type rateLimitedClient struct{ delay time.Duration }

func (c rateLimitedClient) Complete(context.Context, []llm.Message, llm.GenerateOptions) (string, error) {
	return "", &llm.HTTPStatusError{Status: 429, RetryAfter: c.delay}
}
func TestLearning429RespectsRetryAfterWithinSharedBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		now := time.Now()
		NewService(s, rateLimitedClient{2 * time.Second}).execute(t.Context(), j)
		if s.outcome.Status != "pending" || s.outcome.RetryAt == nil || s.outcome.RetryAt.Sub(now) != 2*time.Second {
			t.Fatal("Retry-After lost", s.outcome)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		s, j := budgetJob()
		deadline := time.Now().Add(10 * time.Second)
		j.DeadlineAt = &deadline
		NewService(s, rateLimitedClient{20 * time.Second}).execute(t.Context(), j)
		if s.outcome.Status != "failed" || s.outcome.RetryAt != nil {
			t.Fatal("Retry-After extended frozen budget", s.outcome)
		}
	})
}
