package testutil

import "aigo/internal/domain"

// ClinicalPlanFixture is synthetic contract-test data, never a medical source.
func ClinicalPlanFixture(answer, factRef, complaint string) *domain.ClinicalPlan {
	p := &domain.ClinicalPlan{Person: "患者，男，45岁。", Facts: []domain.ClinicalFact{{ID: "c1", Section: "chief_complaint", Text: complaint}}, LeadIn: "该患者最可能的诊断是", Answer: answer}
	for _, label := range []string{"A", "B", "C", "D", "E"} {
		e := domain.OptionEvidence{Label: label, CaseRefs: []string{"c1"}, Reason: "仅用于自动化的病例关联说明，不作医学结论。"}
		if label == answer {
			e.FactRefs = []string{factRef}
		}
		p.OptionEvidence = append(p.OptionEvidence, e)
	}
	return p
}
