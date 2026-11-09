package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"aigo/internal/domain"
)

func c4ModeFlow(t *testing.T, mode string, rounds int) domain.ReviewFlowConfig {
	t.Helper()
	raw := map[string]any{"id": "c4-mode-flow", "name": "可选审核", "completion_mode": mode}
	list := []domain.RoundConfig{}
	for i := 0; i < rounds; i++ {
		list = append(list, domain.RoundConfig{RoundNumber: i + 1, ExpertIDs: []string{fmt.Sprintf("r%d", i+1)}, RequiredCount: 1})
	}
	raw["rounds"] = list
	encoded, _ := json.Marshal(raw)
	var flow domain.ReviewFlowConfig
	if err := json.Unmarshal(encoded, &flow); err != nil {
		t.Fatal(err)
	}
	return flow
}
func TestC4OnLastRoundCompletesWithoutFinalDecision(t *testing.T) {
	for _, rounds := range []int{1, 2} {
		t.Run(fmt.Sprintf("rounds=%d", rounds), func(t *testing.T) {
			svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1", "r2"}}, nil)
			q := testQuestion("bank-neike")
			q.OwnerID = "author"
			if err := qs.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			flow := c4ModeFlow(t, "on_last_round", rounds)
			if err := svc.CreateFlow(t.Context(), flow); err != nil {
				t.Fatal(err)
			}
			task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= rounds; i++ {
				if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: fmt.Sprintf("r%d", i), Action: domain.StatusApproved, ExpectedRound: i}); err != nil {
					t.Fatal(err)
				}
			}
			done, _ := tasks.GetTask(t.Context(), task.ID)
			stored, _ := qs.GetQuestion(t.Context(), q.ID)
			if done.Status != domain.StatusPublished || done.FinalDecision != nil || stored.LibraryState != domain.LibraryActive || stored.ConfirmedVersion != q.Version || stored.ConfirmationSource != domain.ConfirmSourceReview || stored.Version != 1 {
				t.Fatalf("completion left hidden final step: task=%+v question=%+v", done, stored)
			}
		})
	}
}
func TestC4OnLastRoundDoesNotRequireUnrelatedFinalQualification(t *testing.T) {
	svc, _, _ := newTestService(map[string][]string{"bank-neike": {"r1"}}, nil)
	flow := c4ModeFlow(t, "on_last_round", 1)
	flow.FinalReviewerIDs = []string{"disabled-final"}
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatalf("unrelated final qualification blocked direct-round flow: %v", err)
	}
}
func TestC4DirectRoundPublicationRollsBackOnQuestionWriteFailure(t *testing.T) {
	svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1"}}, nil)
	q := testQuestion("bank-neike")
	if err := qs.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	flow := c4ModeFlow(t, "on_last_round", 1)
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	qs.FailNextSaveQuestion(errors.New("injected publish write failure"))
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err == nil {
		t.Fatal("direct completion skipped atomic question publication")
	}
	current, _ := tasks.GetTask(t.Context(), task.ID)
	records, _ := tasks.ListRecordsByTaskID(t.Context(), task.ID)
	if current.Status != domain.StatusReviewing || len(current.RoundResults[0].Reviews) != 0 || len(records) != 0 {
		t.Fatal("partial vote/publication survived rollback", current, records)
	}
}

func TestC4DirectRoundVoteOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rule     string
		required int
		votes    []domain.QuestionStatus
		want     domain.QuestionStatus
	}{
		{"threshold_early_with_disagreement", "", 2, []domain.QuestionStatus{domain.StatusRejected, domain.StatusApproved, domain.StatusApproved}, domain.StatusPublished},
		{"two_of_three_early", "", 2, []domain.QuestionStatus{domain.StatusApproved, domain.StatusApproved}, domain.StatusPublished},
		{"wait_for_unvoted", "", 2, []domain.QuestionStatus{domain.StatusApproved, domain.StatusRejected}, domain.StatusReviewing},
		{"all_collected_revision_first", "", 2, []domain.QuestionStatus{domain.StatusApproved, domain.StatusRejected, domain.StatusRevisionRequired}, domain.StatusRevisionRequired},
		{"all_collected_reject", "", 2, []domain.QuestionStatus{domain.StatusApproved, domain.StatusRejected, domain.StatusRejected}, domain.StatusRejected},
		{"veto_waits", "veto", 1, []domain.QuestionStatus{domain.StatusApproved, domain.StatusRevisionRequired}, domain.StatusReviewing},
		{"veto_revision_first", "veto", 1, []domain.QuestionStatus{domain.StatusApproved, domain.StatusRejected, domain.StatusRevisionRequired}, domain.StatusRevisionRequired},
		{"veto_all_approve", "veto", 1, []domain.QuestionStatus{domain.StatusApproved, domain.StatusApproved, domain.StatusApproved}, domain.StatusPublished},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1", "r2", "r3"}}, nil)
			q := testQuestion("bank-neike")
			if err := qs.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 1)
			flow.VoteRule = tc.rule
			flow.Rounds[0].ExpertIDs, flow.Rounds[0].RequiredCount = []string{"r1", "r2", "r3"}, tc.required
			if err := svc.CreateFlow(t.Context(), flow); err != nil {
				t.Fatal(err)
			}
			task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			for i, vote := range tc.votes {
				if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: fmt.Sprintf("r%d", i+1), Action: vote, Opinion: "评语"}); err != nil {
					t.Fatal(err)
				}
			}
			done, _ := tasks.GetTask(t.Context(), task.ID)
			after, _ := qs.GetQuestion(t.Context(), q.ID)
			records, _ := tasks.ListRecordsByTaskID(t.Context(), task.ID)
			if done.Status != tc.want || done.FinalDecision != nil || len(records) != len(tc.votes) || after.Version != 1 {
				t.Fatalf("wrong vote outcome: task=%+v q=%+v records=%v", done, after, records)
			}
			if tc.want == domain.StatusPublished && (after.LibraryState != domain.LibraryActive || after.ConfirmationSource != domain.ConfirmSourceReview || after.ConfirmedBy != fmt.Sprintf("r%d", len(tc.votes))) {
				t.Fatalf("direct review lost publication actor: %+v", after)
			}
			if tc.want == domain.StatusRevisionRequired && (after.Status != domain.StatusAIReviewed || after.LibraryState != domain.LibraryUnfiled) {
				t.Fatal("revision did not return to author", after)
			}
			if tc.want == domain.StatusRejected && (after.LibraryState != domain.LibraryDeleted || after.DeletedReason != domain.DeleteReasonReviewReject) {
				t.Fatal("rejection did not remain locked", after)
			}
		})
	}
}

func TestC4CompletionModeFrozenAndLegacyRetainsFinalStep(t *testing.T) {
	for _, mode := range []string{domain.ReviewCompletionOnLastRound, ""} {
		t.Run(map[bool]string{true: "on_last_round", false: "legacy_missing_mode"}[mode != ""], func(t *testing.T) {
			svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1"}}, map[string]bool{"admin1": true})
			q := testQuestion("bank-neike")
			if err := qs.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			flow := c4ModeFlow(t, mode, 1)
			if err := svc.CreateFlow(t.Context(), flow); err != nil {
				t.Fatal(err)
			}
			task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			frozenMode := domain.ReviewCompletionMode(mode)
			// Even a storage-level update cannot rewrite a task's frozen mode.
			task.CompletionMode = domain.ReviewCompletionExternalFinal
			if mode == "" {
				task.CompletionMode = domain.ReviewCompletionOnLastRound
			}
			if err := tasks.UpdateTask(t.Context(), *task); err != nil {
				t.Fatal(err)
			}
			if got, _ := tasks.GetTask(t.Context(), task.ID); got.CompletionMode != frozenMode {
				t.Fatal("task completion changed", got)
			}
			if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
				t.Fatal(err)
			}
			done, _ := tasks.GetTask(t.Context(), task.ID)
			if mode == "" {
				if done.Status != domain.StatusConflict {
					t.Fatal("legacy auto-published", done)
				}
				if err := svc.Finalize(t.Context(), FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved}); err != nil {
					t.Fatal(err)
				}
			} else if done.Status != domain.StatusPublished {
				t.Fatal("direct flow gained final step", done)
			}
		})
	}
}

func TestC4DirectRoundRejectsUnassignedFinalOverrideAndLostFutureReviewer(t *testing.T) {
	svc, tasks, qs := newTestService(map[string][]string{"bank-neike": {"r1", "r2"}}, map[string]bool{"admin1": true})
	q := testQuestion("bank-neike")
	if err := qs.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	flow := c4ModeFlow(t, domain.ReviewCompletionOnLastRound, 2)
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "admin1", HasFinalRight: true, Action: domain.StatusApproved}); err == nil {
		t.Fatal("unassigned final authority bypassed direct round assignment")
	}
	resolver := svc.users.(*fakeResolver)
	resolver.reviewers = map[string][]string{"bank-neike": {"r1"}}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err == nil {
		t.Fatal("revoked next-round reviewer was silently skipped")
	}
	done, _ := tasks.GetTask(t.Context(), task.ID)
	records, _ := tasks.ListRecordsByTaskID(t.Context(), task.ID)
	if done.CurrentRound != 1 || len(done.RoundResults[0].Reviews) != 0 || len(records) != 0 {
		t.Fatal("qualification failure partially advanced", done, records)
	}
}
