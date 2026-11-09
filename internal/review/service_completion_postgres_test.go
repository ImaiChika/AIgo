//go:build integration

package review

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestPostgresDirectCompletionCancelSnapshotAndConcurrentVotes(t *testing.T) {
	svc, store, _, _ := newRestoreServiceFixture(t)
	svc.RequireAICheck = true
	q := testQuestion("")
	q.ID, q.OwnerID, q.CreatedBy = "c4-personal-review", "owner", "owner"
	q.BankIDs = nil
	confirmedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	q.LibraryState, q.LibraryRevision, q.ConfirmedVersion, q.ConfirmationSource, q.ConfirmedBy, q.ConfirmedAt = domain.LibraryActive, 4, 1, domain.ConfirmSourceSelf, "owner", &confirmedAt
	if err := store.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "c4-original-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 1)
	flow.Rounds[0].ExpertIDs, flow.Rounds[0].RequiredCount = []string{"r1", "r2"}, 2
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedTask, err := store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.CompletionMode != domain.ReviewCompletionOnLastRound || storedTask.LibraryBeforeSubmit == nil || storedTask.LibraryBeforeSubmit.ConfirmedBy != "owner" {
		t.Fatal("task snapshot not persisted", storedTask)
	}
	// A late caller cannot change the frozen completion mode or confirmation.
	storedTask.CompletionMode, storedTask.LibraryBeforeSubmit = domain.ReviewCompletionExternalFinal, nil
	if err := store.UpdateTask(t.Context(), *storedTask); err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetQuestion(t.Context(), q.ID)
	req := CancelSubmissionRequest{TaskID: task.ID, OwnerID: "owner", ExpectedVersion: current.Version, ExpectedLibraryRevision: current.LibraryRevision, ExpectedReviewRevision: current.ReviewRevision}
	if err := svc.CancelSubmission(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	after, _ := store.GetQuestion(t.Context(), q.ID)
	if after.Version != 1 || after.LibraryState != domain.LibraryActive || after.ConfirmedBy != "owner" || after.ConfirmedAt == nil || !after.ConfirmedAt.Equal(confirmedAt) || after.LibraryRevision != 6 {
		t.Fatalf("cancel failed exact original confirmation: %+v", after)
	}
	reloaded, _ := store.GetTask(t.Context(), task.ID)
	if reloaded.Status != domain.StatusArchived || reloaded.CompletionMode != domain.ReviewCompletionOnLastRound || reloaded.LibraryBeforeSubmit == nil {
		t.Fatal("frozen snapshot changed", reloaded)
	}
	second, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.PreviousTaskID != task.ID || second.Attempt != 2 {
		t.Fatal("cancel history chain lost", second)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, reviewer := range []string{"r1", "r2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- svc.Review(context.Background(), ReviewRequest{TaskID: second.ID, ExpertID: id, Action: domain.StatusApproved})
		}(reviewer)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	done, _ := store.GetTask(t.Context(), second.ID)
	final, _ := store.GetQuestion(t.Context(), q.ID)
	records, _ := store.ListRecordsByTaskID(t.Context(), second.ID)
	if done.Status != domain.StatusPublished || done.FinalDecision != nil || final.Status != domain.StatusPublished || final.LibraryState != domain.LibraryActive || final.ConfirmationSource != domain.ConfirmSourceReview || final.ConfirmedVersion != 1 || final.Version != 1 || len(records) != 2 {
		t.Fatalf("concurrent direct completion inconsistent: task=%+v question=%+v records=%v", done, final, records)
	}
}

func TestPostgresDirectCompletionMissingInitialEvidenceRollsBackVote(t *testing.T) {
	svc, store, _, _ := newRestoreServiceFixture(t)
	svc.RequireAICheck = true
	q := testQuestion("")
	q.ID, q.OwnerID, q.BankIDs = "c4-no-evidence", "owner", nil
	if err := store.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 1)
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err == nil {
		t.Fatal("published without durable original initial check")
	}
	done, _ := store.GetTask(t.Context(), task.ID)
	records, _ := store.ListRecordsByTaskID(t.Context(), task.ID)
	if done.Status != domain.StatusReviewing || len(done.RoundResults[0].Reviews) != 0 || len(records) != 0 {
		t.Fatal("missing-evidence failure partially committed", done, records)
	}
	// Cancellation does not fabricate confirmation when no pre-submit approval existed.
	current, _ := store.GetQuestion(t.Context(), q.ID)
	if err := svc.CancelSubmission(t.Context(), CancelSubmissionRequest{TaskID: task.ID, OwnerID: "owner", ExpectedVersion: current.Version, ExpectedLibraryRevision: current.LibraryRevision, ExpectedReviewRevision: current.ReviewRevision}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err == nil || errors.Is(err, domain.ErrReviewTaskNotFound) {
		t.Fatal("canceled task accepted late vote or lost history", err)
	}
}

func TestPostgresOptionalReviewKeepsApprovedSharedVersionPublic(t *testing.T) {
	svc, store, _, _ := newRestoreServiceFixture(t)
	svc.RequireAICheck = true
	q := testQuestion("")
	q.ID, q.OwnerID, q.CreatedBy, q.BankIDs = "c4-shared-optional-review", "owner", "owner", nil
	q.ClinicalStem = "患者，男，50岁。C4共享公开病例。最可能的诊断是"
	q.LibraryState, q.ConfirmedVersion, q.ConfirmationSource, q.ConfirmedBy = domain.LibraryActive, 1, domain.ConfirmSourceSelf, "owner"
	if err := store.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "c4-shared-original-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 1)
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "c4-shared-review-approval", QuestionID: q.ID, OwnerID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if task, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, flow.ID); task != nil || !errors.Is(err, domain.ErrReviewNotSubmittable) {
		t.Fatalf("pending share accepted optional review: %+v %v", task, err)
	}
	if _, err := store.ReviewQuestionShare(t.Context(), "c4-shared-review-approval", "admin", domain.QuestionShareApproved, ""); err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetQuestion(t.Context(), q.ID)
	current.Version++
	current.ClinicalStem = "患者，男，50岁。C4私人新版病例。最可能的诊断是"
	if err := store.SaveQuestion(t.Context(), *current); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatalf("approved snapshot blocked private version review: %v", err)
	}
	assertPublic := func() {
		t.Helper()
		rows, total, err := store.SearchQuestions(t.Context(), storage.QuestionFilter{GlobalOnly: true, GlobalStatuses: []string{"approved"}, Keyword: "C4共享公开"}, 1, 10)
		if err != nil || total != 1 || len(rows) != 1 || rows[0].Version != 1 || rows[0].ClinicalStem != q.ClinicalStem {
			t.Fatalf("review changed or suppressed public snapshot: %+v total=%d err=%v", rows, total, err)
		}
		_, privateCount, err := store.SearchQuestions(t.Context(), storage.QuestionFilter{GlobalOnly: true, GlobalStatuses: []string{"approved"}, Keyword: "C4私人新版"}, 1, 10)
		if err != nil || privateCount != 0 {
			t.Fatal("private review revision leaked to public search", privateCount, err)
		}
	}
	assertPublic()
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatal(err)
	}
	after, _ := store.GetQuestion(t.Context(), q.ID)
	if after.Version != 2 || after.LibraryState != domain.LibraryActive || after.ConfirmedVersion != 2 || after.ConfirmationSource != domain.ConfirmSourceReview {
		t.Fatal("private reviewed version not confirmed", after)
	}
	assertPublic()
}
