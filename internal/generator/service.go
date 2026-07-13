package generator

import (
	"context"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
)

// Service 题目生成服务，负责调用 LLM 生成 A2 型试题。
type Service struct {
	client llm.Client // LLM 客户端（千问）
}

// NewService 创建生成服务实例。
func NewService(client llm.Client) *Service {
	return &Service{client: client}
}

// Generate 根据生成请求调用千问 API 生成试题。
// 流程：构建 prompt → 调 LLM → 解析 JSON → 转为 A2Question 切片。
func (s *Service) Generate(ctx context.Context, req domain.GenerationRequest) ([]domain.A2Question, error) {
	if req.Count <= 0 {
		req.Count = 1
	}
	if len(req.KnowledgePoints) == 0 {
		return nil, fmt.Errorf("至少需要一个知识点")
	}

	// 构建提示词
	prompt := buildPrompt(req.Subject, string(req.Difficulty), req.KnowledgePoints[0].Topic, req.Count)

	// 调用千问 API
	raw, err := s.client.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: 0.4, MaxTokens: 1800})
	if err != nil {
		return nil, err
	}

	// 解析 JSON 响应
	items, err := parseQuestions(raw)
	if err != nil {
		return nil, fmt.Errorf("生成内容解析失败: %w", err)
	}

	// 转换为领域对象
	var questions []domain.A2Question
	for _, item := range items {
		q := domain.A2Question{
			ID:              fmt.Sprintf("draft-%d", time.Now().UnixNano()),
			Difficulty:      req.Difficulty,
			KnowledgePoints: req.KnowledgePoints,
			Status:          domain.StatusAIDraft, // 初始状态：AI草稿
			Version:         1,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		if v, ok := item["clinical_stem"].(string); ok {
			q.ClinicalStem = v
		}
		if v, ok := item["answer"].(string); ok {
			q.Answer = v
		}
		if v, ok := item["explanation"].(string); ok {
			q.Explanation = v
		}
		if v, ok := item["difficulty"].(string); ok {
			q.Difficulty = domain.Difficulty(v)
		}
		// 解析选项数组
		if opts, ok := item["options"].([]interface{}); ok {
			for _, o := range opts {
				if m, ok := o.(map[string]interface{}); ok {
					q.Options = append(q.Options, domain.Option{
						Label: m["label"].(string),
						Text:  m["text"].(string),
					})
				}
			}
		}
		questions = append(questions, q)
	}
	return questions, nil
}
