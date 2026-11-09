package storage

import (
	"errors"
	"testing"

	"aigo/internal/domain"
)

func TestDeletedLibraryTombstoneRejectsAutomaticLifecycleProjection(t *testing.T) {
	for _, status := range []domain.QuestionStatus{domain.StatusReviewing, domain.StatusConflict, domain.StatusPublished, domain.StatusRejected, domain.StatusArchived} {
		t.Run(string(status), func(t *testing.T) {
			old := domain.A2Question{ID: "deleted", Status: domain.StatusAIReviewed, Version: 1, LibraryState: domain.LibraryDeleted, LibraryRevision: 3, DeletedReason: domain.DeleteReasonOwnerDelete, DeletedBy: "owner"}
			next := old
			next.Status = status
			if err := PrepareQuestionLibraryWrite(t.Context(), &old, &next, false); !errors.Is(err, ErrQuestionLibraryConflict) {
				t.Fatalf("automated %s rewrote a deletion tombstone: next=%+v err=%v", status, next, err)
			}
		})
	}
}
func TestOwnerDeleteRestoreStillAllowedWithoutContentRewrite(t *testing.T) {
	old := domain.A2Question{ID: "deleted", Status: domain.StatusPublished, Version: 1, LibraryState: domain.LibraryDeleted, LibraryRevision: 3, DeletedReason: domain.DeleteReasonOwnerDelete}
	next := old
	next.Status = domain.StatusAIReviewed
	next.LibraryState = domain.LibraryUnfiled
	next.LibraryRevision++
	next.DeletedReason = ""
	if err := PrepareQuestionLibraryWrite(WithLibraryRevision(t.Context(), 3), &old, &next, false); err != nil {
		t.Fatal(err)
	}
	if next.LibraryState != domain.LibraryUnfiled || next.Status != domain.StatusAIReviewed || next.Version != 1 {
		t.Fatal(next)
	}
}
