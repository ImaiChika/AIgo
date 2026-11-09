package generator

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage/testutil"
)

func clinicalWorkflowFixture() (domain.GenerationRequest, map[string]any) {
	req, item := workflowFixture()
	req.Workflow.Version = knowledgebase.WorkflowVersion
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	basis.Target = "模型改写的文字不决定目标"
	basis.TargetID = "t1"
	basis.CasePlan = testutil.ClinicalPlanFixture("A", "card-one@2:f1", "反复上腹痛2年，加重1天。")
	basis.CasePlan.OptionEvidence[0].CaseRefs = []string{"person", "c1"}
	basis.CasePlan.Facts = append(basis.CasePlan.Facts, domain.ClinicalFact{ID: "c2", Section: "lab_results", Text: "血红蛋白110g/L。"}, domain.ClinicalFact{ID: "c3", Section: "imaging_results", Text: "本例用于结构测试的影像描述。"})
	basis.CasePlan.PreferenceNotes = []string{"仅本次偏好：实验室在影像前，不作通用医学门槛。"}
	basis.CasePlan.OptionEvidence[0].Limitations = "仅内部核对的证据边界，不应自动加到公开解析。"
	item["knowledge_basis"] = basis
	item["clinical_stem"] = "患者，男，45岁。自由文本中的心电图正常不得进入最终题干。该患者最可能的诊断是"
	item["explanation"] = "自由解析也不是成题来源"
	return req, item
}

func TestClinicalWorkflowRendersCaseAndEvidenceInOneCall(t *testing.T) {
	req, item := clinicalWorkflowFixture()
	before, _ := json.Marshal(req)
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	questions, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	q := questions[0]
	if client.calls != 1 || q.WorkflowVersion != knowledgebase.WorkflowVersion || q.GenerationContext.Basis.Target != "测试诊断" {
		t.Fatal(client.calls, q)
	}
	if strings.Contains(q.ClinicalStem, "心电图") || strings.Contains(q.Explanation, "自由解析") || strings.Count(q.ClinicalStem, "反复上腹痛2年") != 1 {
		t.Fatal("free text escaped structured rendering", q)
	}
	if strings.Contains(q.Explanation, "仅内部核对") || !strings.Contains(q.GenerationContext.Basis.CasePlan.OptionEvidence[0].Limitations, "仅内部核对") {
		t.Fatal("internal limits lost or appended to public explanation")
	}
	if strings.Index(q.ClinicalStem, "110g/L") > strings.Index(q.ClinicalStem, "影像描述") {
		t.Fatal("per-run order changed")
	}
	if !strings.HasPrefix(q.Explanation, "正确答案为A。") || !strings.HasSuffix(q.Explanation, "故选A。") {
		t.Fatal(q.Explanation)
	}
	for _, label := range []string{"B", "C", "D", "E"} {
		if !strings.Contains(q.Explanation, label+"项") {
			t.Fatal("missing mapped distractor", label)
		}
	}
	if err := knowledgebase.ValidateInitialQuestionContext(&q, q.GenerationContext); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.messages[0].Content, "target_id") || !strings.Contains(client.messages[1].Content, `"id":"t1"`) {
		t.Fatal("missing v2 contract/target id")
	}
	public, _ := json.Marshal(q)
	if strings.Contains(string(public), "case_plan") || strings.Contains(string(public), "card-one@2:f1") {
		t.Fatal("private artifact leaked into DTO")
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("frozen request mutated")
	}
}

func TestInitialClinicalProtocolKeepsItsOriginalRendering(t *testing.T) {
	req, item := clinicalWorkflowFixture()
	req.Workflow.Version = knowledgebase.InitialClinicalWorkflowVersion
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	q, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q[0].Explanation, "仅内部核对") {
		t.Fatal("old accepted v2 snapshot changed rendering semantics")
	}
	if err := knowledgebase.ValidateInitialQuestionContext(&q[0], q[0].GenerationContext); err != nil {
		t.Fatal(err)
	}
}

func TestClinicalWorkflowRejectsBrokenMapsBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domain.KnowledgeBasis)
	}{
		{"missing plan", func(b *domain.KnowledgeBasis) { b.CasePlan = nil }},
		{"wrong target slot", func(b *domain.KnowledgeBasis) { b.TargetID = "t2" }},
		{"duplicate case id", func(b *domain.KnowledgeBasis) { b.CasePlan.Facts[1].ID = "c1" }},
		{"reserved case id", func(b *domain.KnowledgeBasis) { b.CasePlan.Facts[0].ID = "person" }},
		{"unknown section", func(b *domain.KnowledgeBasis) { b.CasePlan.Facts[0].Section = "tool_instruction" }},
		{"absent case evidence", func(b *domain.KnowledgeBasis) { b.CasePlan.OptionEvidence[0].CaseRefs = []string{"ecg-not-provided"} }},
		{"missing distractor", func(b *domain.KnowledgeBasis) { b.CasePlan.OptionEvidence = b.CasePlan.OptionEvidence[:4] }},
		{"invented fact", func(b *domain.KnowledgeBasis) { b.CasePlan.OptionEvidence[0].FactRefs = []string{"invented"} }},
		{"answer disagreement", func(b *domain.KnowledgeBasis) { b.CasePlan.Answer = "B" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, item := clinicalWorkflowFixture()
			basis := item["knowledge_basis"].(domain.KnowledgeBasis)
			tc.change(&basis)
			item["knowledge_basis"] = basis
			raw, _ := json.Marshal([]any{item})
			client := &workflowTestClient{response: string(raw)}
			if q, err := NewService(client).Generate(t.Context(), req); err == nil || len(q) != 0 || client.calls != 1 {
				t.Fatal("broken map accepted", q, err, client.calls)
			}
		})
	}
}

func TestClinicalWorkflowRetainsChosenOrderWithoutGlobalStyleGate(t *testing.T) {
	req, item := clinicalWorkflowFixture()
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	basis.CasePlan.Facts[1], basis.CasePlan.Facts[2] = basis.CasePlan.Facts[2], basis.CasePlan.Facts[1]
	basis.CasePlan.PreferenceNotes = nil
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	q, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(q[0].ClinicalStem, "影像描述") > strings.Index(q[0].ClinicalStem, "110g/L") {
		t.Fatal("renderer imposed a universal ordering rule")
	}
}

// A recorded live-model finding is replayed here to locate the responsibility:
// the renderer preserves supplied text; reference integrity is not a medical
// anatomy check. This is a diagnosis fixture, not approved clinical content.
func TestClinicalWorkflowPreservesModelSuppliedFinding(t *testing.T) {
	req, item := clinicalWorkflowFixture()
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	const finding = "躯干四肢多发隆起、边缘清楚的红色皮疹，双肺可闻及吸气性喉鸣"
	basis.CasePlan.Facts[1] = domain.ClinicalFact{ID: "c2", Section: "physical_findings", Text: finding}
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	questions, err := NewService(client).Generate(t.Context(), req)
	if err != nil || len(questions) != 1 {
		t.Fatal(questions, err)
	}
	q := questions[0]
	if !strings.Contains(q.ClinicalStem, finding) || q.GenerationContext.Basis.CasePlan.Facts[1].Text != finding || client.calls != 1 {
		t.Fatal("renderer inserted or corrected a medical claim", q, client.calls)
	}
}

func TestClinicalPromptHasOneCompleteOutputShapeForEverySlot(t *testing.T) {
	for _, count := range []int{1, 2, 5, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			req, _ := clinicalWorkflowFixture()
			req.Count = count
			base := buildPrompt(req.KnowledgePoints[0], count, false)
			user := clinicalUserOutputContract(base)
			if strings.Contains(user, `"answer": "正确答案标签"`) || !strings.Contains(user, "按下方本次病例证据协议") {
				t.Fatal("legacy wire example still competes")
			}
			addition, bundle, plans, err := knowledgeWorkflowPrompt(req)
			if err != nil {
				t.Fatal(err)
			}
			shape := strings.SplitN(strings.SplitN(addition, "<output_shape>\n", 2)[1], "\n</output_shape>", 2)[0]
			var items []map[string]any
			if err = json.Unmarshal([]byte(shape), &items); err != nil || len(items) != count {
				t.Fatal(len(items), err)
			}
			if strings.Count(addition, "<output_shape>") != 1 || strings.Contains(addition, "knowledge_basis示意：") {
				t.Fatal("multiple competing shapes")
			}
			for i, item := range items {
				if len(item) != 8 {
					t.Fatal("wrong top-level field count", len(item))
				}
				basis := item["knowledge_basis"].(map[string]any)
				if basis["target_id"] != plans[i].ID {
					t.Fatal("template shifted target slot")
				}
				plan := basis["case_plan"].(map[string]any)
				if len(plan["option_evidence"].([]any)) != 5 {
					t.Fatal("partial evidence example")
				}
				for _, ref := range basis["fact_refs"].([]any) {
					if _, ok := knowledgebase.FactIndex(bundle)[ref.(string)]; !ok {
						t.Fatal("invented example fact")
					}
				}
			}
		})
	}
	projected := knowledgeWorkflowSystemPrompt(getSystemPrompt(false), knowledgebase.WorkflowVersion)
	if strings.Contains(projected, `[{"clinical_stem"`) {
		t.Fatal("legacy seven-field JSON examples still present")
	}
	var examples []domain.A2Question
	for _, line := range strings.Split(getSystemPrompt(false), "\n") {
		if strings.HasPrefix(line, `[{`) {
			json.Unmarshal([]byte(line), &examples)
			for _, q := range examples {
				if !strings.Contains(projected, q.ClinicalStem) || !strings.Contains(projected, q.Explanation) {
					t.Fatal("clinical baseline content removed")
				}
			}
		}
	}
}

func TestClinicalResponseRejectsObservedProtocolFailures(t *testing.T) {
	for _, kind := range []string{"missing_basis", "wrong_notes_type", "missing_closing_object"} {
		t.Run(kind, func(t *testing.T) {
			req, item := clinicalWorkflowFixture()
			encoded, _ := json.Marshal([]any{item})
			var items []map[string]any
			json.Unmarshal(encoded, &items)
			switch kind {
			case "missing_basis":
				delete(items[0], "knowledge_basis")
			case "wrong_notes_type":
				items[0]["knowledge_basis"].(map[string]any)["case_plan"].(map[string]any)["preference_notes"] = "应为数组而不是字符串"
			}
			encoded, _ = json.Marshal(items)
			if kind == "missing_closing_object" {
				encoded = append(encoded[:len(encoded)-2], ']')
			}
			client := &workflowTestClient{response: string(encoded)}
			questions, err := NewService(client).Generate(t.Context(), req)
			if err == nil || len(questions) != 0 || client.calls != 1 {
				t.Fatal("observed damaged output accepted or silently repaired", questions, err, client.calls)
			}
		})
	}
}
