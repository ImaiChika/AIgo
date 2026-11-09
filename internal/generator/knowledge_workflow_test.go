package generator

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type workflowTestClient struct {
	response string
	calls    int
	messages []llm.Message
	options  llm.GenerateOptions
}

func (c *workflowTestClient) Complete(_ context.Context, m []llm.Message, opts llm.GenerateOptions) (string, error) {
	c.calls++
	c.messages = append([]llm.Message(nil), m...)
	c.options = opts
	return c.response, nil
}
func workflowFixture() (domain.GenerationRequest, map[string]any) {
	kp := domain.KnowledgePoint{ID: "test-point", VersionID: "test-version", Topic: "胃溃疡", OutlineCode: "001", Subject: "消化系统"}
	card := domain.KnowledgeCard{ID: "card-one", Revision: 2, Kind: "medical", Status: "active", Title: "测试用卡", Source: "仅测试教材", KnowledgePoints: []domain.KnowledgePoint{kp}, Content: domain.KnowledgeContent{ExamTargets: []string{"测试诊断"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "仅测试事实", AppliesWhen: "测试前提"}}}}
	snapshot := &domain.KnowledgeWorkflowSnapshot{Version: knowledgebase.LegacyWorkflowVersion, CapturedAt: time.Now(), Bundles: []domain.KnowledgeBundle{{KnowledgePointID: kp.ID, Cards: []domain.KnowledgeCard{card}}}}
	item := map[string]any{"clinical_stem": "患者，男，45岁。反复上腹痛2年，加重1天。查体：T 36.8℃，P 80次/分，R 18次/分，BP 120/80mmHg，上腹部压痛。该患者最可能的诊断是", "options": []domain.Option{{Label: "A", Text: "胃溃疡"}, {Label: "B", Text: "十二指肠溃疡"}, {Label: "C", Text: "胃癌"}, {Label: "D", Text: "慢性胃炎"}, {Label: "E", Text: "功能性消化不良"}}, "answer": "A", "explanation": "正确答案为 A。反复上腹痛结合疼痛特点考虑胃溃疡。B 项疼痛规律不同；C 项需进一步检查；D 项不能解释典型节律性疼痛；E 项应先排除器质性疾病。故选 A。", "difficulty": "0.65", "cognitive_level": "应用", "exam_points": "诊断与鉴别诊断，临床表现", "knowledge_basis": domain.KnowledgeBasis{Target: "测试诊断", FactRefs: []string{"card-one@2:f1"}, ConditionNotes: "仅测试前提", AnswerSupport: "仅测试依据"}}
	return domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{kp}, Count: 1, Workflow: snapshot}, item
}
func TestKnowledgeWorkflowOneGenerationCallAndFrozenArtifact(t *testing.T) {
	req, item := workflowFixture()
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	questions, e := NewService(client).Generate(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if client.calls != 1 || len(questions) != 1 || questions[0].GenerationContext == nil {
		t.Fatal(client.calls, questions)
	}
	if !client.options.MedicalSearchHint {
		t.Fatal("generation did not request the optional network reminder")
	}
	if !strings.Contains(client.messages[1].Content, "card-one@2:f1") || !strings.Contains(client.messages[1].Content, "ordered_targets") {
		t.Fatal("missing frozen material")
	}
	if req.Workflow.Bundles[0].Cards[0].Content.Facts[0].ID != "f1" {
		t.Fatal("prompt projection mutated frozen card")
	}
	public, _ := json.Marshal(questions[0])
	if strings.Contains(string(public), "仅测试事实") || strings.Contains(string(public), "knowledge_basis") {
		t.Fatal("raw references leaked into question DTO")
	}
	req.Workflow = nil
	client.response = string(raw)
	direct, e := NewService(client).Generate(context.Background(), req)
	if e != nil || direct[0].GenerationContext != nil || strings.Contains(client.messages[1].Content, "ordered_targets") {
		t.Fatal("direct generation changed", e)
	}
	if !client.options.MedicalSearchHint {
		t.Fatal("direct generation lost the optional network reminder")
	}
}
func TestKnowledgeWorkflowRejectsInvalidReferencesAndTargets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		basis domain.KnowledgeBasis
	}{{"unknown fact", domain.KnowledgeBasis{Target: "测试诊断", FactRefs: []string{"nonexistent"}, ConditionNotes: "条件", AnswerSupport: "依据"}}, {"wrong target", domain.KnowledgeBasis{Target: "另一个目标", FactRefs: []string{"card-one@2:f1"}, ConditionNotes: "条件", AnswerSupport: "依据"}}, {"unconditional", domain.KnowledgeBasis{Target: "测试诊断", FactRefs: []string{"card-one@2:f1"}, AnswerSupport: "依据"}}} {
		t.Run(tc.name, func(t *testing.T) {
			req, item := workflowFixture()
			item["knowledge_basis"] = tc.basis
			raw, _ := json.Marshal([]any{item})
			client := &workflowTestClient{response: string(raw)}
			q, e := NewService(client).Generate(context.Background(), req)
			if e == nil || len(q) > 0 || client.calls != 1 {
				t.Fatal("invalid trace accepted", q, e, client.calls)
			}
		})
	}
	req, item := workflowFixture()
	req.Workflow.Version = "unsupported"
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	if _, e := NewService(client).Generate(context.Background(), req); e == nil || client.calls != 0 {
		t.Fatal("bad snapshot called provider", e, client.calls)
	}
}

func TestKnowledgeWorkflowOutputContractDoesNotConflictWithFrozenBaseline(t *testing.T) {
	for _, personal := range []bool{false, true} {
		req, item := workflowFixture()
		if personal {
			req.PromptSnapshot = &domain.GenerationPromptSnapshot{Appendix: "\n本次个人表达偏好"}
			FreezePromptBaseline(req.PromptSnapshot, []domain.GenerationRequest{req})
		}
		before, _ := json.Marshal(req)
		raw, _ := json.Marshal([]any{item})
		client := &workflowTestClient{response: string(raw)}
		if _, e := NewService(client).Generate(t.Context(), req); e != nil {
			t.Fatal(e)
		}
		system := client.messages[0].Content
		if strings.Contains(system, "exam_points 七个字段") || !strings.Contains(system, "exam_points、knowledge_basis 八个字段") {
			t.Fatalf("knowledge output conflicts with system field allowlist (personal=%v)", personal)
		}
		if !strings.Contains(system, "target必须逐字复制") {
			t.Fatal("exact target contract missing at system level")
		}
		after, _ := json.Marshal(req)
		if string(after) != string(before) {
			t.Fatal("generation rewrote frozen input")
		}
		req.Workflow = nil
		if _, e := NewService(client).Generate(t.Context(), req); e != nil {
			t.Fatal(e)
		}
		want := getSystemPrompt(false)
		if personal {
			want = req.PromptSnapshot.SystemPrompt
		}
		if client.messages[0].Content != want || strings.Contains(client.messages[1].Content, "knowledge_material") {
			t.Fatal("direct generation baseline changed")
		}
	}
}

func TestOptionalEmptyWorkflowMatchesDirectGeneration(t *testing.T) {
	req, item := workflowFixture()
	delete(item, "knowledge_basis")
	raw, _ := json.Marshal([]any{item})
	c := &workflowTestClient{response: string(raw)}
	req.Workflow = nil
	direct, err := NewService(c).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(c.messages)
	var optional domain.KnowledgeWorkflowSnapshot
	json.Unmarshal([]byte(`{"version":"knowledge-v4","reference_protocol":"fact-alias-v1","material_policy":"optional-materials-v1","presentation":{"section_order":["person","chief_complaint","present_illness","past_history","vitals","physical_findings","lab_results","imaging_results","other","lead_in"]},"bundles":[]}`), &optional)
	optional.Presentation = domain.StandardPresentation()
	optional.Bundles = []domain.KnowledgeBundle{{KnowledgePointID: req.KnowledgePoints[0].ID, Cards: []domain.KnowledgeCard{}}}
	req.Workflow = &optional
	got, err := NewService(c).Generate(t.Context(), req)
	if err != nil {
		t.Fatal("empty materials blocked ordinary generation", err)
	}
	after, _ := json.Marshal(c.messages)
	if string(before) != string(after) || got[0].ClinicalStem != direct[0].ClinicalStem || got[0].GenerationContext != nil {
		t.Fatal("empty lookup changed direct contract")
	}
	if req.Workflow == nil || len(req.Workflow.Bundles) != 1 {
		t.Fatal("generation mutated the frozen input")
	}
}

func TestOptionalReferenceOnlyCardsRemainReferencesWithoutInventedCitations(t *testing.T) {
	req, item := workflowFixture()
	delete(item, "knowledge_basis")
	raw, _ := json.Marshal([]any{item})
	req.Workflow.Version = knowledgebase.CurrentClinicalWorkflowVersion
	req.Workflow.Presentation = domain.StandardPresentation()
	req.Workflow.MaterialPolicy = knowledgebase.OptionalMaterialsPolicy
	req.Workflow.Bundles[0].Cards = []domain.KnowledgeCard{{ID: "custom-only", Kind: "custom", Status: "active", Revision: 2, Title: "参考表达", Content: domain.KnowledgeContent{CustomText: "仅在相应临床条件下参考的表达示例"}}}
	client := &workflowTestClient{response: string(raw)}
	questions, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.messages[1].Content, "仅在相应临床条件下参考的表达示例") || strings.Contains(client.messages[1].Content, "ordered_targets") || questions[0].GenerationContext != nil {
		t.Fatal("targetless references changed medical evidence contract")
	}
}

func TestOptionalExpressionOnlyCardAppliesIntentAndExplanationNote(t *testing.T) {
	req, item := workflowFixture()
	delete(item, "knowledge_basis")
	raw, _ := json.Marshal([]any{item})
	req.Workflow.Version = knowledgebase.ExpressionWorkflowVersion
	req.Workflow.Presentation = domain.StandardPresentation()
	req.Workflow.MaterialPolicy = knowledgebase.OptionalMaterialsPolicy
	req.Workflow.Bundles[0].Cards = []domain.KnowledgeCard{{ID: "pkc-expression-only", Kind: "custom", Status: "active", Revision: 2, Content: domain.KnowledgeContent{Expression: &domain.ExpressionRequirements{Instructions: "先描述患者主诉", ExplanationNote: "本例教学附言"}}}}
	c := &workflowTestClient{response: string(raw)}
	questions, err := NewService(c).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.messages[1].Content, "<author_expression_requirements>") || strings.Count(questions[0].Explanation, "本例教学附言") != 1 {
		t.Fatal("selected intent lost without a medical target")
	}
}
