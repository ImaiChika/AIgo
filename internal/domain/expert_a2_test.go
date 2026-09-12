package domain

import (
	"errors"
	"testing"
)

func validExpertA2Question() A2Question {
	return A2Question{
		ClinicalStem: "男，45岁。反复上腹痛2年，加重1天。T 36.8℃，P 80次/分，R 18次/分，BP 120/80mmHg，腹软，上腹部压痛。该患者最可能的诊断是",
		Options: []Option{
			{Label: "A", Text: "胃溃疡"},
			{Label: "B", Text: "十二指肠溃疡"},
			{Label: "C", Text: "胃癌"},
			{Label: "D", Text: "慢性胃炎"},
			{Label: "E", Text: "功能性消化不良"},
		},
		Answer:         "A",
		Explanation:    "正确答案为 A。结合疼痛特点考虑胃溃疡。B 项疼痛规律不同；C 项缺乏警示表现；D 项不能解释节律性疼痛；E 项应在排除器质性疾病后考虑。故选 A。",
		Difficulty:     "0.65",
		CognitiveLevel: "简单应用",
		ExamPoints:     "诊断与鉴别诊断，临床表现",
		OutlineCode:    "110.4.3.1.1",
		Profession:     "消化",
		System:         "消化系统",
	}
}

func TestValidateGeneratedA2(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*A2Question)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "four options remain legacy compatible but fail generated rule", mutate: func(q *A2Question) { q.Options = q.Options[:4] }},
		{name: "forbidden heading", mutate: func(q *A2Question) { q.ClinicalStem = "男，45岁。主诉：腹痛1天。最可能的诊断是" }},
		{name: "exam heading allowed", mutate: func(q *A2Question) { q.ClinicalStem = "男，45岁。查体：腹软。辅助检查：血常规正常。该患者最可能的诊断是" }, valid: true},
		{name: "question mark", mutate: func(q *A2Question) { q.ClinicalStem += "？" }},
		{name: "interrogative word", mutate: func(q *A2Question) { q.ClinicalStem = "男，45岁。腹痛1天。最适宜的检查是什么" }},
		{name: "forbidden option", mutate: func(q *A2Question) { q.Options[4].Text = "以上都是" }},
		{name: "invalid difficulty", mutate: func(q *A2Question) { q.Difficulty = "0.63" }},
		{name: "disease used as exam point", mutate: func(q *A2Question) { q.ExamPoints = "胃溃疡" }},
		{name: "missing distractor explanation", mutate: func(q *A2Question) { q.Explanation = "正确答案为 A。诊断为胃溃疡。故选 A。" }},
		{name: "explanation remains optional", mutate: func(q *A2Question) { q.Explanation = "" }, valid: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := validExpertA2Question()
			if test.mutate != nil {
				test.mutate(&q)
			}
			err := q.ValidateGeneratedA2()
			if test.valid && err != nil {
				t.Fatalf("合法专家A2题校验失败: %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidQuestion) {
				t.Fatalf("不合格题应返回 ErrInvalidQuestion，实际: %v", err)
			}
		})
	}
}

func TestNormalizeGeneratedA2(t *testing.T) {
	q := validExpertA2Question()
	q.ClinicalStem = " 男，45岁。查体：腹软。提问：最可能的诊断是？ "
	q.Options[0].Label = "a．"
	q.Answer = "a．"
	q.ExamPoints = " 诊断与鉴别诊断, 临床表现 "
	q.Explanation = "说明：" + q.Explanation

	q.NormalizeGeneratedA2()
	if q.ClinicalStem != "男，45岁。查体：腹软。最可能的诊断是" {
		t.Errorf("题干归一化错误: %q", q.ClinicalStem)
	}
	if q.Options[0].Label != "A" || q.Answer != "A" {
		t.Errorf("答案标签归一化错误: option=%q answer=%q", q.Options[0].Label, q.Answer)
	}
	if q.ExamPoints != "诊断与鉴别诊断，临床表现" {
		t.Errorf("考核要点归一化错误: %q", q.ExamPoints)
	}
	if q.CognitiveLevel != "应用" {
		t.Errorf("历史认知层次标签应归一为“应用”: %q", q.CognitiveLevel)
	}
}

func TestValidateForReviewKeepsLegacyCompatibilityAndChecksEnrichedQuestions(t *testing.T) {
	legacy := validQuestionForTest()
	if err := legacy.ValidateForReview(); err != nil {
		t.Fatalf("无专家元数据的历史四选项题应保持兼容: %v", err)
	}

	enriched := validExpertA2Question()
	enriched.ClinicalStem += "？"
	if err := enriched.ValidateForReview(); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatalf("带专家元数据的新题应执行严格校验，实际: %v", err)
	}
}

func TestValidateForReviewDoesNotBlockMetadataMismatch(t *testing.T) {
	q := validExpertA2Question()
	q.Difficulty = "0.63"
	q.CognitiveLevel = "不规范标签"
	q.ExamPoints = "胃溃疡"
	if err := q.ValidateForReview(); err != nil {
		t.Fatalf("元数据不规范不应阻断核心内容审核: %v", err)
	}
}
