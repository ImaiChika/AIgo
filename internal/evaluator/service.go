package evaluator

import (
	"strings"
	"time"

	"aigo/internal/domain"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) Evaluate(question domain.A2Question) domain.EvaluationReport {
	report := domain.EvaluationReport{
		QuestionID:      question.ID,
		ScientificScore: 60,
		LogicScore:      60,
		A2FitScore:      60,
		DifficultyHint:  string(question.Difficulty),
		CreatedAt:       time.Now(),
	}

	if len(question.Options) != 5 {
		report.Issues = append(report.Issues, "选项数量不是5个，需符合A-E单选题格式。")
		report.A2FitScore -= 20
	}
	if strings.TrimSpace(question.Answer) == "" {
		report.Issues = append(report.Issues, "缺少正确答案。")
		report.LogicScore -= 20
	}
	// 解析和来源是可选字段，不作为首期必检项
	if strings.TrimSpace(question.Explanation) != "" {
		report.ScientificScore += 10 // 有解析加分
	}

	return report
}
