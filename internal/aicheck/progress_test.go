package aicheck

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"aigo/internal/domain"
)

func TestProgressRetryDeadlineNeverExposesRunningLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		svc, questions, _, tasks := newTestService(&recordingClient{})
		if err := questions.SaveQuestion(ctx, testDraft("retry-progress", 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := tasks.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "retry-progress-task", QuestionID: "retry-progress", Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
			t.Fatal(err)
		}
		progress := func(wantDeadline bool, attempts int) {
			t.Helper()
			items, _, err := svc.ProgressByQuestionIDs(ctx, []string{"retry-progress"})
			if err != nil || len(items) != 1 {
				t.Fatalf("progress: %v %v", items, err)
			}
			raw, err := json.Marshal(items[0])
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			deadline, has := payload["next_attempt_at"]
			if has != wantDeadline {
				t.Fatalf("next_attempt_at present=%v, want %v: %s", has, wantDeadline, raw)
			}
			if has {
				parsed, err := time.Parse(time.RFC3339Nano, deadline.(string))
				if err != nil || !parsed.Equal(time.Now().Add(15*time.Second)) {
					t.Fatalf("retry deadline: %v %v", deadline, err)
				}
			}
			if items[0].Attempts != attempts || items[0].StemSummary != "" || items[0].Question != nil {
				t.Fatalf("incorrect attempts or staging content exposed: %+v", items[0])
			}
		}
		progress(false, 0)
		task, err := tasks.ClaimNextCheckTask(ctx, time.Minute)
		if err != nil || task == nil {
			t.Fatalf("claim: %v", err)
		}
		progress(false, 1)
		if err := tasks.FailCheckTask(ctx, task.ID, "context deadline exceeded", 15*time.Second); err != nil {
			t.Fatal(err)
		}
		progress(true, 1)
		time.Sleep(15 * time.Second)
		task, err = tasks.ClaimNextCheckTask(ctx, time.Minute)
		if err != nil || task == nil {
			t.Fatalf("retry claim: %v", err)
		}
		progress(false, 2)
		if err := tasks.CompleteCheckTask(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
		progress(false, 2)
	})
}
