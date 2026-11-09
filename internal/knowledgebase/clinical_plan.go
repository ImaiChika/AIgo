package knowledgebase

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"aigo/internal/domain"
)

var clinicalFactID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var targetSlotID = regexp.MustCompile(`^t[1-9][0-9]{0,3}$`)

// ValidateContext validates the artifact contract only. It does not certify
// medical truth, uniqueness of the answer, or personal-style compliance.
func ValidateContext(c *domain.QuestionGenerationContext) error {
	if c == nil || !SupportsWorkflowVersion(c.WorkflowVersion) {
		return fmt.Errorf("知识工作流版本不支持或依据缺失")
	}
	if c.ReferenceProtocol != "" && (c.ReferenceProtocol != ShortReferenceProtocol || !UsesClinicalPlan(c.WorkflowVersion)) {
		return fmt.Errorf("知识引用协议不支持")
	}
	if err := ValidateWorkflowPresentation(c.WorkflowVersion, c.Presentation); err != nil {
		return err
	}
	if err := ValidateBasis(c.Bundle, c.Target, c.Basis); err != nil {
		return err
	}
	if c.WorkflowVersion == LegacyWorkflowVersion {
		return nil
	}
	invalid := func(s string) error { return fmt.Errorf("知识工作流结构校验失败: %s", s) }
	if !targetSlotID.MatchString(c.Target.ID) || c.Basis.TargetID != c.Target.ID {
		return invalid("目标编号与冻结计划不一致")
	}
	found := false
	for _, p := range Targets(c.Bundle) {
		if p.CardID == c.Target.CardID && p.CardRevision == c.Target.CardRevision && p.Target == c.Target.Target {
			found = true
		}
	}
	if !found {
		return invalid("目标不属于冻结的考点知识卡")
	}
	p := c.Basis.CasePlan
	if p == nil {
		return invalid("缺少病例与选项证据映射")
	}
	if c.WorkflowVersion == ExpressionWorkflowVersion && !slices.Equal(p.ExplanationNotes, ExpressionNotes(c.Bundle)) {
		return invalid("解析附言与本人冻结选择不一致")
	}
	raw, err := json.Marshal(p)
	if err != nil || utf8.RuneCount(raw) > 16000 {
		return invalid("病例与证据映射超过预算")
	}
	validText := func(text string, max int) bool {
		return strings.TrimSpace(text) != "" && utf8.RuneCountInString(text) <= max
	}
	if !validText(p.Person, 100) || !validText(p.LeadIn, 200) || len(p.Answer) != 1 || !strings.Contains("ABCDE", p.Answer) {
		return invalid("病例一般情况、提问或答案格式无效")
	}
	if len(p.Facts) == 0 || len(p.Facts) > 32 {
		return invalid("病例事实须为1—32条")
	}
	caseIDs := map[string]bool{"person": true}
	for _, f := range p.Facts {
		if !clinicalFactID.MatchString(f.ID) || caseIDs[f.ID] || !validText(f.Text, 700) {
			return invalid("病例事实编号重复或内容无效")
		}
		switch f.Section {
		case "chief_complaint", "present_illness", "past_history", "vitals", "physical_findings", "lab_results", "imaging_results", "other":
		default:
			return invalid("病例事实分组无效")
		}
		caseIDs[f.ID] = true
	}
	if len(p.OptionEvidence) != 5 {
		return invalid("须分别提供A—E的证据说明")
	}
	facts := FactIndex(c.Bundle)
	selected := map[string]bool{}
	for _, id := range c.Basis.FactRefs {
		selected[id] = true
	}
	labels := map[string]bool{}
	for _, e := range p.OptionEvidence {
		if len(e.Label) != 1 || !strings.Contains("ABCDE", e.Label) || labels[e.Label] || !validText(e.Reason, 1200) || utf8.RuneCountInString(e.Limitations) > 500 {
			return invalid("选项标签重复或说明无效")
		}
		labels[e.Label] = true
		if len(e.CaseRefs) == 0 || len(e.CaseRefs) > 33 || len(e.FactRefs) > 20 {
			return invalid("选项病例引用数量无效")
		}
		seen := map[string]bool{}
		for _, id := range e.CaseRefs {
			if !caseIDs[id] || seen[id] {
				return invalid("选项引用不存在或重复的病例事实")
			}
			seen[id] = true
		}
		seen = map[string]bool{}
		primary := false
		for _, id := range e.FactRefs {
			if _, ok := facts[id]; !ok || !selected[id] || seen[id] {
				return invalid("选项知识引用不属于本题已选事实")
			}
			seen[id] = true
			if strings.HasPrefix(id, fmt.Sprintf("%s@%d:", c.Target.CardID, c.Target.CardRevision)) {
				primary = true
			}
		}
		if e.Label == p.Answer && !primary {
			return invalid("正确选项须引用目标卡的医学事实")
		}
	}
	if len(p.PreferenceNotes) > 8 {
		return invalid("个人偏好说明过多")
	}
	for _, note := range p.PreferenceNotes {
		if !validText(note, 500) {
			return invalid("个人偏好说明无效")
		}
	}
	if (c.WorkflowVersion == PresentationWorkflowVersion || c.WorkflowVersion == CurrentClinicalWorkflowVersion || c.WorkflowVersion == ExpressionWorkflowVersion) && !presentationOrdered(c) {
		return invalid("病例顺序与本次冻结设置不一致")
	}
	return nil
}

// RenderClinicalPlan only joins supplied text and labels. It never infers a
// diagnosis, inserts an absent finding, or changes a numeric value/order.
func RenderClinicalPlan(p *domain.ClinicalPlan, versions ...string) (stem, explanation string) {
	version := WorkflowVersion
	if len(versions) > 0 {
		version = versions[0]
	}
	sentence := func(s string) string {
		s = strings.TrimSpace(s)
		last, _ := utf8.DecodeLastRuneInString(s)
		if s != "" && !strings.ContainsRune("。；！？.!?;", last) {
			s += "。"
		}
		return s
	}
	stem = RenderClinicalStem(p)
	byLabel := map[string]domain.OptionEvidence{}
	for _, e := range p.OptionEvidence {
		byLabel[e.Label] = e
	}
	text := func(e domain.OptionEvidence) string {
		if version == InitialClinicalWorkflowVersion {
			return sentence(e.Reason) + sentence(e.Limitations)
		}
		return sentence(e.Reason)
	}
	explanation = "正确答案为" + p.Answer + "。" + text(byLabel[p.Answer])
	for _, label := range []string{"A", "B", "C", "D", "E"} {
		if label != p.Answer {
			explanation += label + "项" + text(byLabel[label])
		}
	}
	if version == ExpressionWorkflowVersion {
		for _, note := range p.ExplanationNotes {
			note = strings.TrimSpace(note)
			if note != "" && !strings.Contains(explanation, note) {
				explanation += sentence(note)
			}
		}
	}
	explanation += "故选" + p.Answer + "。"
	return
}

// Applied only to initial persistence/first check. Later human edits retain the
// original artifact and are intentionally not compared to the generated text.
func ValidateInitialQuestionContext(q *domain.A2Question, c *domain.QuestionGenerationContext) error {
	if err := ValidateContext(c); err != nil {
		return err
	}
	if c.WorkflowVersion == LegacyWorkflowVersion {
		return nil
	}
	stem, explanation := RenderClinicalPlan(c.Basis.CasePlan, c.WorkflowVersion)
	expected := *q
	expected.ClinicalStem, expected.Explanation = stem, explanation
	expected.NormalizeGeneratedA2()
	if q.Answer != c.Basis.CasePlan.Answer || q.ClinicalStem != expected.ClinicalStem || q.Explanation != expected.Explanation {
		return fmt.Errorf("病例证据映射与首次生成的题目内容不一致")
	}
	return nil
}
