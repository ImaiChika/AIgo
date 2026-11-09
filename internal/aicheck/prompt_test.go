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

func TestCheckPromptUsesQuestionFormatWithoutRecheckingLegacyRules(t *testing.T) {
	legacy := &domain.A2Question{}
	current := &domain.A2Question{FormatVersion: domain.A2FormatSecondWave}
	if getCheckSystemPromptForQuestion(legacy) != getCheckSystemPrompt() {
		t.Fatal("历史题的首次检查规则不应被新版格式替换")
	}
	newPrompt := getCheckSystemPromptForQuestion(current)
	for _, rule := range []string{"患者/患儿，男/女，年龄", "治疗原则不使用“首选”", "全部选项与提问同类", "解析是可选字段", "不因本轮格式优化新增额外医学淘汰条件"} {
		if !strings.Contains(newPrompt, rule) {
			t.Fatalf("第二波检查规则缺少: %s", rule)
		}
	}
	if strings.Contains(newPrompt, "体格与检查标签允许保留") {
		t.Fatal("新版质检不能混入旧版允许模板标签的规则")
	}
	if !strings.Contains(buildCheckPrompt(current), "本题适用格式") {
		t.Fatal("检查任务必须标明所用格式口径")
	}
}
