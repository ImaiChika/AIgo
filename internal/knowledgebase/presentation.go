package knowledgebase

import (
	"fmt"
	"sort"

	"aigo/internal/domain"
)

const PresentationWorkflowVersion = "knowledge-v3"
const CurrentClinicalWorkflowVersion = "knowledge-v4"

func ValidateWorkflowPresentation(version string, p *domain.PresentationRequirements) error {
	if version == PresentationWorkflowVersion || version == CurrentClinicalWorkflowVersion || version == ExpressionWorkflowVersion {
		if p == nil {
			return fmt.Errorf("知识工作流缺少本次分组顺序")
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if (version == CurrentClinicalWorkflowVersion || version == ExpressionWorkflowVersion) && !domain.IsStandardPresentation(p) {
			return fmt.Errorf("当前新题须使用信息、主诉、现病史、既往史、查体、实验室、辅助检查、提问的固定顺序")
		}
		return nil
	}
	if p != nil {
		return fmt.Errorf("旧工作流不能附加新的分组执行设置")
	}
	return nil
}

// ApplyPresentation stably reorders existing facts only. Values, IDs, references,
// answer, personal prose and absent sections are not inferred or edited.
func ApplyPresentation(c *domain.QuestionGenerationContext) error {
	if c == nil {
		return fmt.Errorf("知识工作流依据缺失")
	}
	if err := ValidateWorkflowPresentation(c.WorkflowVersion, c.Presentation); err != nil {
		return err
	}
	if c.Presentation == nil {
		return nil
	}
	if c.Basis.CasePlan == nil {
		return fmt.Errorf("知识工作流缺少病例事实")
	}
	rank := presentationRanks(c.Presentation)
	facts := append([]domain.ClinicalFact(nil), c.Basis.CasePlan.Facts...)
	sort.SliceStable(facts, func(i, j int) bool { return rank[facts[i].Section] < rank[facts[j].Section] })
	c.Basis.CasePlan.Facts = facts
	return nil
}

func presentationRanks(p *domain.PresentationRequirements) map[string]int {
	rank := map[string]int{}
	for i, section := range p.SectionOrder {
		rank[section] = i
	}
	return rank
}

func presentationOrdered(c *domain.QuestionGenerationContext) bool {
	if c.Presentation == nil || c.Basis.CasePlan == nil {
		return false
	}
	rank := presentationRanks(c.Presentation)
	last := -1
	for _, fact := range c.Basis.CasePlan.Facts {
		i, ok := rank[fact.Section]
		if !ok || i < last {
			return false
		}
		last = i
	}
	return true
}

// Feedback describes the initial artifact's ordering, never the correctness of
// medicine or the fulfillment of other free-form instructions/current edits.
func PresentationFeedbackFor(c *domain.QuestionGenerationContext) *domain.PresentationFeedback {
	if c == nil || c.Presentation == nil {
		return nil
	}
	f := &domain.PresentationFeedback{State: "unverified", SectionOrder: append([]string(nil), c.Presentation.SectionOrder...), PresentSections: []string{}, UnusedSections: []string{}, FactIDs: []string{}}
	if (c.WorkflowVersion != PresentationWorkflowVersion && c.WorkflowVersion != CurrentClinicalWorkflowVersion && c.WorkflowVersion != ExpressionWorkflowVersion) || ValidateContext(c) != nil {
		return f
	}
	present := map[string]bool{}
	for _, fact := range c.Basis.CasePlan.Facts {
		present[fact.Section] = true
		f.FactIDs = append(f.FactIDs, fact.ID)
	}
	for _, section := range c.Presentation.SectionOrder {
		if present[section] {
			f.PresentSections = append(f.PresentSections, section)
		} else {
			f.UnusedSections = append(f.UnusedSections, section)
		}
	}
	f.State = "applied"
	if len(f.PresentSections) < 2 {
		f.State = "not_applicable"
	}
	return f
}
