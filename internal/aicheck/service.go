// Package aicheck 提供 AI 题目质量检查服务。
// 使用 LLM 检查题目的科学性、答案正确性、解析准确性等。
// 支持通过 llm.Client 接口调换检查模型。
package aicheck

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
)

// Service AI 检查服务。
type Service struct {
	client        llm.Client
	questionStore storage.QuestionStore
	aiReviewStore storage.AIReviewStore
	modelName     string
}

// NewService 创建 AI 检查服务。
// client 为 LLM 客户端（可调换），modelName 为显示用的模型名。
func NewService(client llm.Client, qs storage.QuestionStore, rs storage.AIReviewStore, model string) *Service {
	return &Service{
		client:        client,
		questionStore: qs,
		aiReviewStore: rs,
		modelName:     model,
	}
}

// CheckQuestion 检查单道题目，返回检查结果并持久化。
func (s *Service) CheckQuestion(ctx context.Context, questionID string) (*domain.AIReviewResult, error) {
	// 1. 获取题目
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, fmt.Errorf("获取题目失败: %w", err)
	}
	if q == nil {
		return nil, fmt.Errorf("题目不存在: %s", questionID)
	}

	// 2. 构建 prompt 并调用 LLM
	prompt := buildCheckPrompt(q)
	raw, err := s.client.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: getCheckSystemPrompt()},
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: 0.2, MaxTokens: 4000})
	if err != nil {
		return nil, fmt.Errorf("LLM 调用失败: %w", err)
	}

	// 3. 解析 LLM 响应
	result, err := parseCheckResponse(raw, questionID, s.modelName)
	if err != nil {
		return nil, fmt.Errorf("解析检查结果失败: %w", err)
	}
	// 记录检查时的题目版本号，用于判断结果是否过期
	result.QuestionVersion = q.Version

	// 4. 持久化结果
	if err := s.aiReviewStore.SaveReviewResult(ctx, *result); err != nil {
		return nil, fmt.Errorf("保存检查结果失败: %w", err)
	}

	// 5. 根据检查结果更新题目状态
	// 注意：只能推进草稿态（ai_draft/auto_checked），不能回退人工审核后的状态
	// （reviewing/approved/published 等状态由人工审核流程管理，AI 检查无权改动）
	if result.Verdict == "pass" {
		switch q.Status {
		case domain.StatusAIDraft, domain.StatusAutoChecked, domain.StatusAIReviewed:
			q.Status = domain.StatusAIReviewed
			q.UpdatedAt = time.Now()
			if err := s.questionStore.SaveQuestion(ctx, *q); err != nil {
				fmt.Printf("⚠ 更新题目状态失败: %v\n", err)
			}
		default:
			// 已进入人工审核或更后状态，不修改题目状态，只保存检查结果
		}
	}

	return result, nil
}

// CheckQuestions 批量检查多道题目。
func (s *Service) CheckQuestions(ctx context.Context, questionIDs []string) ([]domain.AIReviewResult, error) {
	var results []domain.AIReviewResult
	for _, id := range questionIDs {
		r, err := s.CheckQuestion(ctx, id)
		if err != nil {
			// 单题失败不中断批量，记录错误继续
			results = append(results, domain.AIReviewResult{
				QuestionID: id,
				Verdict:    "error",
				Suggestion: fmt.Sprintf("检查失败: %v", err),
				Model:      s.modelName,
				CreatedAt:  time.Now(),
			})
			continue
		}
		results = append(results, *r)
	}
	return results, nil
}

// GetResult 获取某题最新的检查结果。
func (s *Service) GetResult(ctx context.Context, questionID string) (*domain.AIReviewResult, error) {
	return s.aiReviewStore.GetLatestByQuestionID(ctx, questionID)
}

// ListResults 列出所有检查结果。
func (s *Service) ListResults(ctx context.Context, limit int) ([]domain.AIReviewResult, error) {
	return s.aiReviewStore.ListAll(ctx, limit)
}

// getCheckSystemPrompt 返回检查用的系统提示词。
func getCheckSystemPrompt() string {
	return `你是医学考试质检专家，负责审查国家执业医师考试 A2 型试题的质量。

你的任务是严格检查以下内容：
1. **科学性**：医学内容是否准确、是否有学术争议、术语是否规范
2. **答案正确性**：给定的正确答案是否确实正确，是否有更好的答案
3. **解析准确性**：解析是否正确说明了答案依据，是否正确解释了干扰项错误原因
4. **逻辑性**：题干信息是否完整、是否有逻辑漏洞、选项是否互斥
5. **A2 格式**：是否符合 A2 型题格式（临床情境题干、五选一）

请严格按 JSON 格式返回检查结果，不要输出其他内容。`
}

// buildCheckPrompt 构建检查 prompt。
func buildCheckPrompt(q *domain.A2Question) string {
	var b strings.Builder
	b.WriteString("请检查以下 A2 型试题的质量：\n\n")

	b.WriteString(fmt.Sprintf("【题干】\n%s\n\n", q.ClinicalStem))

	b.WriteString("【选项】\n")
	for _, opt := range q.Options {
		b.WriteString(fmt.Sprintf("%s. %s\n", opt.Label, opt.Text))
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("【正确答案】\n%s\n\n", q.Answer))

	if q.Explanation != "" {
		b.WriteString(fmt.Sprintf("【解析】\n%s\n\n", q.Explanation))
	}

	if q.Difficulty != "" {
		b.WriteString(fmt.Sprintf("【难度】\n%s\n\n", q.Difficulty))
	}

	b.WriteString(`【输出格式】
返回一个 JSON 对象，包含以下字段：
{
  "verdict": "pass 或 issues_found 或 reject",
  "scores": {
    "scientific": 0-100 的科学性评分,
    "logic": 0-100 的逻辑性评分,
    "a2_fit": 0-100 的 A2 格式适配度评分,
    "answer": 0-100 的答案准确性评分
  },
  "issues": [
    {
      "field": "问题字段（stem/options/answer/explanation）",
      "severity": "error 或 warning 或 info",
      "message": "问题描述"
    }
  ],
  "suggestion": "整体修改建议"
}

评分标准：
- 90-100：优秀，无明显问题
- 70-89：良好，有小问题但不影响使用
- 60-69：及格，有需要修改的问题
- 60 以下：不合格，需要重写

verdict 判定规则：
- pass：所有维度 >= 70 且无 error 级别问题
- issues_found：有 warning 级别问题或部分维度 60-69
- reject：有 error 级别问题或任一维度 < 60`)

	return b.String()
}

// checkResponse LLM 返回的检查结果结构。
type checkResponse struct {
	Verdict    string `json:"verdict"`
	Scores     struct {
		Scientific int `json:"scientific"`
		Logic      int `json:"logic"`
		A2Fit      int `json:"a2_fit"`
		Answer     int `json:"answer"`
	} `json:"scores"`
	Issues []struct {
		Field    string `json:"field"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"issues"`
	Suggestion string `json:"suggestion"`
}

// parseCheckResponse 解析 LLM 返回的检查结果。
func parseCheckResponse(raw, questionID, modelName string) (*domain.AIReviewResult, error) {
	raw = strings.TrimSpace(raw)
	// 去掉可能的 markdown 代码块
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		var cleaned []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				cleaned = append(cleaned, line)
			}
		}
		raw = strings.Join(cleaned, "\n")
	}

	var resp checkResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}

	// 验证 verdict
	switch resp.Verdict {
	case "pass", "issues_found", "reject":
	default:
		resp.Verdict = "issues_found"
	}

	result := &domain.AIReviewResult{
		ID: fmt.Sprintf("air-%s-%d", questionID, time.Now().UnixNano()),
		QuestionID: questionID,
		Verdict:    resp.Verdict,
		Scores: domain.ReviewScores{
			Scientific: resp.Scores.Scientific,
			Logic:      resp.Scores.Logic,
			A2Fit:      resp.Scores.A2Fit,
			Answer:     resp.Scores.Answer,
		},
		Suggestion:  resp.Suggestion,
		Model:       modelName,
		RawResponse: raw,
		CreatedAt:   time.Now(),
	}

	for _, issue := range resp.Issues {
		result.Issues = append(result.Issues, domain.ReviewIssue{
			Field:    issue.Field,
			Severity: issue.Severity,
			Message:  issue.Message,
		})
	}

	return result, nil
}
