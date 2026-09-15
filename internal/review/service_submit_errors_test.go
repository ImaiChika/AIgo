package review

import (
	"context"
	"errors"
	"testing"

	"aigo/internal/domain"
)

// 驳回是终态锁定：重提必须返回可识别的状态冲突错误，API 层据此返回 409 而不是 500。
func TestSubmitRejectedQuestionReturnsNotSubmittable(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()

	rejected := testQuestion("bank-neike")
	rejected.Status = domain.StatusRejected
	if err := questionStore.SaveQuestion(ctx, *rejected); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); !errors.Is(err, domain.ErrReviewNotSubmittable) {
		t.Fatalf("驳回终态重提应返回 ErrReviewNotSubmittable，实际: %v", err)
	}
}

// 未通过 AI 检查的草稿在强制前置开启时同样属于状态冲突，而不是服务器错误。
func TestSubmitUncheckedDraftReturnsNotSubmittable(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	svc.RequireAICheck = true
	ctx := context.Background()

	if err := questionStore.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); !errors.Is(err, domain.ErrReviewNotSubmittable) {
		t.Fatalf("AI 检查未通过的草稿应返回 ErrReviewNotSubmittable，实际: %v", err)
	}
}

// 退回修改后必须真的产生新版本，不能仅凭 ai_reviewed 状态绕过修改窗口重新送审。
func TestResubmitRevisionRequiresNewQuestionVersion(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	q := testQuestion("bank-neike")
	q.Status = domain.StatusAIReviewed
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}
	task := domain.ReviewTask{
		ID:                 "task-revision",
		QuestionID:         q.ID,
		FlowID:             "flow-test",
		SubmissionBankID:   "bank-neike",
		CurrentRound:       1,
		Status:             domain.StatusRevisionRequired,
		QuestionPrevStatus: domain.StatusAIReviewed,
		QuestionVersion:    q.Version,
		Attempt:            1,
		RoundResults:       []domain.RoundResult{{RoundNumber: 1}},
	}
	if err := reviewStore.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SubmitQuestionForBank(ctx, q.ID, task.FlowID, task.SubmissionBankID); !errors.Is(err, domain.ErrReviewNotSubmittable) {
		t.Fatalf("未产生新版本的退修题不应重提，实际: %v", err)
	}
	storedTask, _ := reviewStore.GetTask(ctx, task.ID)
	if storedTask.Status != domain.StatusRevisionRequired || storedTask.Attempt != 1 {
		t.Fatalf("拒绝未修改重提时任务不应变化: %+v", storedTask)
	}
}

// 退回修改后的题目沿用原流程，避免覆盖原任务的流程快照；如需换流程应先撤销原任务。
func TestResubmitRevisionCannotSwitchFlow(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	q := testQuestion("bank-neike")
	q.Status = domain.StatusAIReviewed
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}
	newFlow := testFlow()
	newFlow.ID = "flow-new"
	newFlow.Name = "新规则流程"
	newFlow.Rounds = []domain.RoundConfig{
		{RoundNumber: 1, Name: "新流程初审", ExpertIDs: []string{"r1"}, RequiredCount: 1},
		{RoundNumber: 2, Name: "新流程复审", ExpertIDs: []string{"r1"}, RequiredCount: 1},
	}
	if err := reviewStore.SaveFlowConfig(ctx, newFlow); err != nil {
		t.Fatal(err)
	}
	task := domain.ReviewTask{
		ID:                 "task-revision-switch",
		QuestionID:         q.ID,
		FlowID:             "flow-test",
		SubmissionBankID:   "bank-neike",
		CurrentRound:       1,
		Status:             domain.StatusRevisionRequired,
		QuestionPrevStatus: domain.StatusAIReviewed,
		QuestionVersion:    q.Version,
		RoundResults:       []domain.RoundResult{{RoundNumber: 1}},
	}
	if err := reviewStore.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	q.ClinicalStem += "（已按意见修订）"
	q.Version++
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitQuestionForBank(ctx, q.ID, newFlow.ID, newFlow.BankID); !errors.Is(err, domain.ErrReviewNotSubmittable) {
		t.Fatalf("退修题切换流程应被拒绝，实际: %v", err)
	}
	storedTask, _ := reviewStore.GetTask(ctx, task.ID)
	if storedTask.FlowID != "flow-test" || storedTask.Status != domain.StatusRevisionRequired || storedTask.QuestionVersion != 1 {
		t.Fatalf("切换流程被拒绝后原任务不得覆盖: %+v", storedTask)
	}
}

// 需修改任务不能继续沿用旧轮次投票，必须经过修改并重新送审。
func TestReviewRejectsVoteAfterRevisionRequired(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	q := testQuestion("bank-neike")
	q.Status = domain.StatusAIReviewed
	questionStore.SaveQuestion(ctx, *q)
	reviewStore.SaveFlowConfig(ctx, testFlow())
	task := domain.ReviewTask{
		ID:                 "task-revision-vote",
		QuestionID:         q.ID,
		FlowID:             "flow-test",
		SubmissionBankID:   "bank-neike",
		CurrentRound:       1,
		Status:             domain.StatusRevisionRequired,
		QuestionPrevStatus: domain.StatusAIReviewed,
		QuestionVersion:    q.Version,
		AssignedTo:         []string{"r1"},
		RoundResults:       []domain.RoundResult{{RoundNumber: 1}},
	}
	reviewStore.SaveTask(ctx, task)

	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err == nil {
		t.Fatal("需修改任务不应继续接受旧轮次投票")
	}
	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 0 {
		t.Fatalf("拒绝旧轮次投票不应写入记录，实际 %d 条", len(records))
	}
}
