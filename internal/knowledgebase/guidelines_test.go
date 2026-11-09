package knowledgebase

import (
	"aigo/internal/domain"
	"testing"
)

func TestGlobalMethodIsSeparateFromMedicalAndPointScope(t *testing.T) {
	card := domain.KnowledgeCard{Kind: GuidelineKind, Title: "同一决策维度", Source: "培训第45/49页", Content: domain.KnowledgeContent{CustomText: "选项不得互相包容。"}}
	if e := Validate(card, true); e != nil {
		t.Fatal(e)
	}
	for _, modify := range []func(*domain.KnowledgeCard){func(c *domain.KnowledgeCard) { c.KnowledgePoints = []domain.KnowledgePoint{{ID: "p", VersionID: "v"}} }, func(c *domain.KnowledgeCard) {
		c.Content.Facts = []domain.KnowledgeFact{{ID: "f", Text: "医学事实"}}
	}, func(c *domain.KnowledgeCard) { c.Source = "" }, func(c *domain.KnowledgeCard) { c.Content.Reference = &domain.ExamReference{Stem: "病例"} }} {
		copy := card
		modify(&copy)
		if e := Validate(copy, true); e == nil {
			t.Fatal("accepted mixed/unattributed global method", copy)
		}
	}
}
