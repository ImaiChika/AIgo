package knowledgebase

import (
	"aigo/internal/domain"
	"errors"
	"strings"
	"testing"
)

func validMedical() domain.KnowledgeCard {
	return domain.KnowledgeCard{Title: "测试资料", Kind: "medical", Source: "测试教材，仅测试", KnowledgePoints: []domain.KnowledgePoint{{ID: "point", VersionID: "version"}}, Content: domain.KnowledgeContent{ExamTargets: []string{"目标"}, Facts: []domain.KnowledgeFact{{ID: "fact", Text: "测试事实", AppliesWhen: "条件"}}}}
}
func TestKnowledgeCapabilities(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		p := []string{}
		for i, code := range []string{domain.PermKBView, domain.PermKBManage, domain.PermKBUse} {
			if mask&(1<<i) != 0 {
				p = append(p, code)
			}
		}
		c := CapabilitiesFor(p)
		if c.View != (mask&1 != 0) || c.Manage != (mask&3 == 3) || c.Use != (mask&5 == 5) {
			t.Fatalf("mask %d %+v", mask, c)
		}
	}
}
func TestKnowledgePublicationValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*domain.KnowledgeCard)
	}{
		{"missing source", func(c *domain.KnowledgeCard) { c.Source = "" }}, {"missing targets", func(c *domain.KnowledgeCard) { c.Content.ExamTargets = nil }}, {"missing facts", func(c *domain.KnowledgeCard) { c.Content.Facts = nil }}, {"duplicate fact IDs", func(c *domain.KnowledgeCard) { c.Content.Facts = append(c.Content.Facts, c.Content.Facts[0]) }}, {"unbound", func(c *domain.KnowledgeCard) { c.KnowledgePoints = nil }}, {"version missing", func(c *domain.KnowledgeCard) { c.KnowledgePoints[0].VersionID = "" }}, {"empty reference", func(c *domain.KnowledgeCard) { c.Kind = "exam_reference" }}, {"empty custom", func(c *domain.KnowledgeCard) { c.Kind = "custom" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validMedical()
			tc.change(&c)
			if err := Validate(c, true); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid active card: %v", err)
			}
		})
	}
	if e := Validate(validMedical(), true); e != nil {
		t.Fatal(e)
	}
	c := validMedical()
	c.Source = ""
	c.Content.Facts = nil
	if e := Validate(c, false); e != nil {
		t.Fatal("draft must allow incomplete sources/facts", e)
	}
}

func TestReferenceYearUsesUnicodeCharacterLimit(t *testing.T) {
	for _, tc := range []struct {
		name, year string
		valid      bool
	}{{"forty_ascii", strings.Repeat("a", 40), true}, {"forty_chinese", strings.Repeat("年", 40), true}, {"reported_caption", "2026（模拟试题解析，非历年真题）", true}, {"over_limit", strings.Repeat("年", 41), false}} {
		t.Run(tc.name, func(t *testing.T) {
			card := validMedical()
			card.Kind = "exam_reference"
			card.Content.Reference = &domain.ExamReference{Year: tc.year, Stem: "参考病例", Options: []domain.Option{{Label: "A", Text: "选项"}}, Answer: "A"}
			e := Validate(card, true)
			if tc.valid && e != nil {
				t.Fatal(e)
			}
			if !tc.valid && (e == nil || !strings.Contains(e.Error(), "年份")) {
				t.Fatal("year validation must identify its field", e)
			}
		})
	}
}
