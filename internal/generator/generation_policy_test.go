package generator

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentPolicyDirectRenderingAndFrozenCompatibility(t *testing.T) {
	req, item := workflowFixture()
	req.Workflow = nil
	delete(item, "knowledge_basis")
	item["case_plan"] = &domain.ClinicalPlan{Person: "患者，男，45岁。", Facts: []domain.ClinicalFact{{ID: "lab", Section: "lab_results", Text: "Hb 110g/L"}, {ID: "complaint", Section: "chief_complaint", Text: "上腹痛2年"}, {ID: "image", Section: "imaging_results", Text: "腹部超声未见明显异常"}, {ID: "past", Section: "past_history", Text: "既往有上腹部不适"}}, LeadIn: "该患者最可能的诊断是"}
	req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
	raw, _ := json.Marshal([]any{item})
	client := &MockClient{Response: string(raw)}
	questions, err := NewService(client).Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	stem := questions[0].ClinicalStem
	if !(strings.Index(stem, "上腹痛") < strings.Index(stem, "既往") && strings.Index(stem, "既往") < strings.Index(stem, "Hb") && strings.Index(stem, "Hb") < strings.Index(stem, "超声")) {
		t.Fatal(stem)
	}
	if questions[0].GenerationContext != nil || questions[0].WorkflowVersion != "" {
		t.Fatal("direct acquired a knowledge dependency")
	}
	if !strings.Contains(client.LastMessages[0].Content, "遮挡选项") || !strings.Contains(client.LastMessages[1].Content, "<direct_case_output>") {
		t.Fatal("new policy not delivered")
	}
	req.PromptSnapshot.PolicyVersion = ""
	req.PromptSnapshot.Appendix = "\n旧要求"
	req.PromptSnapshot.SystemPrompt = "旧任务冻结底座"
	if _, err = NewService(client).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if client.LastMessages[0].Content != "旧任务冻结底座" || strings.Contains(client.LastMessages[1].Content, "<direct_case_output>") {
		t.Fatal("legacy task upgraded")
	}
}

func TestFocusedGenerationKeepsTaskEvidenceAndFrozenV4(t *testing.T) {
	req, item := workflowFixture()
	req.Workflow = nil
	delete(item, "knowledge_basis")
	item["case_plan"] = map[string]any{"person": "患者，男，45岁。", "chief_complaint": "上腹痛2年", "lead_in": "该患者最可能的诊断是"}
	req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
	raw, _ := json.Marshal([]any{item})
	client := &MockClient{Response: string(raw)}
	questions, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if questions[0].FormatVersion != "a2-2026-10-context-v2" || req.PromptSnapshot.PolicyVersion != "a2-clinical-20261024-v5" {
		t.Fatal("new task lost its version")
	}
	for _, want := range []string{"【本题构造顺序】", "考核任务", "病例事实", "一般医学知识", "不必输出规划"} {
		if !strings.Contains(client.LastMessages[0].Content, want) {
			t.Fatal("focused instructions missing", want)
		}
	}
	req.PromptSnapshot.PolicyVersion = "a2-clinical-20261023-v4"
	req.PromptSnapshot.SystemPrompt = fixedFieldSystemPrompt()
	questions, err = NewService(client).Generate(t.Context(), req)
	wantOldSystem := strings.Replace(fixedFieldSystemPrompt(), "每个元素只能包含 clinical_stem、options、answer、explanation、difficulty、cognitive_level、exam_points 七个字段。", "每个元素只能包含 clinical_stem、options、answer、explanation、difficulty、cognitive_level、exam_points、case_plan 八个字段。", 1)
	if err != nil || questions[0].FormatVersion != domain.A2FormatContextual || !strings.HasPrefix(client.LastMessages[0].Content, wantOldSystem) || req.PromptSnapshot.SystemPrompt != fixedFieldSystemPrompt() {
		t.Fatal("accepted v4 task changed", err)
	}
	if strings.Contains(client.LastMessages[0].Content, "【本题构造顺序】") || !strings.Contains(client.LastMessages[1].Content, "不用数组、编号或分组名") {
		t.Fatal("old prompt or fixed transport upgraded")
	}
}

func TestGenerationRejectsAllExcludedSnapshotPointsWithoutModelCall(t *testing.T) {
	for _, category := range []string{"基础医学", "临床综合"} {
		for _, policy := range []string{"", domain.InitialA2GenerationPolicy, domain.CurrentA2GenerationPolicy} {
			t.Run(category+"/"+policy, func(t *testing.T) {
				req, _ := workflowFixture()
				req.Workflow = nil
				req.KnowledgePoints = []domain.KnowledgePoint{{Category: category, Subject: "药理", Topic: "旧任务考点"}}
				if policy != "" {
					req.PromptSnapshot = &domain.GenerationPromptSnapshot{PolicyVersion: policy}
				}
				client := &MockClient{Response: "[]"}
				_, err := NewService(client).Generate(t.Context(), req)
				if !domain.IsBasicMedicalScopeError(err) || len(client.LastMessages) != 0 {
					t.Fatalf("excluded snapshot must stop before calling model: err=%v calls=%d", err, len(client.LastMessages))
				}
			})
		}
	}
}

func TestGenerationRejectsExcludedSecondaryPoint(t *testing.T) {
	req, _ := workflowFixture()
	req.Workflow = nil
	req.KnowledgePoints = append(req.KnowledgePoints, domain.KnowledgePoint{Category: "临床综合", Subject: "药理", Topic: "第二个矛盾考点"})
	client := &MockClient{Response: "[]"}
	if _, err := NewService(client).Generate(t.Context(), req); !domain.IsBasicMedicalScopeError(err) || len(client.LastMessages) != 0 {
		t.Fatalf("secondary excluded point reached model: %v", err)
	}
}

func TestCurrentPolicyRequiresNarrativeAndRejectsBasicBeforeCalling(t *testing.T) {
	for _, name := range []string{"missing_narrative", "basic_point", "unknown_policy"} {
		t.Run(name, func(t *testing.T) {
			req, item := workflowFixture()
			req.Workflow = nil
			delete(item, "knowledge_basis")
			req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
			switch name {
			case "basic_point":
				req.KnowledgePoints[0].Category = "基础医学"
			case "unknown_policy":
				req.PromptSnapshot.PolicyVersion = "unknown"
			}
			raw, _ := json.Marshal([]any{item})
			client := &MockClient{Response: string(raw)}
			if _, err := NewService(client).Generate(context.Background(), req); err == nil {
				t.Fatal("invalid new request accepted")
			}
			if name != "missing_narrative" && len(client.LastMessages) > 0 {
				t.Fatal("invalid eligibility/policy called model")
			}
		})
	}
}

func TestReferenceReminderIsVersionedAndDoesNotCreateFacts(t *testing.T) {
	for _, version := range []string{domain.InitialA2GenerationPolicy, domain.ReferenceA2GenerationPolicy, domain.SemanticA2GenerationPolicy, domain.FixedFieldA2GenerationPolicy, domain.CurrentA2GenerationPolicy} {
		t.Run(version, func(t *testing.T) {
			req, _ := clinicalWorkflowFixture()
			req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
			req.PromptSnapshot.PolicyVersion = version
			before, _ := json.Marshal(req.Workflow)
			prompt, bundle, _, err := knowledgeWorkflowPrompt(req)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(prompt, "answer_fact_candidates") != domain.UsesAnswerReferenceReminder(version) {
				t.Fatal("old prompt upgraded or current references missing")
			}
			after, _ := json.Marshal(req.Workflow)
			if string(before) != string(after) {
				t.Fatal("reminder mutated frozen facts")
			}
			if version == domain.CurrentA2GenerationPolicy && !strings.Contains(prompt, knowledgebase.FactRef(bundle.Cards[0], bundle.Cards[0].Content.Facts[0])) {
				t.Fatal("reminder invented references")
			}
		})
	}
}

func TestCurrentPolicyFixedFieldCasePlanRendersInOrder(t *testing.T) {
	req, item := workflowFixture()
	req.Workflow = nil
	delete(item, "knowledge_basis")
	item["case_plan"] = map[string]any{
		"person":          "患者，男，45岁。",
		"lab_results":     "Hb 110g/L",
		"chief_complaint": "上腹痛2年",
		"past_history":    "既往有上腹部不适",
		"lead_in":         "该患者最可能的诊断是",
	}
	req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
	if req.PromptSnapshot.PolicyVersion != domain.CurrentA2GenerationPolicy || !domain.UsesFixedFieldCasePlan(req.PromptSnapshot.PolicyVersion) {
		t.Fatal("current policy not frozen")
	}
	raw, _ := json.Marshal([]any{item})
	client := &MockClient{Response: string(raw)}
	questions, err := NewService(client).Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	stem := questions[0].ClinicalStem
	if !(strings.Index(stem, "上腹痛") < strings.Index(stem, "既往") && strings.Index(stem, "既往") < strings.Index(stem, "Hb") && strings.HasSuffix(stem, "该患者最可能的诊断是")) {
		t.Fatal(stem)
	}
	if !strings.Contains(client.LastMessages[1].Content, "不用数组、编号或分组名") {
		t.Fatal("fixed-field contract not delivered")
	}
}

func TestFixedFieldCasePlanRejectsUnknownKeyAndNonString(t *testing.T) {
	req, item := workflowFixture()
	req.Workflow = nil
	delete(item, "knowledge_basis")
	req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
	for name, plan := range map[string]map[string]any{
		"unknown_key": {"person": "患者，男，45岁。", "chief_complaint": "上腹痛2年", "lead_in": "该患者最可能的诊断是", "diagnosis": "胃溃疡"},
		"array_value": {"person": "患者，男，45岁。", "chief_complaint": []any{"上腹痛2年"}, "lead_in": "该患者最可能的诊断是"},
		"no_section":  {"person": "患者，男，45岁。", "lead_in": "该患者最可能的诊断是"},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := map[string]any{}
			for k, v := range item {
				mutated[k] = v
			}
			mutated["case_plan"] = plan
			raw, _ := json.Marshal([]any{mutated})
			if _, err := NewService(&MockClient{Response: string(raw)}).Generate(context.Background(), req); err == nil {
				t.Fatal("structurally invalid fixed-field plan accepted")
			}
		})
	}
}

func TestEarlierPolicySnapshotsKeepFactsArrayTransport(t *testing.T) {
	req, item := workflowFixture()
	req.Workflow = nil
	delete(item, "knowledge_basis")
	item["case_plan"] = map[string]any{"person": "患者，男，45岁。", "facts": []domain.ClinicalFact{{ID: "c1", Section: "chief_complaint", Text: "上腹痛2年"}}, "lead_in": "该患者最可能的诊断是"}
	req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
	req.PromptSnapshot.PolicyVersion = domain.SemanticA2GenerationPolicy
	raw, _ := json.Marshal([]any{item})
	client := &MockClient{Response: string(raw)}
	if _, err := NewService(client).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(client.LastMessages[1].Content, "不用数组、编号或分组名") {
		t.Fatal("old snapshot upgraded to fixed-field contract")
	}
}
