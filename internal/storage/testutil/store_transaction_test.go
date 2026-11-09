package testutil

import (
	"aigo/internal/domain"
	"context"
	"errors"
	"testing"
)

func TestReviewTransactionRollbackPreservesPrivateCreator(t *testing.T) {
	qstore := NewMemoryStore()
	rs := NewMemoryReviewStore()
	ctx := context.Background()
	q := domain.A2Question{ID: "private-creator", ClinicalStem: "原始内容", Version: 1, Status: domain.StatusAIReviewed, OwnerID: "owner-id", CreatedBy: "owner-name"}
	if err := qstore.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected rollback")
	err := rs.WithReviewTransaction(ctx, qstore, func(tx context.Context) error {
		changed := q
		changed.Status = domain.StatusReviewing
		if err := qstore.SaveQuestion(tx, changed); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	actual, _ := qstore.GetQuestion(ctx, q.ID)
	if actual.CreatedBy != q.CreatedBy || actual.OwnerID != q.OwnerID || actual.Status != q.Status {
		t.Fatalf("rollback corrupted private author metadata: %+v", actual)
	}
}
