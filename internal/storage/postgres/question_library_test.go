//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func libraryTestStore(t *testing.T) *Store {
	t.Helper()
	_, dsn, cleanup := migrationTestSchema(t)
	t.Cleanup(cleanup)
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err = s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func libraryTestQuestion(t *testing.T, s *Store, id string) domain.A2Question {
	t.Helper()
	now := time.Now()
	q := domain.A2Question{ID: id, OwnerID: "owner", ClinicalStem: "患者，男，40岁。最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}}, Answer: "A", Version: 1, Status: domain.StatusAIReviewed, LibraryState: domain.LibraryUnfiled, LibraryRevision: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	return q
}

func confirmLibraryFixture(t *testing.T, s *Store, q *domain.A2Question) {
	t.Helper()
	old := q.LibraryRevision
	q.LibraryState, q.ConfirmedVersion, q.ConfirmationSource = domain.LibraryActive, q.Version, domain.ConfirmSourceSelf
	q.LibraryRevision++
	q.ConfirmedBy = q.OwnerID
	now := time.Now().UTC().Truncate(time.Microsecond)
	q.ConfirmedAt = &now
	if err := s.SaveQuestion(storage.WithLibraryRevision(t.Context(), old), *q); err != nil {
		t.Fatal(err)
	}
}

func TestLibrarySaveRejectsStaleMetadataAndInvalidatesContent(t *testing.T) {
	s := libraryTestStore(t)
	q := libraryTestQuestion(t, s, "cas")
	stale := q
	confirmLibraryFixture(t, s, &q)
	stale.Status = domain.StatusReviewing
	if err := s.SaveQuestion(t.Context(), stale); !errors.Is(err, storage.ErrQuestionLibraryConflict) {
		t.Fatalf("stale write: %v", err)
	}
	fresh, err := s.GetQuestion(t.Context(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ConfirmedBy != "owner" || fresh.ConfirmedAt == nil {
		t.Fatalf("missing confirmation provenance: %+v", fresh)
	}
	fresh.ClinicalStem += "。补充体征"
	fresh.Version++
	if err = s.SaveQuestion(t.Context(), *fresh); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetQuestion(t.Context(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LibraryState != domain.LibraryUnfiled || got.LibraryRevision != 3 || got.ConfirmedVersion != 0 || got.Version != 2 {
		t.Fatalf("content change preserved confirmation: %+v", got)
	}
	if err = s.SaveQuestion(t.Context(), *got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetQuestion(t.Context(), q.ID)
	if again.LibraryRevision != got.LibraryRevision {
		t.Fatal("no-op save changed library revision")
	}
}

func TestLibraryTransactionRollsBackQuestionsAndVersions(t *testing.T) {
	s := libraryTestStore(t)
	a := libraryTestQuestion(t, s, "a")
	b := libraryTestQuestion(t, s, "b")
	failure := errors.New("injected audit failure")
	err := s.WithLibraryTransaction(t.Context(), []string{b.ID, a.ID}, func(ctx context.Context) error {
		return s.WithLibraryTransaction(ctx, []string{a.ID}, func(ctx context.Context) error {
			for _, id := range []string{a.ID, b.ID} {
				q, err := s.GetQuestion(ctx, id)
				if err != nil {
					return err
				}
				q.ClinicalStem += "修订"
				q.Version++
				if err = s.SaveQuestion(ctx, *q); err != nil {
					return err
				}
			}
			return failure
		})
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		q, _ := s.GetQuestion(t.Context(), id)
		versions, _ := s.ListQuestionVersions(t.Context(), id)
		if q.Version != 1 || len(versions) != 1 {
			t.Fatalf("partial commit %s: q=%+v versions=%d", id, q, len(versions))
		}
	}
}

func TestLibraryTransactionsSerializeConflictingChanges(t *testing.T) {
	s := libraryTestStore(t)
	q := libraryTestQuestion(t, s, "serialize")
	locked, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	second := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() {
		first <- s.WithLibraryTransaction(ctx, []string{q.ID}, func(ctx context.Context) error {
			fresh, err := s.GetQuestion(ctx, q.ID)
			if err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			fresh.LibraryState, fresh.ConfirmedVersion, fresh.ConfirmationSource = domain.LibraryActive, 1, domain.ConfirmSourceSelf
			fresh.LibraryRevision++
			return s.SaveQuestion(storage.WithLibraryRevision(ctx, 1), *fresh)
		})
	}()
	<-locked
	go func() {
		second <- s.WithLibraryTransaction(ctx, []string{q.ID}, func(ctx context.Context) error { return s.SaveQuestion(ctx, q) })
	}()
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; !errors.Is(err, storage.ErrQuestionLibraryConflict) {
		t.Fatalf("second mutation must see committed revision: %v", err)
	}
}

func TestInitialCheckEvidenceDoesNotTrustLifecycleStatus(t *testing.T) {
	s := libraryTestStore(t)
	for _, tc := range []struct {
		name, verdict, taskStatus            string
		resultVersion, taskVersion, attempts int
		want                                 bool
	}{
		{"label_only", "", "", 0, 0, 0, false},
		{"legacy_pass", "pass", "", 1, 0, 0, true},
		{"pending", "pass", "pending", 1, 1, 0, false},
		{"never_called", "pass", "succeeded", 1, 1, 0, false},
		{"mismatch", "pass", "succeeded", 0, 1, 1, false},
		{"completed", "pass", "succeeded", 1, 1, 1, true},
		{"rejected", "reject", "succeeded", 1, 1, 1, false},
		{"later_check", "pass", "succeeded", 2, 2, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := libraryTestQuestion(t, s, tc.name)
			if tc.verdict != "" {
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO ai_review_results(id,question_id,question_version,verdict) VALUES($1,$1,$2,$3)`, q.ID, tc.resultVersion, tc.verdict); err != nil {
					t.Fatal(err)
				}
			}
			if tc.taskStatus != "" {
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO ai_check_tasks(id,question_id,question_version,status,attempts) VALUES($1,$1,$2,$3,$4)`, q.ID, tc.taskVersion, tc.taskStatus, tc.attempts); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.HasInitialCheckPass(t.Context(), q.ID)
			if err != nil || got != tc.want {
				t.Fatalf("evidence=%v want %v err=%v", got, tc.want, err)
			}
		})
	}
}

func TestRepeatedMigrateDoesNotReclassifyPostMigrationQuestions(t *testing.T) {
	s := libraryTestStore(t)
	for _, status := range []domain.QuestionStatus{domain.StatusPublished, domain.StatusRejected, domain.StatusArchived} {
		q := libraryTestQuestion(t, s, fmt.Sprint(status))
		if _, err := s.db.ExecContext(t.Context(), `UPDATE questions SET status=$2 WHERE id=$1`, q.ID, string(status)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"published", "rejected", "archived"} {
		q, _ := s.GetQuestion(t.Context(), id)
		if q.LibraryState != domain.LibraryUnfiled || q.ConfirmedVersion != 0 || q.LibraryRevision != 1 {
			t.Fatalf("restart reclassified new unfiled question: %+v", q)
		}
	}
}

func TestDeletedPublishedQuestionCannotCreateShare(t *testing.T) {
	s := libraryTestStore(t)
	q := libraryTestQuestion(t, s, "deleted-share")
	q.Status = domain.StatusPublished
	if err := s.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetQuestion(t.Context(), "deleted-share")
	if err != nil {
		t.Fatal(err)
	}
	q = *loaded
	old := q.LibraryRevision
	q.LibraryState, q.DeletedReason = domain.LibraryDeleted, domain.DeleteReasonOwnerDelete
	q.LibraryRevision++
	if err = s.SaveQuestion(storage.WithLibraryRevision(t.Context(), old), q); err != nil {
		t.Fatal(err)
	}
	err = s.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "single-deleted-share", QuestionID: q.ID, OwnerID: q.OwnerID, Status: domain.QuestionSharePending, CreatedAt: time.Now()})
	if err == nil {
		t.Fatal("deleted question was shared through single endpoint")
	}
	created, err := s.CreateQuestionShares(t.Context(), q.OwnerID, []string{q.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 0 {
		t.Fatalf("deleted question was shared through batch endpoint: %v", created)
	}
}

func TestNoActiveReviewTaskKeepsTerminalHistoryVisible(t *testing.T) {
	s := libraryTestStore(t)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO review_flows(id,name,rounds) VALUES('library-filter','library-filter','[]')`); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"none", "published", "rejected", "archived", "reviewing", "conflict", "revision_required", "hidden_old_active"} {
		q := libraryTestQuestion(t, s, "filter-"+status)
		if status == "none" {
			continue
		}
		taskStatus := status
		if status == "hidden_old_active" {
			taskStatus = "reviewing"
		}
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO review_tasks(id,question_id,flow_id,status) VALUES($1,$2,'library-filter',$3)`, "task-"+status, q.ID, taskStatus); err != nil {
			t.Fatal(err)
		}
		if status == "hidden_old_active" {
			if _, err := s.db.ExecContext(t.Context(), `INSERT INTO review_tasks(id,question_id,flow_id,status,created_at) VALUES('latest-terminal',$1,'library-filter','published',NOW()+INTERVAL '1 second')`, q.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	questions, total, err := s.SearchQuestions(t.Context(), storage.QuestionFilter{NoActiveReviewTask: true}, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("terminal history was excluded or hidden active task escaped: total=%d questions=%+v", total, questions)
	}
	for _, q := range questions {
		active, e := s.HasActiveReviewTask(t.Context(), q.ID)
		if e != nil || active {
			t.Fatalf("filter mismatch for %s: active=%v err=%v", q.ID, active, e)
		}
	}
	_, total, err = s.SearchQuestions(t.Context(), storage.QuestionFilter{NoReviewTask: true}, 1, 100)
	if err != nil || total != 1 {
		t.Fatalf("legacy NoReviewTask semantics changed: count=%d err=%v", total, err)
	}
}

func TestSharedQuestionProjectionOmitsPrivateLibraryActors(t *testing.T) {
	s := libraryTestStore(t)
	q := libraryTestQuestion(t, s, "private-library-actors")
	q.Status = domain.StatusPublished
	if err := s.SaveQuestion(storage.WithQuestionChange(t.Context(), storage.QuestionChange{Actor: "private-confirming-user"}), q); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE questions SET deleted_by='private-historical-deleter' WHERE id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "projection-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "public-projection", QuestionID: q.ID, OwnerID: q.OwnerID, Status: domain.QuestionSharePending, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewQuestionShare(t.Context(), "public-projection", "fixture-reviewer", domain.QuestionShareApproved, ""); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListQuestionShares(t.Context(), "approved", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("share projection count=%d", len(items))
	}
	public := items[0].Question
	// Opaque ownership IDs remain part of the existing UI contract; the newly
	// added audit actors and creator snapshot must stay private.
	if public.OwnerID != q.OwnerID || public.CreatedBy != "" || public.ConfirmedBy != "" || public.DeletedBy != "" {
		t.Fatalf("private library actor leaked: %+v", public)
	}
	private, _ := s.GetQuestion(t.Context(), q.ID)
	if private.ConfirmedBy != "private-confirming-user" || private.DeletedBy != "private-historical-deleter" {
		t.Fatal("redaction modified stored audit provenance")
	}
}
