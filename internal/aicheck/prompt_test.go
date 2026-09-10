package aicheck

import (
	"strings"
	"testing"

	"aigo/internal/domain"
)

func TestBuildCheckPromptIncludesExpertMetadata(t *testing.T) {
	q := &domain.A2Question{
		ClinicalStem:   "男，45岁。腹痛1天。最可能的诊断是",
		Options:        []domain.Option{{Label: "A", Text: "胃溃疡"}},
		Answer:         "A",
		Difficulty:     "0.65",
		CognitiveLevel: "简单应用",
		ExamPoints:     "诊断与鉴别诊断",
		OutlineCode:    "110.4.3.1.6.5",
		Profession:     "消化",
		System:         "三、消化系统",
	}
	prompt := buildCheckPrompt(q)
	for _, want := range []string{
		"【参考元数据（只用于出题提示词反馈，不参与本次质检判定）】",
		"认知层次：简单应用",
		"考核要点：诊断与鉴别诊断",
		"大纲代码：110.4.3.1.6.5",
		"专业：消化",
		"系统：三、消化系统",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("检查prompt缺少 %q", want)
		}
	}
}

func TestCheckSystemPromptDoesNotRejectOptionalExplanationOrUnitSpacing(t *testing.T) {
	prompt := getCheckSystemPrompt()
	for _, want := range []string{
		"解析是可选字段",
		"110/70mmHg",
		"轻微排版建议只能标为info",
		"元数据问题只能标为info",
		"不得单独触发issues_found或reject",
		"另一个同样合理的选项",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("AI检查规则缺少 %q", want)
		}
	}
}
