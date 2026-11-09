package generator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

func TestExplicitPresentationReordersFactsWithoutChangingThem(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy unchanged", true: "explicit v3"}[explicit], func(t *testing.T) {
			req, item := clinicalWorkflowFixture()
			basis := item["knowledge_basis"].(domain.KnowledgeBasis)
			basis.CasePlan.Facts = []domain.ClinicalFact{
				{ID: "c3", Section: "imaging_results", Text: "用于结构验收的胸片结果。"},
				{ID: "c2", Section: "lab_results", Text: "血红蛋白110g/L。"},
				{ID: "c1", Section: "chief_complaint", Text: "反复上腹痛2年，加重1天。"},
				{ID: "c4", Section: "lab_results", Text: "另一项合成检验值4.2mmol/L。"},
			}
			original := append([]domain.ClinicalFact(nil), basis.CasePlan.Facts...)
			if explicit {
				req.Workflow.Version = knowledgebase.PresentationWorkflowVersion
				req.Workflow.Presentation = &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
			}
			item["knowledge_basis"] = basis
			raw, _ := json.Marshal([]any{item})
			before, _ := json.Marshal(req)
			client := &workflowTestClient{response: string(raw)}
			questions, err := NewService(client).Generate(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			q := questions[0]
			facts := q.GenerationContext.Basis.CasePlan.Facts
			want := original
			if explicit {
				want = []domain.ClinicalFact{original[2], original[1], original[3], original[0]}
			}
			if !reflect.DeepEqual(facts, want) {
				t.Fatal("facts/IDs or stable within-group ordering changed", facts)
			}
			if client.calls != 1 || !strings.Contains(q.ClinicalStem, "110g/L") || !strings.Contains(q.ClinicalStem, "4.2mmol/L") || q.Answer != "A" {
				t.Fatal("extra call or content mutation")
			}
			feedback := knowledgebase.PresentationFeedbackFor(q.GenerationContext)
			if explicit {
				if feedback == nil || feedback.State != "applied" || len(feedback.PresentSections) != 3 || len(feedback.UnusedSections) != 5 {
					t.Fatal(feedback)
				}
			} else if feedback != nil {
				t.Fatal("legacy invented a presentation requirement")
			}
			after, _ := json.Marshal(req)
			if string(after) != string(before) {
				t.Fatal("frozen request mutated")
			}
			if err = knowledgebase.ValidateInitialQuestionContext(&q, q.GenerationContext); err != nil {
				t.Fatal(err)
			}
			if explicit {
				facts[0], facts[3] = facts[3], facts[0]
				if knowledgebase.ValidateContext(q.GenerationContext) == nil || knowledgebase.PresentationFeedbackFor(q.GenerationContext).State != "unverified" {
					t.Fatal("corrupt ordering falsely satisfied")
				}
			}
		})
	}
}

func TestPresentationVersionAndMissingSectionsDoNotChangeMedicalRules(t *testing.T) {
	req, item := clinicalWorkflowFixture()
	req.Workflow.Version = knowledgebase.PresentationWorkflowVersion
	req.Workflow.Presentation = &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	basis.CasePlan.Facts = basis.CasePlan.Facts[:1]
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	qs, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f := knowledgebase.PresentationFeedbackFor(qs[0].GenerationContext)
	if f.State != "not_applicable" || len(qs[0].GenerationContext.Basis.CasePlan.Facts) != 1 {
		t.Fatal("absent groups were filled or required", f)
	}
	for _, version := range []string{knowledgebase.LegacyWorkflowVersion, knowledgebase.InitialClinicalWorkflowVersion, knowledgebase.WorkflowVersion} {
		req.Workflow.Version = version
		before := client.calls
		if _, err = NewService(client).Generate(t.Context(), req); err == nil || client.calls != before {
			t.Fatal("new choice attached to legacy snapshot or called model", version)
		}
	}
	req.Workflow.Version = knowledgebase.PresentationWorkflowVersion
	req.Workflow.Presentation = nil
	before := client.calls
	if _, err = NewService(client).Generate(t.Context(), req); err == nil || client.calls != before {
		t.Fatal("missing v3 setting called model")
	}
}
