// Package evaluator 提供题目质量评估功能。
// 通过规则检查题目的格式、选项、答案等是否符合 A2 型题要求。
package evaluator

import (
	"strings"
	"time"

	"aigo/internal/domain"
)

// Service 评估服务。
type Service struct{}

// NewService 创建评估服务实例。
func NewService() *Service {
	return &Service{}
}

// Evaluate 对题目进行规则评估，返回评估报告。
// 评估维度：选项数量、答案存在性、解析加分。
// 注意：解析和来源是可选字段，不作为首期必检项。
func (s *Service) Evaluate(question domain.A2Question) domain.EvaluationReport {
	report := domain.EvaluationReport{
		QuestionID:      question.ID,
		ScientificScore: 60, // 基准分
		LogicScore:      60,
		A2FitScore:      60,
		DifficultyHint:  string(question.Difficulty),
		CreatedAt:       time.Now(),
	}

	// 检查选项数量（A2 单选题应有 5 个选项）
	if len(question.Options) != 5 {
		report.Issues = append(report.Issues, "选项数量不是5个，需符合A-E单选题格式。")
		report.A2FitScore -= 20
	}
	// 检查答案是否存在
	if strings.TrimSpace(question.Answer) == "" {
		report.Issues = append(report.Issues, "缺少正确答案。")
		report.LogicScore -= 20
	}
	// 解析是可选字段，有解析则加分
	if strings.TrimSpace(question.Explanation) != "" {
		report.ScientificScore += 10
	}

	return report
}
