package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage/testutil"
)

// ensureQuestionEditable 的编辑窗口规则：
// AI 检查通过且尚未送审的新题、以及审核退回修改的题目可以编辑，
// 且仅限出题人本人（历史题无生成者快照时回退到个人题库归属）。
func TestEnsureQuestionEditable(t *testing.T) {
	questionStore := testutil.NewMemoryStore()
	reviewStore := testutil.NewMemoryReviewStore()
	svc := review.NewService(testutil.NewMemoryExpertStore(), reviewStore, questionStore, nil)
	server := &Server{reviewSvc: svc, questionStore: questionStore}
	ctx := context.Background()
	creatorCtx := context.WithValue(context.Background(), auth.UsernameKey, "expert")
	request := httptest.NewRequest("PUT", "/api/questions/q1", nil).WithContext(creatorCtx)
	otherRequest := httptest.NewRequest("PUT", "/api/questions/q1", nil).
		WithContext(context.WithValue(context.Background(), auth.UsernameKey, "someone-else"))

	mkQuestion := func(id string, status domain.QuestionStatus) *domain.A2Question {
		return &domain.A2Question{
			ID: id, ClinicalStem: "测试题干", Answer: "A",
			Options: []domain.Option{{Label: "A", Text: "选项"}, {Label: "B", Text: "选项"}},
			Status:  status, Version: 1,
			CreatedBy: "expert",
		}
	}

	save := func(q *domain.A2Question) {
		if err := questionStore.SaveQuestion(ctx, *q); err != nil {
			t.Fatal(err)
		}
	}

	// 退回修改中的题（任务 revision_required）→ 出题人本人可编辑
	revision := mkQuestion("q-revision", domain.StatusAIReviewed)
	save(revision)
	if err := reviewStore.SaveTask(ctx, domain.ReviewTask{ID: "t-revision", QuestionID: revision.ID, Status: domain.StatusRevisionRequired, QuestionVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := server.ensureQuestionEditable(request, revision); err != nil {
		t.Fatalf("出题人本人应可编辑退回修改中的题目: %v", err)
	}
	// 非出题人不可编辑
	if err := server.ensureQuestionEditable(otherRequest, revision); !errors.Is(err, ErrNotQuestionCreator) {
		t.Fatalf("非出题人不应可编辑退回题目: %v", err)
	}

	// 历史题无生成者快照：回退到个人题库归属
	legacy := mkQuestion("q-legacy", domain.StatusAIReviewed)
	legacy.CreatedBy = ""
	legacy.OwnerID = "user-expert"
	save(legacy)
	if err := reviewStore.SaveTask(ctx, domain.ReviewTask{ID: "t-legacy", QuestionID: legacy.ID, Status: domain.StatusRevisionRequired, QuestionVersion: 1}); err != nil {
		t.Fatal(err)
	}
	ownerRequest := httptest.NewRequest("PUT", "/api/questions/q-legacy", nil).
		WithContext(context.WithValue(context.Background(), auth.UsernameKey, "user-expert"))
	if err := server.ensureQuestionEditable(ownerRequest, legacy); err != nil {
		t.Fatalf("无生成者快照时应回退到个人题库归属: %v", err)
	}
	if err := server.ensureQuestionEditable(otherRequest, legacy); !errors.Is(err, ErrNotQuestionCreator) {
		t.Fatalf("非归属人不应可编辑无快照题目: %v", err)
	}

	// 普通已检查且尚未创建审核任务 → 出题人可在新题页微调
	plain := mkQuestion("q-plain", domain.StatusAIReviewed)
	save(plain)
	if err := server.ensureQuestionEditable(request, plain); err != nil {
		t.Fatalf("尚未送审的已检查题目应可编辑: %v", err)
	}
	if err := server.ensureQuestionEditable(otherRequest, plain); !errors.Is(err, ErrNotQuestionCreator) {
		t.Fatalf("非出题人不应可编辑尚未送审题目: %v", err)
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
