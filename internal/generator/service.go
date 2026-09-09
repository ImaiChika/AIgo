package generator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
)

// Service 题目生成服务，负责调用 LLM 生成 A2 型试题。
type Service struct {
	client llm.Client // LLM 客户端（千问）
	Brief  bool       // 兼容旧配置；专家规范下不再降低说明质量
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

	kp := req.KnowledgePoints[0]

	// 构建提示词（包含完整知识点信息）
	prompt := buildPrompt(kp, req.Count, s.Brief)

	// 调用千问 API
	raw, err := s.client.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: getSystemPrompt(s.Brief)},
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: 0.4})
	if err != nil {
		return nil, err
	}

	// 解析 JSON 响应
	items, err := parseQuestions(raw)
	if err != nil {
		return nil, fmt.Errorf("生成内容解析失败: %w", err)
	}

	// 转换为领域对象，自动从知识点填充元数据
	var questions []domain.A2Question
	var rejected []string
	for _, item := range items {
		// 无大纲代码时用知识点主题做 ID 前缀，避免出现 "q--xxx" 空段
		idPrefix := SanitizeIDPrefix(kp.OutlineCode)
		if idPrefix == "" {
			idPrefix = SanitizeIDPrefix(kp.Topic)
		}
		if idPrefix == "" {
			idPrefix = "custom"
		}
		profession, system := QuestionMetadataForKnowledgePoint(kp, req.Subject)
		q := domain.A2Question{
			ID:              fmt.Sprintf("q-%s-%d", idPrefix, time.Now().UnixNano()),
			OutlineCode:     kp.OutlineCode, // 大纲代码
			Profession:      profession,     // 专业按请求或大纲系统映射填写
			System:          system,         // 系统按考试大纲名称填写
			Difficulty:      req.Difficulty, // 默认使用请求的难度
			KnowledgePoints: []domain.KnowledgePoint{kp},
			Status:          domain.StatusAIDraft,
			Version:         1,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}

		// 从 LLM 返回解析字段
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
		if v, ok := item["cognitive_level"].(string); ok {
			q.CognitiveLevel = v
		}
		if v, ok := item["exam_points"].(string); ok {
			q.ExamPoints = v
		}

		// 解析选项数组
		if opts, ok := item["options"].([]interface{}); ok {
			for _, o := range opts {
				if m, ok := o.(map[string]interface{}); ok {
					label, _ := m["label"].(string)
					text, _ := m["text"].(string)
					if label != "" && text != "" {
						q.Options = append(q.Options, domain.Option{
							Label: label,
							Text:  text,
						})
					}
				}
			}
		}

		q.NormalizeGeneratedA2()
		if err := q.ValidateGeneratedA2(); err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		questions = append(questions, q)
	}
	if len(questions) == 0 && len(rejected) > 0 {
		return nil, fmt.Errorf("AI生成结果未通过A2专家规范: %s", strings.Join(rejected, "；"))
	}
	return questions, nil
}

// SanitizeIDPrefix 清理 ID 前缀中的非安全字符（用于 URL 路径和显示）。
// 保留中文、字母、数字，其他字符替换为短横线。
func SanitizeIDPrefix(s string) string {
	var b []rune
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b = append(b, r)
		case r >= 'a' && r <= 'z':
			b = append(b, r)
		case r >= 'A' && r <= 'Z':
			b = append(b, r)
		case r == '.' || r == '-':
			b = append(b, r)
		case r >= 0x4e00 && r <= 0x9fff: // CJK 统一汉字
			b = append(b, r)
		default:
			b = append(b, '-')
		}
	}
	return string(b)
}
