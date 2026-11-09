package testutil

import (
	"context"
	"testing"

	"aigo/internal/domain"
)

func TestSaveQuestionCannotOverwriteNewerLibraryState(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	q := versionedQuestion()
	q.Status = domain.StatusAIReviewed
	q.LibraryState, q.LibraryRevision = domain.LibraryUnfiled, 1
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	stale := q
	q.LibraryState, q.LibraryRevision, q.ConfirmedVersion, q.ConfirmationSource = domain.LibraryActive, 2, 1, domain.ConfirmSourceSelf
	// This fixture represents a committed confirmation by another window.
	s.ForceQuestionForTest(q)
	stale.Status = domain.StatusReviewing
	if err := s.SaveQuestion(ctx, stale); err == nil {
		t.Fatal("stale lifecycle write overwrote the committed library confirmation")
	}
	got, _ := s.GetQuestion(ctx, q.ID)
	if got.LibraryState != domain.LibraryActive || got.LibraryRevision != 2 {
		t.Fatalf("lost confirmation: %+v", got)
	}
}

func TestSaveQuestionOldReviewOutcomeUpdatesLibrary(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         domain.QuestionStatus
		state          domain.LibraryState
		source, reason string
	}{
		{"approved", domain.StatusPublished, domain.LibraryActive, domain.ConfirmSourceReview, ""},
		{"rejected", domain.StatusRejected, domain.LibraryDeleted, "", domain.DeleteReasonReviewReject},
		{"archived", domain.StatusArchived, domain.LibraryDeleted, "", domain.DeleteReasonAdminDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemoryStore()
			ctx := context.Background()
			q := versionedQuestion()
			q.Status, q.LibraryState, q.LibraryRevision = domain.StatusAIReviewed, domain.LibraryUnfiled, 1
			if err := s.SaveQuestion(ctx, q); err != nil {
				t.Fatal(err)
			}
			q.Status = tc.status
			if err := s.SaveQuestion(ctx, q); err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetQuestion(ctx, q.ID)
			if got.LibraryState != tc.state || got.ConfirmationSource != tc.source || got.DeletedReason != tc.reason {
				t.Fatalf("review outcome has inconsistent library metadata: %+v", got)
			}
		})
	}
}
