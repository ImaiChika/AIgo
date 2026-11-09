//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"errors"
	"testing"
	"time"
)

func TestLearningFrozenTimingPersistsAcrossLeaseRecovery(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "retry_capable"}[modern], func(t *testing.T) {
			s, initial := learningStoreFixture(t)
			want := 120 * time.Second
			if modern {
				if _, err := s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET body=body||'{"execution_budget_seconds":420,"attempt_timeout_seconds":180}'::jsonb WHERE id=$1`, initial.ID); err != nil {
					t.Fatal(err)
				}
				want = domain.LearningExecutionBudget
			}
			first, err := s.ClaimLearningJob(t.Context(), 0)
			if err != nil || first == nil || first.FirstStartedAt == nil || first.DeadlineAt == nil || first.LeasedUntil == nil {
				t.Fatal(first, err)
			}
			if first.DeadlineAt.Sub(*first.FirstStartedAt) != want || first.LeasedUntil.Sub(*first.FirstStartedAt) != min(want, first.AttemptTimeout()+domain.LearningLeaseBuffer) {
				t.Fatal("database and worker policy disagree", first)
			}
			if _, err := s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
				t.Fatal(err)
			}
			second, err := s.ClaimLearningJob(t.Context(), 0)
			if err != nil || second == nil || second.Attempts != 2 || !second.DeadlineAt.Equal(*first.DeadlineAt) || second.ExecutionBudgetSeconds != first.ExecutionBudgetSeconds || second.LeaseToken == first.LeaseToken {
				t.Fatal("recovery changed frozen policy", second, err)
			}
			if err := s.FinishLearningJob(t.Context(), first.ID, first.Attempts, first.LeaseToken, storage.LearningOutcome{Status: "succeeded"}); !errors.Is(err, storage.ErrLearningLeaseLost) {
				t.Fatal("old worker committed after recovery", err)
			}
		})
	}
}

func TestLearningExpiredDeadlineAcceptsOnlyCurrentTerminalTimeoutEvidence(t *testing.T) {
	s, _ := learningStoreFixture(t)
	job, err := s.ClaimLearningJob(t.Context(), 120*time.Second)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET deadline_at=NOW()-INTERVAL '1 second',leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []storage.LearningOutcome{{Status: "succeeded"}, {Status: "pending"}, {Status: "failed", ErrorKind: "model_error"}} {
		if err = s.FinishLearningJob(t.Context(), job.ID, job.Attempts, job.LeaseToken, outcome); !errors.Is(err, storage.ErrLearningLeaseLost) {
			t.Fatalf("expired non-timeout outcome %s accepted: %v", outcome.Status, err)
		}
	}
	if err = s.FinishLearningJob(t.Context(), job.ID, job.Attempts, job.LeaseToken, storage.LearningOutcome{Status: "failed", ErrorKind: "timeout", Error: "学习分析总预算已耗尽", ModelCallIDs: []string{"paid-timeout-call"}}); err != nil {
		t.Fatal("current worker lost terminal timeout evidence", err)
	}
	stored := readLearningJob(t, s, job.ID)
	if stored.Status != "failed" || stored.ErrorKind != "timeout" || len(stored.ModelCallIDs) != 1 || stored.ModelCallIDs[0] != "paid-timeout-call" || len(stored.AttemptHistory) != 1 || stored.AttemptHistory[0].ErrorKind != "timeout" {
		t.Fatal(stored)
	}
}

func TestLearningExpiredOldTokenCannotCommitTimeoutAfterReclaim(t *testing.T) {
	s, _ := learningStoreFixture(t)
	old, err := s.ClaimLearningJob(t.Context(), time.Minute)
	if err != nil || old == nil {
		t.Fatal(old, err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	current, err := s.ClaimLearningJob(t.Context(), time.Minute)
	if err != nil || current == nil || old.LeaseToken == current.LeaseToken {
		t.Fatal(current, err)
	}
	if err = s.FinishLearningJob(t.Context(), old.ID, old.Attempts, old.LeaseToken, storage.LearningOutcome{Status: "failed", ErrorKind: "timeout", ModelCallIDs: []string{"stale-paid-call"}}); !errors.Is(err, storage.ErrLearningLeaseLost) {
		t.Fatal("stale timeout escaped fencing", err)
	}
	stored := readLearningJob(t, s, old.ID)
	if stored.Status != "running" || stored.Attempts != 2 || len(stored.ModelCallIDs) != 0 || len(stored.AttemptHistory) != 0 {
		t.Fatal("old timeout rewrote current attempt", stored)
	}
}

func TestLearningSamplingPolicySurvivesLeaseRecovery(t *testing.T) {
	for _, policy := range []string{"", domain.LearningDiverseRetrySampling} {
		name := "legacy"
		if policy != "" {
			name = "diverse"
		}
		t.Run(name, func(t *testing.T) {
			s, initial := learningStoreFixture(t)
			if policy != "" {
				if _, e := s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET body=jsonb_set(jsonb_set(body,'{sampling_policy}',to_jsonb($2::text)),'{prompt_policy}',to_jsonb($3::text)) WHERE id=$1`, initial.ID, policy, domain.LearningScopedReflectionPrompt); e != nil {
					t.Fatal(e)
				}
			}
			first, e := s.ClaimLearningJob(t.Context(), time.Minute)
			if e != nil || first == nil || first.AnalysisTemperature() != 0.2 {
				t.Fatal(first, e)
			}
			if _, e = s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID); e != nil {
				t.Fatal(e)
			}
			second, e := s.ClaimLearningJob(t.Context(), time.Minute)
			want := 0.2
			if policy != "" {
				want = 0.6
			}
			if e != nil || second == nil || second.PromptPolicy != first.PromptPolicy || second.SamplingPolicy != policy || second.AnalysisTemperature() != want || !second.DeadlineAt.Equal(*first.DeadlineAt) {
				t.Fatal("lease recovery changed frozen sampler", second, e)
			}
		})
	}
}
