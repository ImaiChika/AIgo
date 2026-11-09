package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"aigo/internal/domain"
)

var ErrQuestionLibraryConflict = errors.New("题库归属已变化，请刷新后重试")

type libraryRevisionKey struct{}

// WithLibraryRevision authorizes a library mutation against an explicitly read
// revision. Ordinary content/lifecycle saves cannot replace library metadata.
func WithLibraryRevision(ctx context.Context, expected int64) context.Context {
	return context.WithValue(ctx, libraryRevisionKey{}, expected)
}

type QuestionLibraryTransactionStore interface {
	WithLibraryTransaction(context.Context, []string, func(context.Context) error) error
}

type InitialCheckEvidenceStore interface {
	HasInitialCheckPass(context.Context, string) (bool, error)
}

// PrepareQuestionLibraryWrite shares invariants between production and test
// stores. The database's locked row is authoritative for content invalidation.
func PrepareQuestionLibraryWrite(ctx context.Context, existing *domain.A2Question, q *domain.A2Question, contentChanged bool) error {
	if q.LibraryState == "" {
		q.LibraryState = domain.LibraryUnfiled
	}
	if q.LibraryRevision == 0 {
		q.LibraryRevision = 1
	}
	if _, ok := domain.ParseLibraryState(string(q.LibraryState)); !ok {
		return fmt.Errorf("无效题库归属")
	}
	if existing == nil {
		return nil
	}
	old := *existing
	if old.LibraryState == "" {
		old.LibraryState = domain.LibraryUnfiled
	}
	if old.LibraryRevision == 0 {
		old.LibraryRevision = 1
	}
	expected, explicit := ctx.Value(libraryRevisionKey{}).(int64)
	if explicit {
		if expected != old.LibraryRevision || q.LibraryRevision != expected+1 {
			return ErrQuestionLibraryConflict
		}
	} else {
		if q.LibraryRevision != old.LibraryRevision || !sameLibraryMetadata(old, *q) {
			return ErrQuestionLibraryConflict
		}
	}
	// A deletion tombstone is authoritative. A stale optional-review writer
	// must not revive the item or replace the author's recoverable delete reason.
	if old.LibraryState == domain.LibraryDeleted {
		restore := explicit && !contentChanged && old.DeletedReason == domain.DeleteReasonOwnerDelete &&
			(old.Status == domain.StatusAIReviewed || old.Status == domain.StatusPublished) &&
			q.LibraryState == domain.LibraryUnfiled && q.Status == domain.StatusAIReviewed
		if !restore && (contentChanged || q.Status != old.Status || q.LibraryState != old.LibraryState) {
			return ErrQuestionLibraryConflict
		}
		return nil
	}
	changed := false
	actor := QuestionChangeFromContext(ctx).Actor
	if actor == "" {
		actor = "system"
	}
	now := q.UpdatedAt
	if now.IsZero() {
		now = time.Now()
	}
	// Old review endpoints still update QuestionStatus. Keep their final outcomes
	// coherent with personal library membership without fabricating self approval.
	switch {
	case q.Status == domain.StatusPublished && old.Status != domain.StatusPublished:
		q.LibraryState, q.ConfirmedVersion, q.ConfirmationSource = domain.LibraryActive, q.Version, domain.ConfirmSourceReview
		q.ConfirmedBy, q.ConfirmedAt = actor, &now
		q.DeletedReason, q.DeletedBy, q.DeletedFromState = "", "", ""
		changed = true
	case q.Status == domain.StatusRejected && old.Status != domain.StatusRejected:
		q.LibraryState, q.DeletedReason, q.DeletedBy, q.DeletedFromState = domain.LibraryDeleted, domain.DeleteReasonReviewReject, actor, old.LibraryState
		changed = true
	case q.Status == domain.StatusArchived && old.Status != domain.StatusArchived:
		q.LibraryState, q.DeletedReason, q.DeletedBy, q.DeletedFromState = domain.LibraryDeleted, domain.DeleteReasonAdminDelete, actor, old.LibraryState
		if QuestionChangeFromContext(ctx).ChangeType == "owner_archive" {
			q.DeletedReason = domain.DeleteReasonOwnerArchive
		}
		changed = true
	case old.LibraryState == domain.LibraryActive && (contentChanged || q.Status != old.Status):
		q.LibraryState = domain.LibraryUnfiled
		q.ConfirmedVersion, q.ConfirmationSource, q.ConfirmedBy, q.ConfirmedAt = 0, "", "", nil
		changed = true
	}
	if changed && !explicit {
		q.LibraryRevision = old.LibraryRevision + 1
	}
	return nil
}

func sameLibraryMetadata(a, b domain.A2Question) bool {
	return a.LibraryState == b.LibraryState && a.ConfirmedVersion == b.ConfirmedVersion && a.ConfirmationSource == b.ConfirmationSource && a.DeletedReason == b.DeletedReason && a.ConfirmedBy == b.ConfirmedBy && sameOptionalTime(a.ConfirmedAt, b.ConfirmedAt) && a.DeletedBy == b.DeletedBy && a.DeletedFromState == b.DeletedFromState
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
