package domain

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var a2StemStartPattern = regexp.MustCompile(`^(男|女)，\s*\d+\s*(岁|个月|月|天)`) // 专家要求：性别、年龄开头
var a2DifficultyPattern = regexp.MustCompile(`^(0\.\d{2}|1\.00)$`)

var allowedA2CognitiveLevels = map[string]struct{}{
	"记忆": {}, "理解": {}, "简单应用": {}, "综合应用": {},
}

var allowedA2ExamPoints = map[string]struct{}{
	"临床基本概念":    {},
	"医学基础知识":    {},
	"病因与发病机制":   {},
	"临床表现":      {},
	"辅助检查":      {},
	"诊断与鉴别诊断":   {},
	"治疗原则":      {},
	"具体处置措施":    {},
	"并发症及其诊断治疗": {},
	"疾病预防与康复":   {},
	"医学人文":      {},
}

// NormalizeGeneratedA2 修正不会改变医学语义的格式问题。
// 该方法仅用于 AI 新生成题，不改变历史题兼容校验。
func (q *A2Question) NormalizeGeneratedA2() {
	if q == nil {
		return
	}
	stem := strings.TrimSpace(q.ClinicalStem)
	for _, marker := range []string{"一般情况：", "主诉：", "现病史：", "既往史：", "查体：", "辅助检查：", "实验室检查：", "专科情况：", "提问："} {
		stem = strings.ReplaceAll(stem, marker, "")
	}
	stem = strings.TrimRight(stem, "？? \t\r\n")
	q.ClinicalStem = stem
	q.Answer = strings.ToUpper(strings.Trim(strings.TrimSpace(q.Answer), ".．、：:"))
	for i := range q.Options {
		q.Options[i].Label = strings.ToUpper(strings.Trim(strings.TrimSpace(q.Options[i].Label), ".．、：:"))
		q.Options[i].Text = strings.TrimSpace(q.Options[i].Text)
	}
	q.Explanation = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q.Explanation), "说明："))
	q.CognitiveLevel = strings.TrimSpace(q.CognitiveLevel)
	q.ExamPoints = normalizeA2ExamPoints(q.ExamPoints)
}

// ValidateGeneratedA2 校验 AI 新生成题是否符合专家 A2 规范。
// Validate 继续兼容历史四选项题；新生成与批量生成使用本方法。
func (q *A2Question) ValidateGeneratedA2() error {
	if err := q.Validate(); err != nil {
		return err
	}
	if len(q.Options) != 5 {
		return invalidQuestionError("AI生成的A2题必须有A-E五个选项")
	}
	for i, option := range q.Options {
		want := string(rune('A' + i))
		if option.Label != want {
			return invalidQuestionError("选项标签必须依次为A、B、C、D、E")
		}
		if strings.Contains(option.Text, "以上都是") || strings.Contains(option.Text, "以上都不是") {
			return invalidQuestionError("选项不得使用“以上都是”或“以上都不是”")
		}
	}
	if !a2StemStartPattern.MatchString(q.ClinicalStem) {
		return invalidQuestionError("题干必须以“男/女，年龄”开头")
	}
	for _, marker := range []string{"一般情况：", "主诉：", "现病史：", "既往史：", "查体：", "辅助检查：", "实验室检查：", "专科情况：", "提问："} {
		if strings.Contains(q.ClinicalStem, marker) {
			return invalidQuestionError("题干不得出现格式引导词“" + marker + "”")
		}
	}
	if strings.HasSuffix(q.ClinicalStem, "？") || strings.HasSuffix(q.ClinicalStem, "?") {
		return invalidQuestionError("最后提问句末不得使用问号")
	}
	tail := q.ClinicalStem
	if index := strings.LastIndex(tail, "。"); index >= 0 && index+len("。") < len(tail) {
		tail = tail[index+len("。"):]
	}
	if strings.Contains(tail, "哪个") || strings.Contains(tail, "什么") {
		return invalidQuestionError("最后提问不得使用“哪个”或“什么”")
	}
	if strings.TrimSpace(q.Explanation) != "" {
		compactExplanation := strings.ReplaceAll(strings.ReplaceAll(q.Explanation, " ", ""), "\n", "")
		if !strings.HasPrefix(compactExplanation, "正确答案为"+q.Answer) {
			return invalidQuestionError("说明必须以正确答案及诊断或结论开头")
		}
		ending := strings.TrimRight(compactExplanation, "。；;！!")
		if !strings.HasSuffix(ending, "故选"+q.Answer) {
			return invalidQuestionError("说明必须以“故选X”收尾")
		}
		for _, option := range q.Options {
			if option.Label == q.Answer {
				continue
			}
			if !mentionsOptionExplanation(compactExplanation, option.Label) {
				return invalidQuestionError("说明必须逐项分析干扰项" + option.Label)
			}
		}
	}
	if !a2DifficultyPattern.MatchString(string(q.Difficulty)) {
		return invalidQuestionError("预估难度必须是0.00到1.00之间的两位小数")
	}
	difficulty, _ := strconv.ParseFloat(string(q.Difficulty), 64)
	if math.Abs(difficulty*20-math.Round(difficulty*20)) > 1e-9 {
		return invalidQuestionError("预估难度必须是0.05的倍数")
	}
	if _, ok := allowedA2CognitiveLevels[q.CognitiveLevel]; !ok {
		return invalidQuestionError("认知层次不在受控范围内")
	}
	if q.ExamPoints == "" {
		return invalidQuestionError("考核要点不能为空")
	}
	seen := map[string]struct{}{}
	for _, point := range strings.Split(q.ExamPoints, "，") {
		if _, ok := allowedA2ExamPoints[point]; !ok {
			return invalidQuestionError("考核要点不在受控词表内：" + point)
		}
		if _, duplicated := seen[point]; duplicated {
			return invalidQuestionError("考核要点不得重复：" + point)
		}
		seen[point] = struct{}{}
	}
	if strings.TrimSpace(q.OutlineCode) == "" {
		return invalidQuestionError("大纲代码不能为空")
	}
	if strings.TrimSpace(q.Profession) == "" {
		return invalidQuestionError("专业不能为空")
	}
	if strings.TrimSpace(q.System) == "" {
		return invalidQuestionError("系统不能为空")
	}
	return nil
}

// ValidateForReview 对带专家元数据的新格式题使用严格规则；
// 对尚未迁移元数据的历史题继续使用兼容校验，避免破坏既有题库流程。
func (q *A2Question) ValidateForReview() error {
	if err := q.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(q.OutlineCode) == "" &&
		strings.TrimSpace(q.CognitiveLevel) == "" &&
		strings.TrimSpace(q.ExamPoints) == "" &&
		strings.TrimSpace(q.Profession) == "" &&
		strings.TrimSpace(q.System) == "" {
		return nil
	}
	return q.ValidateGeneratedA2()
}

func normalizeA2ExamPoints(value string) string {
	value = strings.ReplaceAll(value, ",", "，")
	parts := strings.Split(value, "，")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return strings.Join(result, "，")
}

func mentionsOptionExplanation(explanation, label string) bool {
	return strings.Contains(explanation, label+"项") || strings.Contains(explanation, label+"选项")
}
