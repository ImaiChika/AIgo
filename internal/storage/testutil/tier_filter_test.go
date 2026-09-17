package testutil

import (
	"context"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// QuestionFilter.Tiers 题库分层过滤：与 PostgreSQL 实现保持相同语义
// （分层由状态推导，多分层取并集，与其他条件 AND）。
func TestSearchQuestionsTierFilter(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	seed := []domain.A2Question{
		{ID: "q-draft", Status: domain.StatusAIDraft},
		{ID: "q-reviewed", Status: domain.StatusAIReviewed},
		{ID: "q-published", Status: domain.StatusPublished},
		{ID: "q-rejected", Status: domain.StatusRejected},
	}
	for _, q := range seed {
		q.Version = 1
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(filter storage.QuestionFilter) []string {
		qs, total, err := store.SearchQuestions(ctx, filter, 1, 100)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(qs))
		for _, q := range qs {
			got = append(got, q.ID)
		}
		if len(got) != total {
			t.Fatalf("total %d 与页内 %v 不一致", total, got)
		}
		return got
	}

	// 空分层 = 不过滤
	if got := ids(storage.QuestionFilter{}); len(got) != 4 {
		t.Fatalf("无分层过滤应返回全部 4 题, got %v", got)
	}
	// 正式题库 = published
	if got := ids(storage.QuestionFilter{Tiers: []string{"formal"}}); len(got) != 1 || got[0] != "q-published" {
		t.Fatalf("tier=formal 应仅含 q-published, got %v", got)
	}
	// 过程题库 = 检查通过后的流程中状态；暂存草稿（待 AI 检查）对用户不可见
	// （同创建时间按 ID 倒序）
	if got := ids(storage.QuestionFilter{Tiers: []string{"working"}}); len(got) != 1 || got[0] != "q-reviewed" {
		t.Fatalf("tier=working 应仅含检查通过的 q-reviewed, got %v", got)
	}
	// 淘汰题库 = rejected/archived
	if got := ids(storage.QuestionFilter{Tiers: []string{"eliminated"}}); len(got) != 1 || got[0] != "q-rejected" {
		t.Fatalf("tier=eliminated 应仅含 q-rejected, got %v", got)
	}
	// 多分层并集
	if got := ids(storage.QuestionFilter{Tiers: []string{"formal", "eliminated"}}); len(got) != 2 {
		t.Fatalf("tier=formal+eliminated 应含 2 题, got %v", got)
	}
	// 分层与状态等值 AND 叠加
	if got := ids(storage.QuestionFilter{Tiers: []string{"working"}, Status: string(domain.StatusAIReviewed)}); len(got) != 1 || got[0] != "q-reviewed" {
		t.Fatalf("tier=working 且 status=ai_reviewed 应仅含 q-reviewed, got %v", got)
	}
}
