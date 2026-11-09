//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestC4ZeroRoundShareCapturesConfirmedVersionAndFiltersFrozenContent(t *testing.T) {
	s := libraryTestStore(t)
	ctx := t.Context()
	q := libraryTestQuestion(t, s, "share-versioned")
	q.ClinicalStem = "男，40岁。共享原始病例。最可能的诊断是"
	q.Profession = "内科"
	q.Version++
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	qptr, _ := s.GetQuestion(ctx, q.ID)
	q = *qptr
	if err := s.SaveReviewResult(ctx, domain.AIReviewResult{ID: "share-initial-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	confirmLibraryFixture(t, s, &q)
	if err := s.CreateQuestionShare(ctx, domain.QuestionShareRequest{ID: "share-fixed", QuestionID: q.ID, OwnerID: q.OwnerID, Status: domain.QuestionSharePending, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("confirmed zero-round question must be shareable: %v", err)
	}
	if _, err := s.ReviewQuestionShare(ctx, "share-fixed", "reviewer", domain.QuestionShareApproved, ""); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetQuestion(ctx, q.ID)
	current.ClinicalStem = "男，40岁。仅限个人的新修订。最可能的诊断是"
	current.Profession = "外科"
	current.Version++
	if err := s.SaveQuestion(ctx, *current); err != nil {
		t.Fatal(err)
	}
	filter := storage.QuestionFilter{GlobalOnly: true, GlobalStatuses: []string{"approved"}, Keyword: "共享原始", Professions: []string{"内科"}}
	rows, total, err := s.SearchQuestions(ctx, filter, 1, 10)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Version != 2 || rows[0].ClinicalStem != q.ClinicalStem {
		t.Fatalf("global read drifted with personal revision: rows=%+v total=%d error=%v", rows, total, err)
	}
	filter.Keyword = "仅限个人"
	_, total, err = s.SearchQuestions(ctx, filter, 1, 10)
	if err != nil || total != 0 {
		t.Fatal("private revision affected global search", total, err)
	}
	filter.Keyword = "共享原始"
	counts, err := s.CountQuestionsByStatus(ctx, filter)
	if err != nil || counts["published"] != 1 {
		t.Fatal("global frozen statistics", counts, err)
	}
}

func confirmedShareFixture(t *testing.T, s *Store, id string) domain.A2Question {
	t.Helper()
	q := libraryTestQuestion(t, s, id)
	if err := s.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "pass-" + id, QuestionID: id, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	confirmLibraryFixture(t, s, &q)
	return q
}

func TestShareSubmissionAndApprovalRejectStaleRevisions(t *testing.T) {
	cases := []struct {
		name  string
		alter func(*domain.QuestionShareRequest)
	}{
		{"content", func(r *domain.QuestionShareRequest) { r.ExpectedVersion++ }},
		{"library", func(r *domain.QuestionShareRequest) {
			v := *r.ExpectedLibraryRevision + 1
			r.ExpectedLibraryRevision = &v
		}},
		{"review", func(r *domain.QuestionShareRequest) {
			v := *r.ExpectedReviewRevision + 1
			r.ExpectedReviewRevision = &v
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := libraryTestStore(t)
			q := confirmedShareFixture(t, s, "stale")
			r := domain.QuestionShareRequest{ID: "share", QuestionID: q.ID, OwnerID: q.OwnerID, ExpectedVersion: q.Version, ExpectedLibraryRevision: &q.LibraryRevision, ExpectedReviewRevision: &q.ReviewRevision}
			tc.alter(&r)
			if err := s.CreateQuestionShare(t.Context(), r); !errors.Is(err, storage.ErrQuestionShareVersionConflict) {
				t.Fatalf("stale %s accepted: %v", tc.name, err)
			}
			got, _ := s.GetQuestionShareByQuestionID(t.Context(), q.ID)
			if got != nil {
				t.Fatal("stale submission persisted")
			}
		})
	}
	s := libraryTestStore(t)
	q := confirmedShareFixture(t, s, "approve")
	if err := s.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "approve-share", QuestionID: q.ID, OwnerID: q.OwnerID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewQuestionShareVersion(t.Context(), "approve-share", "reviewer", domain.QuestionShareApproved, "", 2); !errors.Is(err, storage.ErrQuestionShareVersionConflict) {
		t.Fatalf("wrong share revision accepted: %v", err)
	}
	approved, err := s.ReviewQuestionShareVersion(t.Context(), "approve-share", "reviewer", domain.QuestionShareApproved, "", 1)
	if err != nil || approved.Revision != 2 {
		t.Fatalf("approval result %+v %v", approved, err)
	}
	if _, err = s.ReviewQuestionShareVersion(t.Context(), "approve-share", "reviewer", domain.QuestionShareRejected, "", 1); !errors.Is(err, storage.ErrQuestionShareVersionConflict) {
		t.Fatalf("stale competing decision accepted: %v", err)
	}
}

func TestShareRequiresOriginalCheckEvidenceAndCurrentConfirmation(t *testing.T) {
	for _, name := range []string{"missing_first_check", "later_check_only", "unconfirmed_revision", "foreign_owner"} {
		t.Run(name, func(t *testing.T) {
			s := libraryTestStore(t)
			q := libraryTestQuestion(t, s, name)
			confirmLibraryFixture(t, s, &q)
			if name == "later_check_only" {
				if err := s.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "later", QuestionID: q.ID, QuestionVersion: 2, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			if name == "unconfirmed_revision" {
				q.ClinicalStem += "新修改"
				q.Version++
				if err := s.SaveQuestion(t.Context(), q); err != nil {
					t.Fatal(err)
				}
			}
			owner := q.OwnerID
			if name == "foreign_owner" {
				owner = "another"
			}
			err := s.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "share", QuestionID: q.ID, OwnerID: owner})
			want := storage.ErrQuestionShareNotFormal
			if name == "foreign_owner" {
				want = storage.ErrQuestionShareNotOwner
			}
			if !errors.Is(err, want) {
				t.Fatalf("ineligible %s: %v", name, err)
			}
		})
	}
}

func TestGlobalSharesKeepFrozenBanksAndEligibilityAfterPersonalReview(t *testing.T) {
	s := libraryTestStore(t)
	ctx := t.Context()
	for _, bank := range []string{"original", "private"} {
		if err := s.SaveBank(ctx, domain.QuestionBank{ID: bank, Name: bank, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	q := confirmedShareFixture(t, s, "bank-share")
	// Classification changes do not change content, but the submission captures it.
	q.BankIDs = []string{"original"}
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateQuestionShare(ctx, domain.QuestionShareRequest{ID: "bank-request", QuestionID: q.ID, OwnerID: q.OwnerID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewQuestionShare(ctx, "bank-request", "reviewer", domain.QuestionShareApproved, ""); err != nil {
		t.Fatal(err)
	}
	qptr, _ := s.GetQuestion(ctx, q.ID)
	q = *qptr
	q.BankIDs = []string{"private"}
	q.Status = domain.StatusRejected
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO review_flows(id,name,rounds) VALUES('new-flow','new-flow','[]'); INSERT INTO review_tasks(id,question_id,flow_id,status) VALUES('private-task','bank-share','new-flow','reviewing')`); err != nil {
		t.Fatal(err)
	}
	filter := storage.QuestionFilter{GlobalOnly: true, GlobalStatuses: []string{"approved"}, BankID: "original", NoActiveReviewTask: true, ExcludeDeleted: true, LibraryStates: []string{"active"}, ScopeRestricted: true, BankScope: []string{"original"}}
	rows, total, err := s.SearchQuestions(ctx, filter, 1, 1)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Status != domain.StatusPublished || len(rows[0].BankIDs) != 1 || rows[0].BankIDs[0] != "original" {
		t.Fatalf("global content or frozen filter drift: %+v %d %v", rows, total, err)
	}
	stats, err := s.AggregateQuestionStats(ctx, filter, 30)
	if err != nil || stats.Total != 1 || stats.ByBank["original"] != 1 || stats.ByBank["private"] != 0 || stats.Unclassified != 0 {
		t.Fatalf("frozen bank stats %+v %v", stats, err)
	}
	filter.BankID = "private"
	_, total, err = s.SearchQuestions(ctx, filter, 1, 1)
	if err != nil || total != 0 {
		t.Fatalf("private classification leaked %d %v", total, err)
	}
	filter.BankID = ""
	filter.Unclassified = true
	_, total, err = s.SearchQuestions(ctx, filter, 1, 1)
	if err != nil || total != 0 {
		t.Fatalf("private classification affected unclassified %d %v", total, err)
	}
}

func TestVersionedBulkShareBindsPreviewAndRollsBackWholeSelection(t *testing.T) {
	s := libraryTestStore(t)
	ctx := t.Context()
	a := confirmedShareFixture(t, s, "a")
	b := confirmedShareFixture(t, s, "b")
	items, total, err := s.PreviewQuestionShareItems(ctx, storage.QuestionFilter{OwnerID: a.OwnerID}, 500)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("preview %+v %d %v", items, total, err)
	}
	b.ClinicalStem += "未展示的新修订"
	b.Version++
	if err = s.SaveQuestion(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateQuestionShareItems(ctx, a.OwnerID, items); err == nil {
		t.Fatal("stale preview accepted")
	}
	for _, id := range []string{a.ID, b.ID} {
		if request, _ := s.GetQuestionShareByQuestionID(ctx, id); request != nil {
			t.Fatal("bulk partially committed", id)
		}
	}
	// The IDs-only legacy entrance cannot silently share a zero-round item.
	if ids, err := s.CreateQuestionShares(ctx, a.OwnerID, []string{a.ID}); err != nil || len(ids) != 0 {
		t.Fatal("zero-round bypassed three-axis selection", ids, err)
	}
	items, total, err = s.PreviewQuestionShareItems(ctx, storage.QuestionFilter{OwnerID: a.OwnerID}, 500)
	if err != nil || total != 1 {
		t.Fatal(items, total, err)
	}
	created, err := s.CreateQuestionShareItems(ctx, a.OwnerID, items)
	if err != nil || len(created) != 1 || created[0] != a.ID {
		t.Fatal(created, err)
	}
}

func TestShareApprovalRechecksSourceAxesAndSerializesCompetingDecisions(t *testing.T) {
	for _, axis := range []string{"version", "library_revision", "review_revision"} {
		t.Run(axis, func(t *testing.T) {
			s := libraryTestStore(t)
			q := confirmedShareFixture(t, s, "source-axis")
			if err := s.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "source-request", QuestionID: q.ID, OwnerID: q.OwnerID}); err != nil {
				t.Fatal(err)
			}
			// Simulate an out-of-band writer crossing the application pending lock.
			if _, err := s.db.ExecContext(t.Context(), `UPDATE questions SET `+axis+`=`+axis+`+1 WHERE id=$1`, q.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReviewQuestionShareVersion(t.Context(), "source-request", "reviewer", domain.QuestionShareApproved, "", 1); !errors.Is(err, storage.ErrQuestionShareVersionConflict) {
				t.Fatalf("approval ignored source %s change: %v", axis, err)
			}
			request, _ := s.GetQuestionShareByQuestionID(t.Context(), q.ID)
			if request.Status != domain.QuestionSharePending || request.Revision != 1 {
				t.Fatalf("failed approval mutated request %+v", request)
			}
		})
	}
	s := libraryTestStore(t)
	q := confirmedShareFixture(t, s, "competing-share")
	if err := s.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "competing-request", QuestionID: q.ID, OwnerID: q.OwnerID}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, status := range []domain.QuestionShareStatus{domain.QuestionShareApproved, domain.QuestionShareRejected} {
		go func() {
			_, err := s.ReviewQuestionShareVersion(t.Context(), "competing-request", "reviewer", status, "", 1)
			results <- err
		}()
	}
	successful, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successful++
		} else if errors.Is(err, storage.ErrQuestionShareVersionConflict) {
			conflicts++
		} else {
			t.Errorf("unexpected concurrent result %v", err)
		}
	}
	if successful != 1 || conflicts != 1 {
		t.Fatalf("decisions succeeded %d conflicts %d", successful, conflicts)
	}
	request, _ := s.GetQuestionShareByQuestionID(t.Context(), q.ID)
	if request.Revision != 2 || request.Status == domain.QuestionSharePending {
		t.Fatalf("non-atomic decision %+v", request)
	}
}
