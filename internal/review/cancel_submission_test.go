package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestCancelSubmissionRestoresOnlySuspendedConfirmation(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "unfiled", true: "self_confirmed"}[active], func(t *testing.T) {
			svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1"}}, nil)
			q := testQuestion("bank-neike")
			q.OwnerID = "author"
			confirmedAt := time.Now().Add(-time.Hour)
			if active {
				q.LibraryState, q.ConfirmedVersion, q.ConfirmationSource, q.ConfirmedBy, q.ConfirmedAt = domain.LibraryActive, 1, domain.ConfirmSourceSelf, "author", &confirmedAt
			}
			if err := qs.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 1)
			if err := svc.CreateFlow(t.Context(), flow); err != nil {
				t.Fatal(err)
			}
			task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := qs.GetQuestion(t.Context(), q.ID)
			req := CancelSubmissionRequest{TaskID: task.ID, OwnerID: "author", ExpectedVersion: before.Version, ExpectedLibraryRevision: before.LibraryRevision, ExpectedReviewRevision: before.ReviewRevision}
			if err := svc.CancelSubmission(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			after, _ := qs.GetQuestion(t.Context(), q.ID)
			done, _ := tasks.GetTask(t.Context(), task.ID)
			if after.Status != domain.StatusAIReviewed || after.Version != 1 || after.LibraryRevision != before.LibraryRevision+1 || done.Status != domain.StatusArchived || done.FinalDecision != nil {
				t.Fatalf("cancel did not preserve history/version: question=%+v task=%+v", after, done)
			}
			if active {
				if after.LibraryState != domain.LibraryActive || after.ConfirmedBy != "author" || after.ConfirmedAt == nil || !after.ConfirmedAt.Equal(confirmedAt) || after.ConfirmationSource != domain.ConfirmSourceSelf || after.ConfirmedVersion != 1 {
					t.Fatalf("lost original approval: %+v", after)
				}
			} else if after.LibraryState != domain.LibraryUnfiled || after.ConfirmedVersion != 0 {
				t.Fatalf("fabricated approval: %+v", after)
			}
			if err := svc.CancelSubmission(t.Context(), req); !errors.Is(err, domain.ErrReviewConflict) {
				t.Fatalf("stale cancellation replay: %v", err)
			}
			resubmitted, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil || resubmitted.PreviousTaskID != task.ID || resubmitted.Attempt != 2 {
				t.Fatalf("cancel lost resubmit chain: %+v %v", resubmitted, err)
			}
			encoded, _ := json.Marshal(done)
			if strings.Contains(string(encoded), "library_before_submit") || strings.Contains(string(encoded), "confirmed_by") {
				t.Fatal("public task exposes private confirmation snapshot", string(encoded))
			}
		})
	}
}

func TestCancelSubmissionRejectsVotesWrongOwnerAndStaleAxes(t *testing.T) {
	for _, name := range []string{"wrong_owner", "content_version", "library_revision", "review_revision", "voted", "record_only", "other_active_task"} {
		t.Run(name, func(t *testing.T) {
			svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1", "r2"}}, nil)
			q := testQuestion("bank-neike")
			q.OwnerID = "author"
			if err := qs.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 1)
			flow.Rounds[0].ExpertIDs, flow.Rounds[0].RequiredCount = []string{"r1", "r2"}, 2
			if err := svc.CreateFlow(t.Context(), flow); err != nil {
				t.Fatal(err)
			}
			task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := qs.GetQuestion(t.Context(), q.ID)
			req := CancelSubmissionRequest{TaskID: task.ID, OwnerID: "author", ExpectedVersion: before.Version, ExpectedLibraryRevision: before.LibraryRevision, ExpectedReviewRevision: before.ReviewRevision}
			wantErr := domain.ErrReviewConflict
			switch name {
			case "wrong_owner":
				req.OwnerID = "other"
				wantErr = ErrReviewCancelNotOwner
			case "content_version":
				req.ExpectedVersion++
			case "library_revision":
				req.ExpectedLibraryRevision++
			case "review_revision":
				req.ExpectedReviewRevision++
			case "voted":
				if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
					t.Fatal(err)
				}
			case "record_only":
				if err := tasks.SaveRecord(t.Context(), domain.ReviewRecord{ID: "hidden-record", TaskID: task.ID}); err != nil {
					t.Fatal(err)
				}
			case "other_active_task":
				other := *task
				other.ID = "inconsistent-older-task"
				other.CreatedAt = task.CreatedAt.Add(-time.Second)
				if err := tasks.SaveTask(t.Context(), other); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.CancelSubmission(t.Context(), req); !errors.Is(err, wantErr) {
				t.Fatalf("wanted %v, got %v", wantErr, err)
			}
			after, _ := qs.GetQuestion(t.Context(), q.ID)
			kept, _ := tasks.GetTask(t.Context(), task.ID)
			if after.Status != before.Status || after.LibraryRevision != before.LibraryRevision || kept.Status != domain.StatusReviewing {
				t.Fatalf("rejected cancellation partially committed: %+v %+v", after, kept)
			}
		})
	}
}

func TestCancelSubmissionWriteFailureKeepsFrozenSnapshot(t *testing.T) {
	svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1"}}, nil)
	q := testQuestion("bank-neike")
	q.OwnerID = "author"
	q.LibraryState, q.ConfirmedVersion, q.ConfirmationSource, q.ConfirmedBy = domain.LibraryActive, 1, domain.ConfirmSourceSelf, "author"
	if err := qs.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 1)
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := qs.GetQuestion(t.Context(), q.ID)
	req := CancelSubmissionRequest{TaskID: task.ID, OwnerID: "author", ExpectedVersion: before.Version, ExpectedLibraryRevision: before.LibraryRevision, ExpectedReviewRevision: before.ReviewRevision}
	qs.FailNextSaveQuestion(errors.New("injected cancel write failure"))
	if err := svc.CancelSubmission(t.Context(), req); err == nil {
		t.Fatal("expected rollback")
	}
	kept, _ := tasks.GetTask(t.Context(), task.ID)
	if kept.Status != domain.StatusReviewing || kept.LibraryBeforeSubmit == nil || kept.LibraryBeforeSubmit.ConfirmedBy != "author" {
		t.Fatal("rollback dropped private snapshot", kept)
	}
	if err := svc.CancelSubmission(t.Context(), req); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestCancelHistoricalSubmissionDoesNotInventApprovalOrInitialCheck(t *testing.T) {
	for _, previous := range []domain.QuestionStatus{domain.StatusAIReviewed, domain.StatusAIDraft, domain.StatusAutoChecked} {
		t.Run(string(previous), func(t *testing.T) {
			svc, tasks, qs := newTestService(nil, nil)
			q := testQuestion("bank-neike")
			q.OwnerID, q.Status = "author", domain.StatusReviewing
			if err := qs.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			task := domain.ReviewTask{ID: "legacy-no-snapshot", QuestionID: q.ID, CurrentRound: 1, Status: domain.StatusReviewing, QuestionVersion: 1, QuestionPrevStatus: previous}
			if err := tasks.SaveTask(t.Context(), task); err != nil {
				t.Fatal(err)
			}
			before, _ := qs.GetQuestion(t.Context(), q.ID)
			if err := svc.CancelSubmission(t.Context(), CancelSubmissionRequest{TaskID: task.ID, OwnerID: "author", ExpectedVersion: before.Version, ExpectedLibraryRevision: before.LibraryRevision, ExpectedReviewRevision: before.ReviewRevision}); err != nil {
				t.Fatal(err)
			}
			after, _ := qs.GetQuestion(t.Context(), q.ID)
			if after.Status != previous || after.LibraryState != domain.LibraryUnfiled || after.ConfirmedVersion != 0 || after.Version != 1 {
				t.Fatalf("legacy cancellation promoted a question: %+v", after)
			}
		})
	}
}
