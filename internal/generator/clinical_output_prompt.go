package generator

import (
	"encoding/json"
	"fmt"
	"strings"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

// Preserve the baseline's clinical examples as prose. Their obsolete seven-key
// JSON shape must not compete with this request's structured output protocol.
func clinicalStyleExamples(base string) string {
	lines := strings.Split(base, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, `[{`) {
			continue
		}
		var examples []domain.A2Question
		if json.Unmarshal([]byte(line), &examples) != nil || len(examples) == 0 {
			continue
		}
		var text strings.Builder
		for _, q := range examples {
			if q.ClinicalStem == "" {
				continue
			}
			text.WriteString("病例表达参考（仅供临床内容与语言风格参考）：\n题干：" + q.ClinicalStem + "\n选项：")
			for _, o := range q.Options {
				text.WriteString(o.Label + "．" + o.Text + "；")
			}
			text.WriteString("\n答案与说明参考：" + q.Answer + "。" + q.Explanation)
		}
		if text.Len() > 0 {
			lines[i] = text.String()
		}
	}
	return strings.Join(lines, "\n")
}

func clinicalUserOutputContract(base string) string {
	const start = "【输出格式】\n输出一个JSON数组，每个元素包含以下字段："
	const end = "\n\n只输出JSON数组，不要输出其他任何内容。"
	index := strings.Index(base, start)
	if index < 0 {
		return base
	}
	tail := strings.Index(base[index:], end)
	if tail < 0 {
		return base
	}
	return base[:index] + "【输出格式】\n按下方本次病例证据协议返回完整JSON数组，字段与对象层次以该协议为准。" + base[index+tail+len(end):]
}

// Marshal the entire requested array rather than hand-writing an incomplete
// one-option fragment. The example uses actual target/fact identifiers, while
// clinical values remain explicit placeholders to be authored from the inputs.
func clinicalOutputExample(bundle domain.KnowledgeBundle, plans []domain.WorkflowTarget) string {
	return clinicalOutputExampleWithReferences(bundle, plans, func(id string) string { return id })
}
func clinicalOutputExampleWithReferences(bundle domain.KnowledgeBundle, plans []domain.WorkflowTarget, referenceName func(string) string) string {
	items := make([]map[string]any, 0, len(plans))
	for i, target := range plans {
		ref := ""
		for _, card := range bundle.Cards {
			if card.ID == target.CardID && len(card.Content.Facts) > 0 {
				ref = referenceName(knowledgebase.FactRef(card, card.Content.Facts[0]))
				break
			}
		}
		answer := string(rune('A' + i%5))
		options := []domain.Option{}
		evidence := []domain.OptionEvidence{}
		for _, label := range []string{"A", "B", "C", "D", "E"} {
			options = append(options, domain.Option{Label: label, Text: "本题同类选项的实际内容"})
			entry := domain.OptionEvidence{Label: label, CaseRefs: []string{"c1"}, FactRefs: []string{}, Reason: "结合本题已给证据填写实际理由", Limitations: "必要时填写证据边界"}
			if label == answer {
				entry.FactRefs = []string{ref}
			}
			evidence = append(evidence, entry)
		}
		plan := domain.ClinicalPlan{Person: "患者/患儿，男/女，实际年龄。", Facts: []domain.ClinicalFact{{ID: "c1", Section: "chief_complaint", Text: "本题实际症状、病程及相关事实"}}, LeadIn: "关联本病例的完整提问是", OptionEvidence: evidence, PreferenceNotes: []string{"本次适用偏好说明，可留空数组"}}
		items = append(items, map[string]any{"clinical_stem": "", "options": options, "answer": answer, "explanation": "", "difficulty": "0.65", "cognitive_level": "应用", "exam_points": "诊断与鉴别诊断", "knowledge_basis": map[string]any{"target_id": target.ID, "fact_refs": []string{ref}, "condition_notes": "本题实际适用前提", "answer_support": "本题实际最佳答案依据", "case_plan": &plan}})
	}
	raw, _ := json.Marshal(items)
	return fmt.Sprintf("\n【本次完整输出结构示意：%d道】\n下面是程序序列化的完整数组，恰好对应ordered_targets的%d项。保持对象层次及字段类型；填入本题实际内容，不照抄占位文字。答案位置、难度、考核要点和事实引用仅演示结构，须按本次真实要求选择，不能固定答案或引用不适用事实。每题五个option_evidence分别对应A—E；preference_notes须为字符串数组或空数组。clinical_stem/explanation可留空，由程序组装。\n<output_shape>\n%s\n</output_shape>\n", len(plans), len(plans), raw)
}
