//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestGenerationStaleAttemptCannotRewriteClaimedSuccessor(t *testing.T) {
	for _, action := range []string{"retry", "fail", "complete", "question_ids"} {
		t.Run(action, func(t *testing.T) {
			s, ctx := newAICheckOutcomeTestStore(t)
			if _, err := s.CreateGenerationRun(ctx, domain.GenerationRun{ID: "fenced-generation", OwnerID: "owner", Status: domain.GenerationRunPending, RequestedCount: 1, MaxAttempts: 3}); err != nil {
				t.Fatal(err)
			}
			first, _, err := s.ClaimNextGenerationRun(ctx, time.Minute)
			if err != nil || first == nil {
				t.Fatal(first, err)
			}
			if _, err = s.db.ExecContext(ctx, `UPDATE generation_runs SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
				t.Fatal(err)
			}
			next, _, err := s.ClaimNextGenerationRun(ctx, time.Minute)
			if err != nil || next == nil || next.Attempts != 2 {
				t.Fatal(next, err)
			}
			oldCtx := context.WithoutCancel(storage.WithQueueAttempt(ctx, storage.QueueGeneration, first.ID, first.Attempts))
			switch action {
			case "retry":
				_, err = s.RetryGenerationRun(oldCtx, first.ID, "late retry", time.Millisecond)
			case "fail":
				err = s.FailGenerationRun(oldCtx, first.ID, "late terminal failure")
			case "complete":
				err = s.CompleteGenerationRun(oldCtx, first.ID, []string{"late-question"})
			case "question_ids":
				err = s.RecordGenerationRunQuestionIDs(oldCtx, first.ID, []string{"late-question"})
			}
			if !errors.Is(err, storage.ErrGenerationRunLeaseLost) {
				t.Errorf("stale %s must lose fence, got %v", action, err)
			}
			current, err := s.GetGenerationRun(ctx, first.ID)
			if err != nil || current.Status != domain.GenerationRunRunning || current.Attempts != 2 || current.Error != "" || len(current.QuestionIDs) != 0 {
				t.Errorf("late %s changed successor: %+v error=%v", action, current, err)
			}
			nextCtx := storage.WithQueueAttempt(ctx, storage.QueueGeneration, next.ID, next.Attempts)
			if err = s.CompleteGenerationRun(nextCtx, next.ID, []string{"current-question"}); err != nil {
				t.Fatal("current worker must commit", err)
			}
			if err = s.FailGenerationRun(oldCtx, first.ID, "overwrite committed successor"); !errors.Is(err, storage.ErrGenerationRunLeaseLost) {
				t.Errorf("late terminal write should be fenced after success: %v", err)
			}
			current, err = s.GetGenerationRun(ctx, first.ID)
			if err != nil || current.Status != domain.GenerationRunSucceeded || len(current.QuestionIDs) != 1 || current.QuestionIDs[0] != "current-question" {
				t.Errorf("successor success overwritten: %+v %v", current, err)
			}
		})
	}
}

func TestAICheckStaleAttemptCannotRewriteClaimedSuccessor(t *testing.T) {
	for _, action := range []string{"fail", "complete", "pass", "reject"} {
		t.Run(action, func(t *testing.T) {
			s, ctx := newAICheckOutcomeTestStore(t)
			q := aiCheckTestQuestion(t, s, ctx, "fenced-check-question")
			if _, err := s.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "fenced-check", QuestionID: q.ID, QuestionVersion: q.Version, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
				t.Fatal(err)
			}
			first, err := s.ClaimNextCheckTask(ctx, time.Minute)
			if err != nil || first == nil {
				t.Fatal(first, err)
			}
			if _, err = s.db.ExecContext(ctx, `UPDATE ai_check_tasks SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
				t.Fatal(err)
			}
			next, err := s.ClaimNextCheckTask(ctx, time.Minute)
			if err != nil || next == nil || next.Attempts != 2 {
				t.Fatal(next, err)
			}
			oldCtx := context.WithoutCancel(storage.WithQueueAttempt(ctx, storage.QueueAICheck, first.ID, first.Attempts))
			switch action {
			case "fail":
				err = s.FailCheckTask(oldCtx, first.ID, "late timeout", time.Millisecond)
			case "complete":
				err = s.CompleteCheckTask(oldCtx, first.ID)
			case "pass":
				err = s.ApplyAICheckOutcome(oldCtx, passOutcome(q, first.ID))
			case "reject":
				err = s.ApplyAICheckOutcome(oldCtx, failOutcome(q, first.ID))
			}
			if !errors.Is(err, storage.ErrAICheckTaskLeaseLost) {
				t.Errorf("stale %s must lose fence, got %v", action, err)
			}
			tasks, err := s.LatestCheckTasksByQuestionIDs(ctx, []string{q.ID})
			current := tasks[q.ID]
			if err != nil || current.Status != domain.AICheckTaskRunning || current.Attempts != 2 || current.LastError != "" {
				t.Errorf("late %s changed successor: %+v %v", action, current, err)
			}
			stored, err := s.GetQuestion(ctx, q.ID)
			if err != nil || stored == nil || stored.Status != domain.StatusAIDraft {
				t.Errorf("late outcome changed staging question: %+v %v", stored, err)
			}
			for _, table := range []string{"ai_review_results", "ai_check_discards"} {
				var n int
				if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
					t.Errorf("stale write left %s rows=%d %v", table, n, err)
				}
			}
			nextCtx := storage.WithQueueAttempt(ctx, storage.QueueAICheck, next.ID, next.Attempts)
			if err = s.ApplyAICheckOutcome(nextCtx, passOutcome(q, next.ID)); err != nil {
				t.Fatal("current worker must atomically pass", err)
			}
			if err = s.FailCheckTask(oldCtx, first.ID, "late failure after success", time.Millisecond); !errors.Is(err, storage.ErrAICheckTaskLeaseLost) {
				t.Errorf("late failure should be fenced after success: %v", err)
			}
			stored, err = s.GetQuestion(ctx, q.ID)
			if err != nil || stored == nil || stored.Status != domain.StatusAIReviewed {
				t.Errorf("current successful question lost: %+v %v", stored, err)
			}
		})
	}
}

func TestAICheckFencedFinalAttemptRejectAndExhaustRemainAtomic(t *testing.T) {
	for _, mode := range []string{"quality_reject", "execution_exhausted"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx := newAICheckOutcomeTestStore(t)
			q := aiCheckTestQuestion(t, s, ctx, "fenced-final-question")
			if _, err := s.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "fenced-final-check", QuestionID: q.ID, QuestionVersion: q.Version, Status: domain.AICheckTaskPending, MaxAttempts: 2}); err != nil {
				t.Fatal(err)
			}
			first, err := s.ClaimNextCheckTask(ctx, time.Minute)
			if err != nil || first == nil {
				t.Fatal(first, err)
			}
			if _, err = s.db.ExecContext(ctx, `UPDATE ai_check_tasks SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
				t.Fatal(err)
			}
			final, err := s.ClaimNextCheckTask(ctx, time.Minute)
			if err != nil || final == nil || final.Attempts != 2 {
				t.Fatal(final, err)
			}
			if err = s.FailCheckTask(storage.WithQueueAttempt(ctx, storage.QueueAICheck, first.ID, first.Attempts), first.ID, "old failure must not exhaust successor", time.Millisecond); !errors.Is(err, storage.ErrAICheckTaskLeaseLost) {
				t.Fatal("stale failure was not fenced", err)
			}
			kept, err := s.GetQuestion(ctx, q.ID)
			if err != nil || kept == nil {
				t.Fatal("old failure deleted current final attempt", kept, err)
			}
			currentCtx := storage.WithQueueAttempt(ctx, storage.QueueAICheck, final.ID, final.Attempts)
			expectedVerdict := "reject"
			if mode == "quality_reject" {
				err = s.ApplyAICheckOutcome(currentCtx, failOutcome(q, final.ID))
			} else {
				expectedVerdict = "check_failed"
				err = s.FailCheckTask(currentCtx, final.ID, "current final failure", time.Millisecond)
			}
			if err != nil {
				t.Fatal("current final outcome failed", err)
			}
			gone, err := s.GetQuestion(ctx, q.ID)
			if err != nil || gone != nil {
				t.Fatal("current final outcome did not discard staging question", gone, err)
			}
			var n int
			if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_check_discards WHERE question_id=$1 AND verdict=$2`, q.ID, expectedVerdict).Scan(&n); err != nil || n != 1 {
				t.Fatal("final discard missing or duplicated", n, err)
			}
			tasks, err := s.LatestCheckTasksByQuestionIDs(ctx, []string{q.ID})
			if err != nil || len(tasks) != 0 {
				t.Fatal("discard cascade left pending task", tasks, err)
			}
		})
	}
}

func TestGenerationOldAttemptCannotAcknowledgeSuccessorCommit(t *testing.T) {
	s, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, s, ctx, "handoff-template")
	if err := s.DeleteQuestion(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGenerationRun(ctx, domain.GenerationRun{ID: "fenced-handoff", OwnerID: "owner", Status: domain.GenerationRunPending, RequestedCount: 1, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	first, _, err := s.ClaimNextGenerationRun(ctx, time.Minute)
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE generation_runs SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.ClaimNextGenerationRun(ctx, time.Minute)
	if err != nil || next == nil {
		t.Fatal(next, err)
	}
	q.ID = "current-handoff-question"
	commit := storage.GenerationRunCommit{RunID: next.ID, Attempt: next.Attempts, Questions: []domain.A2Question{q}, CheckMaxAttempts: 3}
	if err = s.CommitGenerationRun(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitGenerationRun(ctx, commit); err != nil {
		t.Fatal("same attempt must replay ambiguous acknowledgement", err)
	}
	old := commit
	old.Attempt = first.Attempts
	q.ID = "old-uncommitted-question"
	old.Questions = []domain.A2Question{q}
	if err = s.CommitGenerationRun(ctx, old); !errors.Is(err, storage.ErrGenerationRunLeaseLost) {
		t.Fatalf("old attempt was falsely acknowledged as committed: %v", err)
	}
	run, err := s.GetGenerationRun(ctx, first.ID)
	if err != nil || run.Status != domain.GenerationRunSucceeded || len(run.QuestionIDs) != 1 || run.QuestionIDs[0] != "current-handoff-question" {
		t.Fatal(run, err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM questions`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
