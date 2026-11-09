package aicheck

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/storage/testutil"
	"context"
	"strings"
	"testing"
)

type knowledgeCheckClient struct {
	calls  int
	prompt string
}

func (c *knowledgeCheckClient) Complete(_ context.Context, m []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.calls++
	c.prompt = m[1].Content
	return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[],"suggestion":"测试通过"}`, nil
}
func TestKnowledgeWorkflowUsesOriginalSingleFirstCheck(t *testing.T) {
	ctx := context.Background()
	questions := testutil.NewMemoryStore()
	reviews := testutil.NewMemoryAIReviewStore()
	q := testDraft("knowledge-check", 1)
	card := domain.KnowledgeCard{ID: "card", Revision: 2, Kind: "medical", Status: "active", Title: "测试", Source: "测试来源", Content: domain.KnowledgeContent{ExamTargets: []string{"测试目标"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "冻结测试事实"}}}}
	q.WorkflowVersion = knowledgebase.LegacyWorkflowVersion
	q.GenerationContext = &domain.QuestionGenerationContext{WorkflowVersion: knowledgebase.LegacyWorkflowVersion, Bundle: domain.KnowledgeBundle{Cards: []domain.KnowledgeCard{card}}, Target: domain.WorkflowTarget{CardID: "card", CardRevision: 2, Target: "测试目标"}, Basis: domain.KnowledgeBasis{Target: "测试目标", FactRefs: []string{"card@2:f1"}, ConditionNotes: "仅测试条件", AnswerSupport: "测试依据"}}
	if e := questions.SaveQuestion(ctx, q); e != nil {
		t.Fatal(e)
	}
	client := &knowledgeCheckClient{}
	svc := NewService(client, questions, reviews, testutil.NewMemoryAICheckTaskStore(), "test")
	if _, e := svc.CheckQuestion(ctx, q.ID); e != nil {
		t.Fatal(e)
	}
	if client.calls != 1 || !strings.Contains(client.prompt, "冻结测试事实") || !strings.Contains(client.prompt, "不能仅因存在引用编号就判通过") {
		t.Fatal(client.calls, client.prompt)
	}
	if _, e := svc.CheckQuestion(ctx, q.ID); client.calls != 1 {
		t.Fatal("recheck permitted", e, client.calls)
	}
	q.ID = "knowledge-missing-context"
	q.Status = domain.StatusAIDraft
	q.GenerationContext = nil
	if e := questions.SaveQuestion(ctx, q); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.CheckQuestion(ctx, q.ID); e == nil || client.calls != 1 {
		t.Fatal("missing context silently fell back", e, client.calls)
	}
}
