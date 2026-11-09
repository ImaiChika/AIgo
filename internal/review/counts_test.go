//go:build integration

package review

import (
	"aigo/internal/domain"
	"testing"
)

func TestReviewedCountTracksRestoration(t *testing.T) {
	svc, _, _, task := newRestoreServiceFixture(t)
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatal(err)
	}
	if count, err := svc.CountReviewedBy(t.Context(), "r1"); err != nil || count != 1 {
		t.Fatalf("before restore: %d %v", count, err)
	}
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RestoreReview(t.Context(), "restorer", "admin", RestoreRequest{OperationID: "count-restore-operation", PreviewToken: preview.Token, Reason: "重新分配流程"}); err != nil {
		t.Fatal(err)
	}
	if count, err := svc.CountReviewedBy(t.Context(), "r1"); err != nil || count != 0 {
		t.Fatalf("deleted review is still counted: %d %v", count, err)
	}
}
