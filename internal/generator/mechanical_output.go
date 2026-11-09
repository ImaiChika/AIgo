package generator

import (
	"fmt"
	"regexp"
	"strings"

	"aigo/internal/domain"
)

func delegatedOutput(req domain.GenerationRequest) bool {
	return req.PromptSnapshot != nil && req.PromptSnapshot.OutputPolicy == domain.DelegatedGenerationOutput
}

// Fill only predetermined array slots. Explicit conflicting labels are not
// overwritten; mixing unlabeled slots with reordered labels is ambiguous.
func fillOptionLabels(options []domain.Option) error {
	missing := false
	for _, o := range options {
		missing = missing || strings.TrimSpace(o.Label) == ""
	}
	if !missing {
		return nil
	}
	for i := range options {
		if i >= 5 {
			return fmt.Errorf("选项数量超出A—E")
		}
		expected := string(rune('A' + i))
		label := strings.ToUpper(strings.Trim(strings.TrimSpace(options[i].Label), ".．、：:"))
		if label != "" && label != expected {
			return fmt.Errorf("选项槽位与显式标签冲突")
		}
		options[i].Label = expected
	}
	return nil
}

func assembleKnowledgeMetadata(basis *domain.KnowledgeBasis, plan domain.WorkflowTarget, answer string) error {
	if strings.TrimSpace(basis.TargetID) == "" {
		basis.TargetID = plan.ID
	}
	p := basis.CasePlan
	if p == nil {
		return nil
	}
	for i := range p.Facts {
		if p.Facts[i].ID == "" {
			p.Facts[i].ID = fmt.Sprintf("c%d", i+1)
		}
	}
	labels := make([]domain.Option, len(p.OptionEvidence))
	for i, e := range p.OptionEvidence {
		labels[i].Label = e.Label
	}
	if err := fillOptionLabels(labels); err != nil {
		return err
	}
	// These are summaries of selections the model actually made. Never add a
	// reference from the card merely because it is available or is the only one.
	seen := map[string]bool{}
	refs := []string{}
	add := func(ids []string) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				refs = append(refs, id)
			}
		}
	}
	add(basis.FactRefs)
	for i := range p.OptionEvidence {
		e := &p.OptionEvidence[i]
		e.FactRefs = uniqueReferences(e.FactRefs)
		e.CaseRefs = uniqueReferences(e.CaseRefs)
		e.Label = labels[i].Label
		add(e.FactRefs)
		if strings.EqualFold(e.Label, answer) && strings.TrimSpace(basis.AnswerSupport) == "" {
			basis.AnswerSupport = e.Reason
		}
	}
	basis.FactRefs = refs
	return nil
}

var explanationAnswerPrefix = regexp.MustCompile(`^正确答案为[\s：:]*([A-Ea-e])`)
var explanationAnswerSuffix = regexp.MustCompile(`故选[\s：:]*([A-Ea-e])[。；;！!\s]*$`)

// Boilerplate contains only the answer label already supplied by the model.
// Clinical prose is preserved. An explicit contradictory answer is an error.
func assembleExplanation(explanation, answer string) (string, error) {
	if strings.TrimSpace(explanation) == "" || len(answer) != 1 || !strings.Contains("ABCDE", answer) {
		return explanation, nil
	}
	for _, pattern := range []*regexp.Regexp{explanationAnswerPrefix, explanationAnswerSuffix} {
		indices := pattern.FindStringSubmatchIndex(explanation)
		if len(indices) > 3 {
			stated := strings.ToUpper(explanation[indices[2]:indices[3]])
			if stated != answer {
				return "", fmt.Errorf("解析显式答案与题目答案不一致")
			}
			explanation = explanation[:indices[2]] + stated + explanation[indices[3]:]
		}
	}
	compact := strings.ReplaceAll(strings.ReplaceAll(explanation, " ", ""), "\n", "")
	if !strings.HasPrefix(compact, "正确答案为"+answer) {
		explanation = "正确答案为" + answer + "。" + explanation
	}
	compact = strings.ReplaceAll(strings.ReplaceAll(explanation, " ", ""), "\n", "")
	if !strings.HasSuffix(strings.TrimRight(compact, "。；;！!"), "故选"+answer) {
		explanation = strings.TrimSpace(explanation) + "故选" + answer + "。"
	}
	return explanation, nil
}

// Repeating the same explicit reference adds no medical evidence. Unknown
// references remain untouched so the original validator can reject them.
func uniqueReferences(refs []string) []string {
	if refs == nil {
		return nil
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		if !seen[ref] {
			seen[ref] = true
			result = append(result, ref)
		}
	}
	return result
}
