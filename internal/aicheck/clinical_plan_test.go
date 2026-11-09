package aicheck

import (
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage/testutil"
)

func TestClinicalPlanFirstCheckUsesOriginalTaskAndRejectsDamagedMapping(t *testing.T) {
	client := &knowledgeCheckClient{}
	svc, questions, _, _ := newTestService(client)
	q := testDraft("clinical-first-check", 1)
	card := domain.KnowledgeCard{ID: "card", Revision: 2, Kind: "medical", Status: "active", Content: domain.KnowledgeContent{ExamTargets: []string{"测试目标"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "原始测试事实"}}}}
	q.WorkflowVersion = knowledgebase.WorkflowVersion
	q.GenerationContext = &domain.QuestionGenerationContext{WorkflowVersion: knowledgebase.WorkflowVersion, Bundle: domain.KnowledgeBundle{Cards: []domain.KnowledgeCard{card}}, Target: domain.WorkflowTarget{ID: "t1", CardID: "card", CardRevision: 2, Target: "测试目标"}, Basis: domain.KnowledgeBasis{Target: "测试目标", TargetID: "t1", FactRefs: []string{"card@2:f1"}, ConditionNotes: "仅测试条件", AnswerSupport: "仅测试依据", CasePlan: testutil.ClinicalPlanFixture("A", "card@2:f1", "反复上腹痛2年。")}}
	q.ClinicalStem, q.Explanation = knowledgebase.RenderClinicalPlan(q.GenerationContext.Basis.CasePlan)
	q.NormalizeGeneratedA2()
	if err := questions.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckQuestion(t.Context(), q.ID); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || !strings.Contains(client.prompt, "case_plan") || !strings.Contains(client.prompt, "不增加评分项、淘汰条件或复判轮次") {
		t.Fatal(client.calls, client.prompt)
	}
	if _, err := svc.CheckQuestion(t.Context(), q.ID); err != nil || client.calls != 1 {
		t.Fatal("rechecked", err, client.calls)
	}
	q.ID = "clinical-corrupt"
	q.GenerationContext.Basis.CasePlan.OptionEvidence[0].CaseRefs = []string{"unknown"}
	if err := questions.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckQuestion(t.Context(), q.ID); err == nil || client.calls != 1 {
		t.Fatal("corrupt mapping reached checker", err, client.calls)
	}
}
