package knowledgebase

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aigo/internal/domain"
)

func TestExpressionValidationAndProvenance(t *testing.T) {
	card := domain.KnowledgeCard{ID: "pkc-owned", Kind: "custom", Title: "表达", KnowledgePoints: []domain.KnowledgePoint{{ID: "p", VersionID: "v"}}, Content: domain.KnowledgeContent{Expression: &domain.ExpressionRequirements{Instructions: "依据本例说明", ExplanationNote: "教学提示。"}}}
	if err := Validate(card, true); err != nil {
		t.Fatal(err)
	}
	if !IsExpressionCard(card) || len(ExpressionNotes(domain.KnowledgeBundle{Cards: []domain.KnowledgeCard{card, card}})) != 1 {
		t.Fatal("explicit provenance or deduplication wrong")
	}
	card.ID = "kc-base"
	if IsExpressionCard(card) {
		t.Fatal("base reference was promoted into a command")
	}
	card.OriginSubmissionID = "reviewed"
	if !IsExpressionCard(card) {
		t.Fatal("reviewed expression copy lost intent")
	}
	for _, change := range []func(*domain.KnowledgeCard){func(c *domain.KnowledgeCard) { c.Kind = "medical" }, func(c *domain.KnowledgeCard) { c.Content.Expression = &domain.ExpressionRequirements{} }, func(c *domain.KnowledgeCard) {
		c.Content.Expression = &domain.ExpressionRequirements{Instructions: strings.Repeat("字", 4001)}
	}, func(c *domain.KnowledgeCard) {
		c.Content.Expression = &domain.ExpressionRequirements{ExplanationNote: strings.Repeat("字", 161)}
	}} {
		copy := card
		change(&copy)
		if !errors.Is(Validate(copy, true), ErrInvalid) {
			t.Fatal("invalid expression accepted")
		}
	}
}

func TestExpressionContentRoundTrip(t *testing.T) {
	var card domain.KnowledgeCard
	if err := json.Unmarshal([]byte(`{"kind":"custom","content":{"custom_text":"参考原文","expression":{"instructions":"每个理由以依据开头","explanation_note":"教学提示：比较证据。"}}}`), &card); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"expression"`) || !strings.Contains(string(raw), "教学提示") {
		t.Fatal("explicit expression intent was discarded")
	}
}

func TestExpressionExplanationNotesRenderOnlyInNewWorkflow(t *testing.T) {
	var plan domain.ClinicalPlan
	if err := json.Unmarshal([]byte(`{"person":"患者，男，45岁。","answer":"A","lead_in":"最可能的诊断是","explanation_notes":["教学提示：比较证据。"],"option_evidence":[{"label":"A","reason":"诊断依据。"},{"label":"B","reason":"理由乙。"},{"label":"C","reason":"理由丙。"},{"label":"D","reason":"理由丁。"},{"label":"E","reason":"理由戊。"}]}`), &plan); err != nil {
		t.Fatal(err)
	}
	stem, old := RenderClinicalPlan(&plan, CurrentClinicalWorkflowVersion)
	newStem, next := RenderClinicalPlan(&plan, "knowledge-v5")
	if strings.Contains(old, "教学提示") {
		t.Fatal("old frozen rendering changed")
	}
	if newStem != stem || strings.Count(next, "教学提示：比较证据。") != 1 || !strings.HasSuffix(next, "教学提示：比较证据。故选A。") {
		t.Fatal("explicit note has no final rendering path", next)
	}
}
