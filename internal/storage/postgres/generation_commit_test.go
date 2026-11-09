//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"fmt"
	"testing"
	"time"
)

func TestGenerationCommitRequiresRequestedCardinality(t *testing.T) {
	for _, returned := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(returned), func(t *testing.T) {
			st, ctx := newAICheckOutcomeTestStore(t)
			q := aiCheckTestQuestion(t, st, ctx, "contract-fixture")
			if err := st.DeleteQuestion(ctx, q.ID); err != nil {
				t.Fatal(err)
			}
			_, err := st.CreateGenerationRun(ctx, domain.GenerationRun{ID: "count-contract", OwnerID: "owner", Status: domain.GenerationRunPending, RequestedCount: 2, MaxAttempts: 3})
			if err != nil {
				t.Fatal(err)
			}
			run, _, err := st.ClaimNextGenerationRun(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			questions := []domain.A2Question{}
			for i := 0; i < returned; i++ {
				copy := q
				copy.ID = fmt.Sprintf("count-q%d", i)
				questions = append(questions, copy)
			}
			err = st.CommitGenerationRun(ctx, storage.GenerationRunCommit{RunID: run.ID, Attempt: run.Attempts, Questions: questions, CheckMaxAttempts: 3})
			if err == nil {
				t.Fatal("wrong count committed", returned)
			}
			var nq, nt int
			if err = st.db.QueryRow(`SELECT (SELECT count(*) FROM questions),(SELECT count(*) FROM ai_check_tasks)`).Scan(&nq, &nt); err != nil || nq != 0 || nt != 0 {
				t.Fatal(nq, nt, err)
			}
			after, _ := st.GetGenerationRun(ctx, run.ID)
			if after.Status != domain.GenerationRunRunning {
				t.Fatal("bad commit completed run", after)
			}
		})
	}
}

func TestGenerationCommitRollsBackEveryHandoffFailure(t *testing.T) {
	for _, tc := range []struct{ name, table, condition string }{{"second_question", "questions", "NEW.id='atomic-q2'"}, {"check_enqueue", "ai_check_tasks", "TRUE"}, {"completion", "generation_runs", "NEW.status='succeeded'"}} {
		t.Run(tc.name, func(t *testing.T) {
			st, ctx := newAICheckOutcomeTestStore(t)
			_, err := st.CreateGenerationRun(ctx, domain.GenerationRun{ID: "atomic-run", OwnerID: "owner", Status: domain.GenerationRunPending, RequestedCount: 2, MaxAttempts: 3})
			if err != nil {
				t.Fatal(err)
			}
			run, _, err := st.ClaimNextGenerationRun(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			a := aiCheckTestQuestion(t, st, ctx, "fixture-base")
			if err := st.DeleteQuestion(ctx, a.ID); err != nil {
				t.Fatal(err)
			}
			a.ID = "atomic-q1"
			b := a
			b.ID = "atomic-q2"
			trigger := fmt.Sprintf(`CREATE FUNCTION fail_handoff() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'injected handoff failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_handoff BEFORE INSERT OR UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION fail_handoff()`, tc.condition, tc.table)
			if _, err = st.db.ExecContext(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			commit := storage.GenerationRunCommit{RunID: run.ID, Attempt: run.Attempts, Questions: []domain.A2Question{a, b}, CheckMaxAttempts: 3}
			if err = st.CommitGenerationRun(ctx, commit); err == nil {
				t.Fatal("fault did not abort commit")
			}
			var nq, nt int
			_ = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM questions`).Scan(&nq)
			_ = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_check_tasks`).Scan(&nt)
			if nq != 0 || nt != 0 {
				t.Fatalf("partial writes survived: questions=%d checks=%d", nq, nt)
			}
			if _, err = st.db.ExecContext(ctx, `DROP TRIGGER fail_handoff ON `+tc.table+`; DROP FUNCTION fail_handoff()`); err != nil {
				t.Fatal(err)
			}
			if err = st.CommitGenerationRun(ctx, commit); err != nil {
				t.Fatal(err)
			}
			// An ambiguous acknowledgement may replay after the checker has advanced a draft.
			_, err = st.db.ExecContext(ctx, `UPDATE questions SET status='ai_reviewed' WHERE id='atomic-q1'`)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.CommitGenerationRun(ctx, commit); err != nil {
				t.Fatal(err)
			}
			q, _ := st.GetQuestion(ctx, "atomic-q1")
			if q.Status != domain.StatusAIReviewed {
				t.Fatal("replay reverted first-check outcome")
			}
			r, _ := st.GetGenerationRun(ctx, run.ID)
			if r.Status != domain.GenerationRunSucceeded || len(r.QuestionIDs) != 2 {
				t.Fatal(r)
			}
			_ = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_check_tasks`).Scan(&nt)
			if nt != 2 {
				t.Fatalf("duplicate checks: %d", nt)
			}
		})
	}
}

func TestGenerationCommitRejectsExistingQuestionID(t *testing.T) {
	st, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, st, ctx, "protected-existing")
	if _, err := st.db.ExecContext(ctx, `UPDATE questions SET status='ai_reviewed' WHERE id='protected-existing'`); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateGenerationRun(ctx, domain.GenerationRun{ID: "new-colliding-run", OwnerID: "another-owner", Status: domain.GenerationRunPending, RequestedCount: 1, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := st.ClaimNextGenerationRun(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	err = st.CommitGenerationRun(ctx, storage.GenerationRunCommit{RunID: run.ID, Attempt: run.Attempts, Questions: []domain.A2Question{q}, CheckMaxAttempts: 3})
	if err == nil {
		t.Fatal("existing question was accepted as a new draft")
	}
	stored, _ := st.GetQuestion(ctx, q.ID)
	if stored.Status != domain.StatusAIReviewed {
		t.Fatal("existing passed question was overwritten")
	}
	var n int
	_ = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_check_tasks WHERE question_id='protected-existing'`).Scan(&n)
	if n != 0 {
		t.Fatal("created a recheck for an existing question")
	}
}
