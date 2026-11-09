package review

import (
	"context"
	"strings"
	"testing"

	"aigo/internal/domain"
)

// 送审强制前置开启时，未通过 AI 检查的草稿不能直接提交审核。
func TestSubmitRequiresAIReviewWhenEnabled(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	svc.RequireAICheck = true
	ctx := context.Background()

	question := testQuestion("bank-neike")
	question.Status = domain.StatusAIDraft
	if err := questionStore.SaveQuestion(ctx, *question); err != nil {
		t.Fatal(err)
	}
	flow := testFlow()
	flow.Rounds[0].ExpertIDs = []string{"r1"}
	if err := reviewStore.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil || !strings.Contains(err.Error(), "AI 质量检查") {
		t.Fatalf("开启强制前置时 ai_draft 应被拒绝，实际错误: %v", err)
	}
	task, _ := reviewStore.GetTaskByQuestionID(ctx, "q1")
	if task != nil {
		t.Fatal("拒绝提交后不应创建审核任务")
	}

	q, _ := questionStore.GetQuestion(ctx, "q1")
	q.Status = domain.StatusAIReviewed
	questionStore.ForceQuestionForTest(*q)
	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err != nil {
		t.Fatalf("ai_reviewed 应可提交审核: %v", err)
	}
}

// 即使旧配置关闭门禁，ai_draft 也不得绕过唯一一次 AI 检查。
func TestSubmitStillRejectsDraftWhenLegacyGateFlagDisabled(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	question := testQuestion("bank-neike")
	question.Status = domain.StatusAIDraft
	if err := questionStore.SaveQuestion(ctx, *question); err != nil {
		t.Fatal(err)
	}
	flow := testFlow()
	flow.Rounds[0].ExpertIDs = []string{"r1"}
	if err := reviewStore.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if task, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil || task != nil {
		t.Fatalf("旧配置不得允许 ai_draft 绕过检查: task=%+v err=%v", task, err)
	}
}
