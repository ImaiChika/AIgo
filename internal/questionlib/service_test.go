package questionlib

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

type stubTasks struct {
	active map[string]bool
	err    error
}

func (s stubTasks) ActiveTaskForQuestion(_ context.Context, id string) (bool, error) {
	return s.active[id], s.err
}

type libraryTestStore struct {
	*testutil.MemoryStore
	passed map[string]bool
	shares map[string]*domain.QuestionShareRequest
	failID string
}

func (s *libraryTestStore) HasInitialCheckPass(_ context.Context, id string) (bool, error) {
	return s.passed[id], nil
}
func (s *libraryTestStore) GetQuestionShareByQuestionID(_ context.Context, id string) (*domain.QuestionShareRequest, error) {
	return s.shares[id], nil
}
func (s *libraryTestStore) SaveQuestion(ctx context.Context, q domain.A2Question) error {
	if q.ID == s.failID {
		return errors.New("injected save failure")
	}
	return s.MemoryStore.SaveQuestion(ctx, q)
}
func confirmFixture(t *testing.T) (*libraryTestStore, *Service, domain.A2Question) {
	t.Helper()
	store := &libraryTestStore{MemoryStore: testutil.NewMemoryStore(), passed: map[string]bool{}, shares: map[string]*domain.QuestionShareRequest{}}
	q := domain.A2Question{ID: "q-confirm-1", OwnerID: "teacher-1", CreatedBy: "teacher-1", ClinicalStem: "患者，男，40岁。题干。该患者最可能的诊断是", Options: fiveOptions(), Answer: "A", Explanation: "解析", Status: domain.StatusAIReviewed, Version: 1}
	if err := store.SaveQuestion(storageChange(), q); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.GetQuestion(t.Context(), q.ID)
	store.passed[q.ID] = true
	return store, NewService(store, stubTasks{}), *saved
}
func storageChange() context.Context {
	return storage.WithQuestionChange(context.Background(), storage.QuestionChange{Actor: "teacher-1", OwnerID: "teacher-1"})
}

func TestAdmissionAcceptsTeacherWordingButRequiresEvidenceAndStructure(t *testing.T) {
	store, _, q := confirmFixture(t)
	q.FormatVersion = domain.A2FormatFocused
	q.Version = 2
	q.ClinicalStem = "临床病例：这位患者应该选择哪个处理方案？"
	q.Explanation = "老师自主编写的说明，不固定开头和结尾。"
	if err := ValidateAdmission(t.Context(), store, &q); err != nil {
		t.Fatal(err)
	}
	store.passed[q.ID] = false
	if err := ValidateAdmission(t.Context(), store, &q); err == nil {
		t.Fatal("missing first check passed")
	}
	store.passed[q.ID] = true
	q.Answer = "Z"
	if err := ValidateAdmission(t.Context(), store, &q); !errors.Is(err, domain.ErrInvalidQuestion) {
		t.Fatal("invalid answer accepted", err)
	}
}
func fiveOptions() []domain.Option {
	result := make([]domain.Option, 5)
	for i, label := range []string{"A", "B", "C", "D", "E"} {
		result[i] = domain.Option{Label: label, Text: "选项" + label}
	}
	return result
}
func itemFor(q domain.A2Question) ConfirmItem {
	library, review := q.LibraryRevision, q.ReviewRevision
	return ConfirmItem{QuestionID: q.ID, ExpectedVersion: q.Version, ExpectedLibraryRevision: &library, ExpectedReviewRevision: &review}
}
func currentItem(t *testing.T, s *libraryTestStore, id string) ConfirmItem {
	t.Helper()
	q, err := s.GetQuestion(t.Context(), id)
	if err != nil || q == nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return itemFor(*q)
}

func TestConfirmWritesActiveWithoutReviewTask(t *testing.T) {
	t.Parallel()
	store, svc, q := confirmFixture(t)
	now := time.Now()
	item := itemFor(q)
	confirmed, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{item}, now)
	if err != nil || confirmed != 1 {
		t.Fatalf("confirm: %d %v", confirmed, err)
	}
	got, _ := store.GetQuestion(t.Context(), q.ID)
	if got.LibraryState != domain.LibraryActive || got.ConfirmedVersion != 1 || got.ConfirmationSource != domain.ConfirmSourceSelf || got.ConfirmedBy != q.OwnerID || got.ConfirmedAt == nil || !got.ConfirmedAt.Equal(now) {
		t.Fatalf("bad confirmation: %+v", got)
	}
	if got.Status != domain.StatusAIReviewed || got.Version != 1 || got.LibraryRevision != q.LibraryRevision+1 {
		t.Fatalf("wrong revisions: %+v", got)
	}
	if confirmed, err = svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{item}, now.Add(time.Minute)); err != nil || confirmed != 1 {
		t.Fatalf("replay: %d %v", confirmed, err)
	}
	replay, _ := store.GetQuestion(t.Context(), q.ID)
	if replay.LibraryRevision != got.LibraryRevision || !replay.ConfirmedAt.Equal(*got.ConfirmedAt) {
		t.Fatal("replay must preserve revision and original confirmation time")
	}
}

func TestConfirmRejectsInvalidStates(t *testing.T) {
	t.Parallel()
	for _, status := range []domain.QuestionStatus{domain.StatusAIDraft, domain.StatusAutoChecked, domain.StatusReviewing, domain.StatusConflict, domain.StatusRevisionRequired, domain.StatusRejected, domain.StatusArchived, domain.StatusPublished} {
		t.Run(string(status), func(t *testing.T) {
			store, svc, q := confirmFixture(t)
			q.Status = status
			if err := store.SaveQuestionRawForTest(q); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now()); err == nil {
				t.Fatal("locked state allowed")
			}
		})
	}
}

func TestConfirmRequiresExpectedVersion(t *testing.T) {
	t.Parallel()
	_, svc, q := confirmFixture(t)
	for _, field := range []string{"version", "library_revision", "review_revision"} {
		t.Run(field, func(t *testing.T) {
			item := itemFor(q)
			switch field {
			case "version":
				item.ExpectedVersion = 0
			case "library_revision":
				item.ExpectedLibraryRevision = nil
			case "review_revision":
				item.ExpectedReviewRevision = nil
			}
			if _, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{item}, time.Now()); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("missing guard accepted: %v", err)
			}
		})
	}
}

func TestConfirmRequiresInitialCheckEvidence(t *testing.T) {
	t.Parallel()
	store, svc, q := confirmFixture(t)
	delete(store.passed, q.ID)
	if _, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now()); err == nil {
		t.Fatal("status alone must not replace persisted AI pass evidence")
	}
}

func TestLibraryMutationsRejectStaleGuardsAndForeignOwner(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"confirm", "delete", "restore"} {
		t.Run(operation, func(t *testing.T) {
			store, svc, q := confirmFixture(t)
			if operation == "restore" {
				if _, err := svc.Delete(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now()); err != nil {
					t.Fatal(err)
				}
				qptr, _ := store.GetQuestion(t.Context(), q.ID)
				q = *qptr
			}
			for _, field := range []string{"version", "library_revision", "review_revision"} {
				t.Run(field, func(t *testing.T) {
					item := itemFor(q)
					switch field {
					case "version":
						item.ExpectedVersion++
					case "library_revision":
						*item.ExpectedLibraryRevision += 9
					case "review_revision":
						*item.ExpectedReviewRevision++
					}
					if _, err := svc.mutate(t.Context(), q.OwnerID, []ConfirmItem{item}, time.Now(), operation); !errors.Is(err, ErrLibraryConflict) {
						t.Fatalf("stale guard: %v", err)
					}
				})
			}
			if _, err := svc.mutate(t.Context(), "other", []ConfirmItem{itemFor(q)}, time.Now(), operation); !errors.Is(err, ErrNotOwner) {
				t.Fatalf("foreign owner: %v", err)
			}
		})
	}
}

func TestEditDropsConfirmThenReconfirm(t *testing.T) {
	t.Parallel()
	store, svc, q := confirmFixture(t)
	if _, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	edited, _ := store.GetQuestion(t.Context(), q.ID)
	edited.Explanation = "教师补充解析"
	edited.Version++
	if err := store.SaveQuestion(storageChange(), *edited); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetQuestion(t.Context(), q.ID)
	if got.LibraryState != domain.LibraryUnfiled || got.PersonallyExportable() {
		t.Fatalf("edit retained confirmation: %+v", got)
	}
	if _, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{itemFor(*got)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetQuestion(t.Context(), q.ID)
	if !got.PersonallyExportable() {
		t.Fatal("reconfirmation should restore exportability")
	}
}

func TestDeleteAndRestoreBoundaries(t *testing.T) {
	t.Parallel()
	for _, status := range []domain.QuestionStatus{domain.StatusAIReviewed, domain.StatusPublished} {
		t.Run(string(status), func(t *testing.T) {
			store, svc, q := confirmFixture(t)
			q.Status = status
			if status == domain.StatusPublished {
				q.LibraryState = domain.LibraryActive
				q.ConfirmedVersion = q.Version
				q.ConfirmationSource = domain.ConfirmSourceLegacy
			}
			if err := store.SaveQuestionRawForTest(q); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Delete(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now()); err != nil {
				t.Fatal(err)
			}
			deleted, _ := store.GetQuestion(t.Context(), q.ID)
			if deleted.LibraryState != domain.LibraryDeleted || deleted.DeletedReason != domain.DeleteReasonOwnerDelete || deleted.DeletedBy != q.OwnerID || deleted.DeletedFromState != q.LibraryState {
				t.Fatalf("bad delete metadata: %+v", deleted)
			}
			if _, err := svc.Restore(t.Context(), q.OwnerID, []ConfirmItem{itemFor(*deleted)}, time.Now()); err != nil {
				t.Fatal(err)
			}
			got, _ := store.GetQuestion(t.Context(), q.ID)
			if got.LibraryState != domain.LibraryUnfiled || got.ConfirmedVersion != 0 || got.ConfirmationSource != "" || got.Status != domain.StatusAIReviewed || got.Version != q.Version {
				t.Fatalf("bad restore: %+v", got)
			}
			if _, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{itemFor(*got)}, time.Now()); err != nil {
				t.Fatalf("restored question cannot reconfirm: %v", err)
			}
		})
	}
	for _, status := range []domain.QuestionStatus{domain.StatusRejected, domain.StatusArchived, domain.StatusReviewing, domain.StatusConflict, domain.StatusRevisionRequired} {
		t.Run("locked_"+string(status), func(t *testing.T) {
			store, svc, q := confirmFixture(t)
			q.Status = status
			q.LibraryState = domain.LibraryDeleted
			q.DeletedReason = domain.DeleteReasonOwnerDelete
			if err := store.SaveQuestionRawForTest(q); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Restore(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now()); err == nil {
				t.Fatal("forged owner-delete must not revive terminal/inflight status")
			}
		})
	}
}

func TestConfirmBatchConflictDoesNotPartiallyConfirm(t *testing.T) {
	t.Parallel()
	store, svc, q := confirmFixture(t)
	missing := itemFor(q)
	missing.QuestionID = "missing"
	n, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q), missing}, time.Now())
	if err == nil || n != 0 {
		t.Fatalf("batch should reject with zero results: %d %v", n, err)
	}
	got, _ := store.GetQuestion(t.Context(), q.ID)
	if got.LibraryState == domain.LibraryActive || got.LibraryRevision != q.LibraryRevision {
		t.Fatal("earlier confirmation persisted after later conflict")
	}
}

func TestLibraryMutationsRollBackLaterWriteFailure(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"confirm", "delete", "restore"} {
		t.Run(operation, func(t *testing.T) {
			store, svc, q := confirmFixture(t)
			q2 := q
			q2.ID = "q-confirm-2"
			if err := store.SaveQuestion(storageChange(), q2); err != nil {
				t.Fatal(err)
			}
			store.passed[q2.ID] = true
			if operation == "restore" {
				if _, err := svc.Delete(t.Context(), q.OwnerID, []ConfirmItem{currentItem(t, store, q.ID), currentItem(t, store, q2.ID)}, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := store.GetQuestion(t.Context(), q.ID)
			store.failID = q2.ID
			n, err := svc.mutate(t.Context(), q.OwnerID, []ConfirmItem{currentItem(t, store, q.ID), currentItem(t, store, q2.ID)}, time.Now(), operation)
			if err == nil || n != 0 {
				t.Fatalf("write failure: %d %v", n, err)
			}
			got, _ := store.GetQuestion(t.Context(), q.ID)
			if got.LibraryRevision != before.LibraryRevision || got.LibraryState != before.LibraryState {
				t.Fatalf("partial %s: before=%+v after=%+v", operation, before, got)
			}
		})
	}
}

func TestLibraryMutationsRespectDurableReviewAndShareLocks(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"confirm", "delete", "restore"} {
		t.Run(operation, func(t *testing.T) {
			for _, lock := range []string{"review", "share_pending", "share_approved", "review_unavailable"} {
				t.Run(lock, func(t *testing.T) {
					store, svc, q := confirmFixture(t)
					if operation == "restore" {
						if _, err := svc.Delete(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now()); err != nil {
							t.Fatal(err)
						}
						ptr, _ := store.GetQuestion(t.Context(), q.ID)
						q = *ptr
					}
					switch lock {
					case "review":
						svc.tasks = stubTasks{active: map[string]bool{q.ID: true}}
					case "review_unavailable":
						svc.tasks = nil
					default:
						status := domain.QuestionSharePending
						if lock == "share_approved" {
							status = domain.QuestionShareApproved
						}
						store.shares[q.ID] = &domain.QuestionShareRequest{Status: status}
					}
					if _, err := svc.mutate(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q)}, time.Now(), operation); err == nil {
						t.Fatal("lock bypassed")
					}
					got, _ := store.GetQuestion(t.Context(), q.ID)
					if got.LibraryRevision != q.LibraryRevision {
						t.Fatal("blocked mutation changed state")
					}
				})
			}
		})
	}
}

func TestConfirmBatchLimits(t *testing.T) {
	t.Parallel()
	_, svc, q := confirmFixture(t)
	if _, err := svc.Confirm(t.Context(), q.OwnerID, nil, time.Now()); err == nil {
		t.Fatal("empty batch accepted")
	}
	items := make([]ConfirmItem, MaxConfirmBatch+1)
	for i := range items {
		items[i] = itemFor(q)
		items[i].QuestionID = fmt.Sprintf("q-%d", i)
	}
	if _, err := svc.Confirm(t.Context(), q.OwnerID, items, time.Now()); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("batch limit: %v", err)
	}
	if _, err := svc.Confirm(t.Context(), q.OwnerID, []ConfirmItem{itemFor(q), itemFor(q)}, time.Now()); err == nil {
		t.Fatal("duplicate item accepted")
	}
}
