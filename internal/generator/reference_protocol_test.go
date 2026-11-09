package generator

import (
	"encoding/json"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

func aliasFixture(t *testing.T) (domain.GenerationRequest, map[string]any) {
	t.Helper()
	req, item := clinicalWorkflowFixture()
	req.Workflow.ReferenceProtocol = knowledgebase.ShortReferenceProtocol
	return req, item
}
func TestReferenceProtocolMapsFrozenAliasesWithoutChangingEvidence(t *testing.T) {
	req, item := aliasFixture(t)
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	basis.FactRefs = []string{"k1"}
	basis.CasePlan.OptionEvidence[0].FactRefs = []string{"k1"}
	item["knowledge_basis"] = basis
	before, _ := json.Marshal(req)
	raw, _ := json.Marshal([]any{item})
	client := &workflowTestClient{response: string(raw)}
	questions, err := NewService(client).Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("frozen input mutated")
	}
	q := questions[0]
	if q.GenerationContext.Basis.FactRefs[0] != "card-one@2:f1" || q.GenerationContext.Basis.CasePlan.OptionEvidence[0].FactRefs[0] != "card-one@2:f1" {
		t.Fatal("alias not expanded to frozen evidence")
	}
	if err = knowledgebase.ValidateInitialQuestionContext(&q, q.GenerationContext); err != nil {
		t.Fatal(err)
	}
	prompt := client.messages[1].Content
	if !strings.Contains(prompt, `"id":"k1"`) || !strings.Contains(prompt, "子集") {
		t.Fatal("short references or explicit subset contract missing")
	}
	if client.calls != 1 {
		t.Fatal("extra model stage")
	}
}
func TestReferenceProtocolDoesNotGuessUnknownOrUnselectedFacts(t *testing.T) {
	for _, name := range []string{"unknown", "duplicate", "unselected", "legacy"} {
		t.Run(name, func(t *testing.T) {
			req, item := aliasFixture(t)
			basis := item["knowledge_basis"].(domain.KnowledgeBasis)
			switch name {
			case "unknown":
				basis.FactRefs = []string{"k99"}
			case "duplicate":
				basis.FactRefs = []string{"k1", "k1"}
			case "unselected":
				req.Workflow.Bundles[0].Cards[0].Content.Facts = append(req.Workflow.Bundles[0].Cards[0].Content.Facts, domain.KnowledgeFact{ID: "f2", Text: "另一条已知事实", AppliesWhen: "测试前提"})
				basis.CasePlan.OptionEvidence[1].FactRefs = []string{"k2"}
				basis.FactRefs = []string{"k1"}
			case "legacy":
				req, _ = clinicalWorkflowFixture()
				basis.FactRefs = []string{"k1"}
			}
			item["knowledge_basis"] = basis
			raw, _ := json.Marshal([]any{item})
			if _, err := NewService(&workflowTestClient{response: string(raw)}).Generate(t.Context(), req); err == nil {
				t.Fatal("invalid reference was repaired or allowed")
			}
		})
	}
}
