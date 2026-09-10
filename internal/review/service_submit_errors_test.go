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

// 送审题库与流程绑定题库不一致属于请求错误，API 层据此返回 400。
func TestSubmitBankMismatchReturnsBadRequest(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()

	checked := testQuestion("bank-neike")
	checked.Status = domain.StatusAIReviewed
	if err := questionStore.SaveQuestion(ctx, *checked); err != nil {
		t.Fatal(err)
	}
	flow := testFlow()
	flow.BankID = "bank-other"
	if err := reviewStore.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SubmitQuestionForBank(ctx, "q1", "flow-test", "bank-neike"); !errors.Is(err, domain.ErrReviewBadRequest) {
		t.Fatalf("题库不匹配应返回 ErrReviewBadRequest，实际: %v", err)
	}
}
