package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage/testutil"
)

// ensureQuestionEditable 的编辑窗口规则：
// 仅"专家审核退回修改（需修改）"的题目（ai_reviewed + 任务 revision_required）可以编辑。
func TestEnsureQuestionEditable(t *testing.T) {
	questionStore := testutil.NewMemoryStore()
	reviewStore := testutil.NewMemoryReviewStore()
	svc := review.NewService(testutil.NewMemoryExpertStore(), reviewStore, questionStore, nil)
	server := &Server{reviewSvc: svc, questionStore: questionStore}
	ctx := context.Background()
	request := httptest.NewRequest("PUT", "/api/questions/q1", nil)

	mkQuestion := func(id string, status domain.QuestionStatus) *domain.A2Question {
		return &domain.A2Question{
			ID: id, ClinicalStem: "测试题干", Answer: "A",
			Options: []domain.Option{{Label: "A", Text: "选项"}, {Label: "B", Text: "选项"}},
			Status:  status, Version: 1,
		}
	}

	save := func(q *domain.A2Question) {
		if err := questionStore.SaveQuestion(ctx, *q); err != nil {
			t.Fatal(err)
		}
	}

	// 退回修改中的题（任务 revision_required）→ 可编辑
	revision := mkQuestion("q-revision", domain.StatusAIReviewed)
	save(revision)
	if err := reviewStore.SaveTask(ctx, domain.ReviewTask{ID: "t-revision", QuestionID: revision.ID, Status: domain.StatusRevisionRequired, QuestionVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := server.ensureQuestionEditable(request, revision); err != nil {
		t.Fatalf("退回修改中的题目应可编辑: %v", err)
	}

	// 普通已检查（无任务）→ 不可编辑
	plain := mkQuestion("q-plain", domain.StatusAIReviewed)
	save(plain)
	if err := server.ensureQuestionEditable(request, plain); err == nil {
		t.Fatal("未退回修改的已检查题目不应可编辑")
	}

	// 其余状态一律不可编辑
	for _, status := range []domain.QuestionStatus{
		domain.StatusAIDraft, domain.StatusAutoChecked, domain.StatusRejected,
		domain.StatusReviewing, domain.StatusConflict, domain.StatusPublished, domain.StatusArchived,
	} {
		q := mkQuestion("q-"+string(status), status)
		save(q)
		if err := server.ensureQuestionEditable(request, q); err == nil {
			t.Fatalf("状态 %s 不应可编辑", status)
		}
	}
}
