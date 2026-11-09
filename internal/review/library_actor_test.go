package review

import (
	"testing"

	"aigo/internal/domain"
)

func TestReviewLibraryOutcomeRecordsActualActor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final domain.QuestionStatus
		actor string
	}{
		{"final_approve", domain.StatusApproved, "admin1"},
		{"final_reject", domain.StatusRejected, "admin1"},
		{"vote_reject", "", "r2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, questions := newTestService(map[string][]string{"bank-neike": {"r1", "r2"}}, map[string]bool{"admin1": true})
			q := testQuestion("bank-neike")
			q.OwnerID = "author"
			q.CreatedBy = "author"
			if err := questions.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			flow := testFlow()
			flow.Rounds[0].RequiredCount = 1
			if tc.final == "" {
				flow.Rounds[0].RequiredCount = 2
			}
			if err := svc.reviewStore.SaveFlowConfig(t.Context(), flow); err != nil {
				t.Fatal(err)
			}
			task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			action := domain.StatusApproved
			if tc.final == "" {
				action = domain.StatusRejected
			}
			if err = svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: action, Opinion: "表述问题"}); err != nil {
				t.Fatal(err)
			}
			if tc.final != "" {
				err = svc.Finalize(t.Context(), FinalizeRequest{TaskID: task.ID, ReviewerID: tc.actor, Action: tc.final, Opinion: "最终结论"})
			} else {
				err = svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: action, Opinion: "表述问题"})
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := questions.GetQuestion(t.Context(), q.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.OwnerID != "author" || got.CreatedBy != "author" || got.Version != 1 {
				t.Fatalf("review changed authorship/content version: %+v", got)
			}
			if tc.final == domain.StatusApproved {
				if got.ConfirmedBy != tc.actor || got.ConfirmedAt == nil || got.ConfirmationSource != domain.ConfirmSourceReview {
					t.Fatalf("approval lost actual actor: %+v", got)
				}
			} else if got.DeletedBy != tc.actor || got.DeletedReason != domain.DeleteReasonReviewReject {
				t.Fatalf("rejection lost actual actor: %+v", got)
			}
		})
	}
}
