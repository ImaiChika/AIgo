package knowledgebase

import (
	"aigo/internal/domain"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ValidateNarrative checks a bounded transport contract, not medical semantics.
func ValidateNarrative(p *domain.ClinicalPlan) error {
	if p == nil || strings.TrimSpace(p.Person) == "" || utf8.RuneCountInString(p.Person) > 100 || strings.TrimSpace(p.LeadIn) == "" || utf8.RuneCountInString(p.LeadIn) > 200 || len(p.Facts) == 0 || len(p.Facts) > 32 {
		return fmt.Errorf("病例信息/提问/事实数量格式无效")
	}
	raw, err := json.Marshal(p)
	if err != nil || utf8.RuneCount(raw) > 16000 {
		return fmt.Errorf("病例结构超过预算")
	}
	valid := map[string]bool{}
	for _, s := range domain.ClinicalSections() {
		valid[s] = true
	}
	seen := map[string]bool{"person": true}
	for _, f := range p.Facts {
		if !clinicalFactID.MatchString(f.ID) || seen[f.ID] || !valid[f.Section] || strings.TrimSpace(f.Text) == "" || utf8.RuneCountInString(f.Text) > 700 {
			return fmt.Errorf("病例事实编号、分组或内容无效")
		}
		seen[f.ID] = true
	}
	return nil
}

func OrderNarrative(p *domain.ClinicalPlan, requirements *domain.PresentationRequirements) {
	rank := presentationRanks(requirements)
	facts := append([]domain.ClinicalFact(nil), p.Facts...)
	sort.SliceStable(facts, func(i, j int) bool { return rank[facts[i].Section] < rank[facts[j].Section] })
	p.Facts = facts
}

func narrativeSentence(s string) string {
	s = strings.TrimSpace(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	if s != "" && !strings.ContainsRune("。；！？.!?;", last) {
		s += "。"
	}
	return s
}

func RenderClinicalStem(p *domain.ClinicalPlan) string {
	stem := narrativeSentence(p.Person)
	for _, f := range p.Facts {
		stem += narrativeSentence(f.Text)
	}
	return stem + strings.TrimSpace(p.LeadIn)
}
