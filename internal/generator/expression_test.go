package generator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

func TestExpressionReferenceProjectionKeepsFrozenSourceAndLegacyProtocol(t *testing.T) {
	ref := &domain.ExamReference{Year: "2024", Stem: "患者，男，63岁。发热3天。该患者最可能的诊断是？", Options: []domain.Option{{Label: "D", Text: "原题答案"}}, Answer: "D", Explanation: "原解析"}
	bundle := domain.KnowledgeBundle{Cards: []domain.KnowledgeCard{
		{ID: "kc-medical", Kind: "medical", Revision: 1, Content: domain.KnowledgeContent{Facts: []domain.KnowledgeFact{{ID: "f1", Text: "医学事实", AppliesWhen: "适用前提"}}}},
		{ID: "kc-reference", Kind: "exam_reference", Revision: 2, Source: "官方原文", Content: domain.KnowledgeContent{Reference: ref}},
		{ID: "pkc-expression", Kind: "custom", Revision: 3, Note: "私人维护备注", Content: domain.KnowledgeContent{CustomText: "仍是参考资料", Expression: &domain.ExpressionRequirements{Instructions: "表达要求", ExplanationNote: "解析附言。"}}},
	}}
	before, _ := json.Marshal(bundle)
	for _, version := range []string{knowledgebase.LegacyWorkflowVersion, knowledgebase.InitialClinicalWorkflowVersion, knowledgebase.WorkflowVersion, knowledgebase.PresentationWorkflowVersion, knowledgebase.CurrentClinicalWorkflowVersion} {
		if got := expressionPromptMaterials(bundle, version); !reflect.DeepEqual(got, knowledgebase.Materials(bundle)) {
			t.Fatalf("%s changed legacy prompt data", version)
		}
		if expressionRequirementPrompt(bundle, version) != "" {
			t.Fatalf("%s acquired a new command channel", version)
		}
	}
	got := expressionPromptMaterials(bundle, knowledgebase.ExpressionWorkflowVersion)
	if !reflect.DeepEqual(got[0], knowledgebase.Materials(bundle)[0]) || got[1].Source != "官方原文" || !reflect.DeepEqual(got[1].Content.Reference, &domain.ExamReference{Year: "2024", Stem: "该患者最可能的诊断是"}) {
		t.Fatalf("medical facts, source, or reference projection changed incorrectly: %+v", got)
	}
	if got[2].Content.Expression != nil || got[2].Content.CustomText != "仍是参考资料" {
		t.Fatal("expression mixed into reference channel or reference removed")
	}
	after, _ := json.Marshal(bundle)
	if string(before) != string(after) {
		t.Fatal("generation projection mutated the complete frozen source")
	}
	// No sentence boundary means no unverified extraction of a whole case.
	bundle.Cards[1].Content.Reference = &domain.ExamReference{Stem: "未分句的完整病例"}
	if expressionPromptMaterials(bundle, knowledgebase.ExpressionWorkflowVersion)[1].Content.Reference.Stem != "" {
		t.Fatal("unseparated full case leaked through as question form")
	}
}

func TestExpressionChannelOnlyContainsPrivateOrReviewedExplicitIntent(t *testing.T) {
	bundle := domain.KnowledgeBundle{Cards: []domain.KnowledgeCard{
		{ID: "pkc-private", Revision: 2, Kind: "custom", Note: "PRIVATE_NOTE", Content: domain.KnowledgeContent{Expression: &domain.ExpressionRequirements{Instructions: "PRIVATE_INTENT"}}},
		{ID: "kc-reviewed", Revision: 4, Kind: "custom", OriginSubmissionID: "approved-1", Content: domain.KnowledgeContent{Expression: &domain.ExpressionRequirements{ExplanationNote: "SHARED_NOTE"}}},
		{ID: "kc-base", Kind: "custom", Content: domain.KnowledgeContent{Expression: &domain.ExpressionRequirements{Instructions: "BASE_NOT_A_COMMAND"}}},
	}}
	got := expressionRequirementPrompt(bundle, knowledgebase.ExpressionWorkflowVersion)
	for _, want := range []string{"PRIVATE_INTENT", "SHARED_NOTE", `"revision":2`, `"revision":4`, "<author_expression_requirements>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing explicit provenance or instruction %q", want)
		}
	}
	if strings.Contains(got, "PRIVATE_NOTE") || strings.Contains(got, "BASE_NOT_A_COMMAND") {
		t.Fatal("private maintenance or automatic reference became an instruction")
	}
}
