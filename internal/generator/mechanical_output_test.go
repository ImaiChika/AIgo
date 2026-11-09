package generator

import (
	"encoding/json"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage/testutil"
)

func delegatedFixture() (domain.GenerationRequest, map[string]any) {
	req, item := workflowFixture()
	req.Workflow.Version = knowledgebase.CurrentClinicalWorkflowVersion
	req.Workflow.Presentation = domain.StandardPresentation()
	req.PromptSnapshot = FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
	item["knowledge_basis"] = domain.KnowledgeBasis{ConditionNotes: "only the stated clinical conditions", CasePlan: testutil.ClinicalPlanFixture("A", "card-one@2:f1", "本例反复上腹痛2年，加重1天。")}
	return req, item
}
func TestDelegatedKnowledgeMetadataDoesNotRegenerateAValidQuestion(t *testing.T) {
	req, item := delegatedFixture()
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	for i := range basis.CasePlan.Facts {
		basis.CasePlan.Facts[i].ID = ""
	}
	for i := range basis.CasePlan.OptionEvidence {
		basis.CasePlan.OptionEvidence[i].Label = ""
	}
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	got, err := NewService(client).Generate(t.Context(), req)
	if err != nil || len(got) != 1 {
		t.Fatal("mechanical omission discarded the complete clinical answer", err)
	}
	c := got[0].GenerationContext
	if c.Target.ID != "t1" || c.Basis.TargetID != "t1" || len(c.Basis.FactRefs) != 1 || c.Basis.AnswerSupport != c.Basis.CasePlan.OptionEvidence[0].Reason || client.calls != 1 {
		t.Fatal("canonical evidence not assembled", c)
	}
	if err := knowledgebase.ValidateContext(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0].ClinicalStem, "反复上腹痛2年") {
		t.Fatal("clinical text changed")
	}
}

func TestDelegatedMetadataNeverRepairsMedicalEvidence(t *testing.T) {
	for _, name := range []string{"wrong_target", "unknown_reference", "missing_correct_reference", "wrong_case_reference", "answer_mismatch"} {
		t.Run(name, func(t *testing.T) {
			req, item := delegatedFixture()
			basis := item["knowledge_basis"].(domain.KnowledgeBasis)
			switch name {
			case "wrong_target":
				basis.TargetID = "t2"
			case "unknown_reference":
				basis.CasePlan.OptionEvidence[0].FactRefs = []string{"invented-medical-fact"}
			case "missing_correct_reference":
				basis.CasePlan.OptionEvidence[0].FactRefs = nil
			case "wrong_case_reference":
				basis.CasePlan.OptionEvidence[0].CaseRefs = []string{"missing-case"}
			case "answer_mismatch":
				basis.CasePlan.Answer = "B"
			}
			item["knowledge_basis"] = basis
			raw, _ := json.Marshal([]any{item})
			client := &workflowTestClient{response: string(raw)}
			if _, err := NewService(client).Generate(t.Context(), req); err == nil {
				t.Fatal("medical relation was guessed or contradictory evidence accepted")
			}
		})
	}
}
func TestDelegatedExplanationOnlyAddsKnownBoilerplate(t *testing.T) {
	body := "本例A符合已有证据。B项不符；C项不符；D项不符；E项不符。"
	got, err := assembleExplanation(body, "A")
	if err != nil || got != "正确答案为A。"+body+"故选A。" {
		t.Fatal(got, err)
	}
	same, err := assembleExplanation(got, "A")
	if err != nil || same != got {
		t.Fatal("duplicate wrappers", same, err)
	}
	for _, text := range []string{"正确答案为B。" + body, body + "故选B。"} {
		if _, err := assembleExplanation(text, "A"); err == nil {
			t.Fatal("conflicting answer replaced")
		}
	}
}
func TestLegacyMetadataOmissionRemainsRejected(t *testing.T) {
	req, item := delegatedFixture()
	req.PromptSnapshot.OutputPolicy = ""
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	if _, err := NewService(client).Generate(t.Context(), req); err == nil {
		t.Fatal("old persisted protocol was silently widened")
	}
}

func TestDelegatedAssemblyPreservesOriginalPromptsAndValidClinicalEvidence(t *testing.T) {
	req, item := delegatedFixture()
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	basis.TargetID = "t1"
	basis.FactRefs = []string{"card-one@2:f1"}
	basis.AnswerSupport = basis.CasePlan.OptionEvidence[0].Reason
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	current, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := json.Marshal(client.messages)
	req.PromptSnapshot.OutputPolicy = ""
	baseline, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	originalMessages, _ := json.Marshal(client.messages)
	if string(messages) != string(originalMessages) {
		t.Fatal("mechanical assembly rewrote baseline model instructions")
	}
	a, b := current[0], baseline[0]
	if a.ClinicalStem != b.ClinicalStem || a.Explanation != b.Explanation || a.Answer != b.Answer {
		t.Fatal("valid original clinical output changed")
	}
	refsA, _ := json.Marshal(a.GenerationContext)
	refsB, _ := json.Marshal(b.GenerationContext)
	if string(refsA) != string(refsB) {
		t.Fatal("valid original evidence changed")
	}
}

func TestDelegatedReferenceDeduplicationKeepsOnlyExplicitEvidence(t *testing.T) {
	req, item := delegatedFixture()
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	first := &basis.CasePlan.OptionEvidence[0]
	first.FactRefs = append(first.FactRefs, first.FactRefs...)
	first.CaseRefs = append(first.CaseRefs, first.CaseRefs...)
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	questions, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	actual := questions[0].GenerationContext.Basis.CasePlan.OptionEvidence[0]
	if len(actual.FactRefs) != 1 || len(actual.CaseRefs) != 1 || actual.Reason != first.Reason {
		t.Fatal("explicit evidence changed", actual)
	}
}

func TestDelegatedLabelsDoNotOverwriteExplicitWrongTypes(t *testing.T) {
	req, item := delegatedFixture()
	item["options"] = []map[string]any{{"label": 5, "text": "explicit option"}}
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	if _, err := NewService(client).Generate(t.Context(), req); err == nil {
		t.Fatal("explicit numeric label was mistaken for a missing label")
	}
}

func TestDelegatedPartialLabelsRejectAmbiguityAndPreserveExplicitOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		labels  []string
		want    []string
		invalid bool
	}{
		{"missing_fixed_slots", []string{"A", "", "C", "", "E"}, []string{"A", "B", "C", "D", "E"}, false},
		{"explicit_reordered", []string{"B", "A", "C", "D", "E"}, []string{"B", "A", "C", "D", "E"}, false},
		{"ambiguous_partial_order", []string{"B", "", "C", "D", "E"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := make([]domain.Option, len(tc.labels))
			for i, label := range tc.labels {
				options[i] = domain.Option{Label: label, Text: "original clinical option"}
			}
			err := fillOptionLabels(options)
			if tc.invalid {
				if err == nil {
					t.Fatal("ambiguous correspondence was guessed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, option := range options {
				if option.Label != tc.want[i] || option.Text != "original clinical option" {
					t.Fatal("explicit order or clinical text changed", options)
				}
			}
		})
	}
}

func TestFrozenGenerationTemperatureChangesOnlySampling(t *testing.T) {
	req, item := delegatedFixture()
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	basis.TargetID = "t1"
	basis.FactRefs = []string{"card-one@2:f1"}
	basis.AnswerSupport = basis.CasePlan.OptionEvidence[0].Reason
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	client := &MockClient{Response: string(raw)}
	svc := NewService(client)
	var original string
	for _, temperature := range []float64{0.4, 0.6, 0} {
		req.PromptSnapshot.GenerationTemperature = &temperature
		if _, err := svc.Generate(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		messages, _ := json.Marshal(client.LastMessages)
		if original == "" {
			original = string(messages)
		}
		if original != string(messages) || client.LastOptions.Temperature != temperature || client.LastOptions.TemperatureSet != (temperature == 0) {
			t.Fatal("temperature changed prompt or zero not explicit")
		}
	}
}

func TestDelegatedFactIDCollisionIsRejectedInsteadOfRebindingEvidence(t *testing.T) {
	req, item := delegatedFixture()
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	first := basis.CasePlan.Facts[0]
	first.ID = "c2"
	second := first
	second.ID, second.Text = "", "another original clinical observation"
	basis.CasePlan.Facts = []domain.ClinicalFact{first, second}
	for i := range basis.CasePlan.OptionEvidence {
		basis.CasePlan.OptionEvidence[i].CaseRefs = []string{"c2"}
	}
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	if _, err := NewService(&workflowTestClient{response: string(raw)}).Generate(t.Context(), req); err == nil {
		t.Fatal("colliding clinical fact IDs were accepted")
	}
	// The otherwise identical valid control must succeed: rejection must not be
	// caused by an unrelated dangling case reference in the fixture.
	basis.CasePlan.Facts[1].ID = "c3"
	item["knowledge_basis"] = basis
	raw, _ = json.Marshal([]any{item})
	if _, err := NewService(&workflowTestClient{response: string(raw)}).Generate(t.Context(), req); err != nil {
		t.Fatal("non-colliding control failed", err)
	}
}
