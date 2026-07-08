package generator

import (
	"context"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
)

type Service struct {
	client llm.Client
}

func NewService(client llm.Client) *Service {
	return &Service{client: client}
}

func (s *Service) Generate(ctx context.Context, req domain.GenerationRequest) ([]domain.A2Question, error) {
	if req.Count <= 0 {
		req.Count = 1
	}
	if len(req.KnowledgePoints) == 0 {
		return nil, fmt.Errorf("至少需要一个知识点")
	}

	prompt := buildPrompt(req.Subject, string(req.Difficulty), req.KnowledgePoints[0].Topic, req.Count)

	raw, err := s.client.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: 0.4, MaxTokens: 1800})
	if err != nil {
		return nil, err
	}

	items, err := parseQuestions(raw)
	if err != nil {
		return nil, fmt.Errorf("生成内容解析失败: %w", err)
	}

	var questions []domain.A2Question
	for _, item := range items {
		q := domain.A2Question{
			ID:         fmt.Sprintf("draft-%d", time.Now().UnixNano()),
			Difficulty: req.Difficulty,
			KnowledgePoints: req.KnowledgePoints,
			Status:     domain.StatusAIDraft,
			Version:    1,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
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
