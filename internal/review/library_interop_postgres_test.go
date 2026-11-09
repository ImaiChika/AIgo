//go:build integration

package review

import (
	"errors"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestPostgresDeletedOptionalReviewGuards(t *testing.T) {
	svc, store, _, prior := newRestoreServiceFixture(t)
	svc.RequireAICheck = true
	for _, status := range []domain.QuestionStatus{domain.StatusAIReviewed, domain.StatusPublished} {
		t.Run(string(status), func(t *testing.T) {
			q := testQuestion("")
			q.ID = "deleted-optional-" + string(status)
			q.OwnerID = "owner"
			q.CreatedBy = "owner"
			q.BankIDs = nil
			q.Status = status
			q.LibraryState = domain.LibraryDeleted
			q.LibraryRevision = 4
			q.DeletedReason = domain.DeleteReasonOwnerDelete
			if err := store.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "initial-" + q.ID, QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if status == domain.StatusAIReviewed {
				task, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, prior.FlowID)
				if task != nil || !errors.Is(err, domain.ErrReviewNotSubmittable) {
					t.Fatalf("deleted submission: task=%+v err=%v", task, err)
				}
				if task, _ := store.GetTaskByQuestionID(t.Context(), q.ID); task != nil {
					t.Fatal("rejected submission left a task")
				}
			} else {
				task := domain.ReviewTask{ID: "done-" + q.ID, QuestionID: q.ID, FlowID: prior.FlowID, Status: domain.StatusPublished, QuestionVersion: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if err := store.SaveTask(t.Context(), task); err != nil {
					t.Fatal(err)
				}
				if err := svc.ReturnPublishedForRevision(t.Context(), q.ID, "final", "管理员", "修改"); !errors.Is(err, storage.ErrQuestionLibraryConflict) {
					t.Fatalf("deleted unpublish: %v", err)
				}
				if after, _ := store.GetTask(t.Context(), task.ID); after.Status != domain.StatusPublished {
					t.Fatal("rejected unpublish created active revision")
				}
			}
			after, _ := store.GetQuestion(t.Context(), q.ID)
			if after.Status != status || after.LibraryState != domain.LibraryDeleted || after.DeletedReason != domain.DeleteReasonOwnerDelete || after.LibraryRevision != 4 {
				t.Fatal("review guard changed deletion", after)
			}
		})
	}
}

func TestPostgresOptionalReviewSuspendsConfirmationAndRejectsLateDecisionOnDelete(t *testing.T) {
	svc, store, _, prior := newRestoreServiceFixture(t)
	svc.RequireAICheck = true
	q := testQuestion("")
	q.ID = "active-optional"
	q.OwnerID = "owner"
	q.CreatedBy = "owner"
	q.BankIDs = nil
	q.LibraryState = domain.LibraryActive
	q.ConfirmedVersion = 1
	q.ConfirmationSource = domain.ConfirmSourceSelf
	q.ConfirmedBy = "owner"
	q.LibraryRevision = 4
	if err := store.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "initial-" + q.ID, QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, prior.FlowID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.GetQuestion(t.Context(), q.ID)
	if after.LibraryState != domain.LibraryUnfiled || after.ConfirmedVersion != 0 || after.LibraryRevision != 5 || after.Version != 1 {
		t.Fatalf("review did not suspend personal approval: %+v", after)
	}
	// Model an inconsistent pre-fix record. New author-delete APIs cannot create
	// this while a task is active; an old record still must not accept late votes.
	if _, err := store.DB().ExecContext(t.Context(), `UPDATE questions SET library_state='deleted',deleted_reason='owner_delete',deleted_by='owner',library_revision=library_revision+1 WHERE id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); !errors.Is(err, storage.ErrQuestionLibraryConflict) {
		t.Fatalf("late vote reached deleted content: %v", err)
	}
	records, _ := store.ListRecordsByTaskID(t.Context(), task.ID)
	if len(records) != 0 {
		t.Fatal("rejected late vote was persisted")
	}
	after, _ = store.GetQuestion(t.Context(), q.ID)
	if after.LibraryState != domain.LibraryDeleted || after.DeletedReason != domain.DeleteReasonOwnerDelete {
		t.Fatal(after)
	}
}
