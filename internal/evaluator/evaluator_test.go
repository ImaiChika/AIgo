package evaluator

import (
	"testing"
	"aigo/internal/domain"
)

func TestEvaluate_OptionalFields(t *testing.T) {
	svc := NewService()

	// 没有解析、没有来源 — 不应该报错
	q := domain.A2Question{
		ID:           "test-1",
		ClinicalStem: "患者男，65岁...",
		Options: []domain.Option{
			{Label: "A", Text: "选项A"}, {Label: "B", Text: "选项B"},
			{Label: "C", Text: "选项C"}, {Label: "D", Text: "选项D"},
			{Label: "E", Text: "选项E"},
		},
		Answer:     "A",
		Difficulty: domain.DifficultyMedium,
	}
	report := svc.Evaluate(q)
	for _, issue := range report.Issues {
		if issue == "缺少解析。" || issue == "缺少解析来源，必须进入人工核验。" {
			t.Errorf("不应报错: %s", issue)
		}
	}
	if report.ScientificScore < 60 {
		t.Errorf("无解析无来源不应扣分，但 ScientificScore=%d", report.ScientificScore)
	}

	// 有解析 — 应该加分
	q.Explanation = "患者表现为..."
	report = svc.Evaluate(q)
	if report.ScientificScore != 70 {
		t.Errorf("有解析应加10分，但 ScientificScore=%d", report.ScientificScore)
	}
}
