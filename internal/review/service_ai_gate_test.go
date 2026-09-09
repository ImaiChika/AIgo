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

	if err := questionStore.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil || !strings.Contains(err.Error(), "AI 质量检查") {
		t.Fatalf("开启强制前置时 ai_draft 应被拒绝，实际错误: %v", err)
	}
	task, _ := reviewStore.GetTaskByQuestionID(ctx, "q1")
	if task != nil {
		t.Fatal("拒绝提交后不应创建审核任务")
	}

	// AI 检查通过（ai_reviewed）后可正常提交
	q, _ := questionStore.GetQuestion(ctx, "q1")
	q.Status = domain.StatusAIReviewed
	questionStore.ForceQuestionForTest(*q)
	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err != nil {
		t.Fatalf("ai_reviewed 应可提交审核: %v", err)
	}
}

// 强制前置关闭时保持历史行为：ai_draft 可直接送审。
func TestSubmitAllowsDraftWhenGateDisabled(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	svc.RequireAICheck = false
	ctx := context.Background()

	if err := questionStore.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatalf("关闭强制前置时 ai_draft 应可提交审核: %v", err)
	}
	if task == nil || task.ID == "" {
		t.Fatal("应创建审核任务")
	}
}

// 按题库批量提交时，未通过 AI 检查的草稿单独计数跳过，不影响其他题目送审。
func TestSubmitBankSkipsUncheckedDraftsWhenEnabled(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	svc.RequireAICheck = true
	ctx := context.Background()

	draft := testQuestion("bank-neike")
	checked := testQuestion("bank-neike")
	checked.ID = "q2"
	checked.Status = domain.StatusAIReviewed
	if err := questionStore.SaveQuestion(ctx, *draft); err != nil {
		t.Fatal(err)
	}
	if err := questionStore.SaveQuestion(ctx, *checked); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	result, err := svc.SubmitBank(ctx, "bank-neike", "flow-test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Submitted != 1 {
		t.Fatalf("仅 ai_reviewed 题应提交成功，实际 %d", result.Submitted)
	}
	if result.SkippedNotChecked != 1 {
		t.Fatalf("未检查草稿应单独计数跳过，实际 %d", result.SkippedNotChecked)
	}
}
