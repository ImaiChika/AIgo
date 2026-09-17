// Package aicheck 提供 AI 题目质量检查服务。
// 使用 LLM 检查题目的科学性、答案正确性、解析准确性等。
// 支持通过 llm.Client 接口调换检查模型。
package aicheck

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	taskStore     storage.AICheckTaskStore
	modelName     string

	// worker 配置（零值时使用默认，见 worker.go）
	CheckTimeout time.Duration // 单次检查调用超时；任务租约 = 超时 + 缓冲
	MaxAttempts  int           // 失败重试上限，耗尽标记最终失败
	RetryBackoff time.Duration // 首次重试退避，按尝试次数指数递增

	autoEnabled bool // 自动触发检查开关，见 worker.go
}

// NewService 创建 AI 检查服务。
// client 为 LLM 客户端（可调换），modelName 为显示用的模型名；
// taskStore 为持久化任务队列，nil 时仅支持同步检查（CheckQuestions）。
func NewService(client llm.Client, qs storage.QuestionStore, rs storage.AIReviewStore, ts storage.AICheckTaskStore, model string) *Service {
	return &Service{
		client:        client,
		questionStore: qs,
		aiReviewStore: rs,
		taskStore:     ts,
		modelName:     model,
		autoEnabled:   true,
	}
}

// CheckQuestion 检查单道题目，返回检查结果并持久化。
// 供手动检查与批量检查使用：只落库检查结论与题目状态，不涉及检查任务。
func (s *Service) CheckQuestion(ctx context.Context, questionID string) (*domain.AIReviewResult, error) {
	if existing, err := s.aiReviewStore.GetLatestByQuestionID(ctx, questionID); err == nil && existing != nil {
		return existing, nil
	}
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, fmt.Errorf("题目不存在: %s", questionID)
	}
	if q.Status != domain.StatusAIDraft && q.Status != domain.StatusAutoChecked {
		return nil, fmt.Errorf("AI 检查只允许在题目首次生成后执行一次")
	}
	return s.checkQuestion(ctx, questionID, "")
}

// checkQuestion 执行检查并落库。taskID 非空时（后台 worker 路径）把检查任务
// 的完成标记与检查结论放进同一事务，确保任务状态与题目状态始终一致。
func (s *Service) checkQuestion(ctx context.Context, questionID, taskID string) (*domain.AIReviewResult, error) {
	// 1. 获取题目
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, fmt.Errorf("获取题目失败: %w", err)
	}
	if q == nil {
		return nil, fmt.Errorf("题目不存在: %s", questionID)
	}
	strictQuestion := *q
	strictQuestion.Options = append([]domain.Option(nil), q.Options...)
	strictErr := strictQuestion.ValidateForReview()

	// 2. 构建 prompt 并调用 LLM
	prompt := buildCheckPrompt(q)
	raw, err := s.client.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: getCheckSystemPrompt()},
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: 0.2})
	if err != nil {
		return nil, fmt.Errorf("LLM 调用失败: %w", err)
	}

	// 3. 解析 LLM 响应
	result, err := parseCheckResponse(raw, questionID, s.currentModelName())
	if err != nil {
		return nil, fmt.Errorf("解析检查结果失败: %w", err)
	}
	// 元数据只用于反馈出题提示词，不是 AI 质量门禁。即使模型误把元数据
	// 标成 warning/error，也不能因此淘汰核心内容和格式均可用的题目。
	normalizeNonBlockingMetadataIssues(result)
	// 记录检查时的题目版本号，用于判断结果是否过期
	result.QuestionVersion = q.Version
	if strictErr != nil {
		result.Verdict = "reject"
		if result.Scores.A2Fit > 50 {
			result.Scores.A2Fit = 50
		}
		result.Issues = append(result.Issues, domain.ReviewIssue{
			Field:    "expert_format",
			Severity: "error",
			Message:  strictErr.Error(),
		})
		result.Suggestion = "请先修正专家A2规范问题：" + strictErr.Error() + "。" + result.Suggestion
	}

	// 4. 落库：检查结果、题目状态推进或淘汰删除、任务完成要么全部提交、要么全部回滚。
	// AI 检查仅在题目首次创建时执行一次：通过则把草稿态推进为 ai_reviewed；
	// 不通过时题目自动淘汰删除（不入题库），淘汰原因留档供生成页展示。
	// 人工修改后的把关责任在专家审核环节；系统不创建第二次 AI 检查。
	if err := s.applyCheckOutcome(ctx, result, q, taskID); err != nil {
		return nil, err
	}
	if result.Verdict != "pass" && q.Status == domain.StatusAIDraft {
		slog.Info("AI 检查淘汰题目", "question_id", questionID, "verdict", result.Verdict)
	}

	return result, nil
}

// applyCheckOutcome 按检查结论构造落库动作并持久化。
// 生产 PostgreSQL 实现了 storage.AICheckOutcomeStore，所有写入收敛为单个事务；
// 其他存储（测试内存实现）回退为分步写入，保持原有语义。
func (s *Service) applyCheckOutcome(ctx context.Context, result *domain.AIReviewResult, q *domain.A2Question, taskID string) error {
	outcome := storage.AICheckOutcome{Result: *result}
	if result.Verdict == "pass" {
		switch q.Status {
		case domain.StatusAIDraft, domain.StatusAutoChecked, domain.StatusAIReviewed:
			updated := *q
			updated.Status = domain.StatusAIReviewed
			updated.UpdatedAt = time.Now()
			outcome.Question = &updated
		default:
			// 已进入人工审核或更后状态，不修改题目状态，只保存检查结果
		}
	} else if q.Status == domain.StatusAIDraft {
		// 首次检查不通过 → 淘汰：留档原因后删除题目（题库只保留检查通过的题）
		outcome.Discard = &domain.AICheckDiscard{
			ID:          fmt.Sprintf("aicd-%s-%d", q.ID, time.Now().UnixNano()),
			QuestionID:  q.ID,
			Verdict:     result.Verdict,
			Scores:      result.Scores,
			Issues:      result.Issues,
			Suggestion:  result.Suggestion,
			Model:       result.Model,
			StemSummary: stemSummary(q.ClinicalStem, 60),
			CreatedAt:   time.Now(),
		}
		outcome.DeleteQuestionID = q.ID
	}

	if outcomeStore, ok := s.questionStore.(storage.AICheckOutcomeStore); ok {
		outcome.CompleteTaskID = taskID
		return outcomeStore.ApplyAICheckOutcome(ctx, outcome)
	}

	// 回退：分步写入（仅测试内存存储）
	if err := s.aiReviewStore.SaveReviewResult(ctx, outcome.Result); err != nil {
		return fmt.Errorf("保存检查结果失败: %w", err)
	}
	if outcome.Question != nil {
		if err := s.questionStore.SaveQuestion(ctx, *outcome.Question); err != nil {
			return fmt.Errorf("更新题目状态失败: %w", err)
		}
	}
	if outcome.Discard != nil {
		if err := s.aiReviewStore.SaveDiscardResult(ctx, *outcome.Discard); err != nil {
			return fmt.Errorf("保存淘汰记录失败: %w", err)
		}
		if err := s.questionStore.DeleteQuestion(ctx, outcome.DeleteQuestionID); err != nil {
			return fmt.Errorf("删除未通过检查的题目失败: %w", err)
		}
	}
	if taskID != "" && s.taskStore != nil {
		if err := s.taskStore.CompleteCheckTask(ctx, taskID); err != nil {
			return fmt.Errorf("标记检查任务完成失败: %w", err)
		}
	}
	return nil
}

// currentModelName 支持动态 AI 检查配置。旧的固定客户端或测试 mock 没有
// InfoProvider 时继续使用服务初始化时传入的模型名。
func (s *Service) currentModelName() string {
	if provider, ok := s.client.(llm.InfoProvider); ok {
		if model := provider.Info().Model; strings.TrimSpace(model) != "" {
			return model
		}
	}
	return s.modelName
}

// stemSummary 生成题干摘要，用于淘汰记录展示。
func stemSummary(stem string, limit int) string {
	runes := []rune(strings.TrimSpace(stem))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
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
				Model:      s.currentModelName(),
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

// GetResultsByQuestionIDs 批量获取多题的最新检查结果，返回 questionID → 结果。
// 供审核侧展示 AI 检查参考信息使用。
func (s *Service) GetResultsByQuestionIDs(ctx context.Context, questionIDs []string) (map[string]domain.AIReviewResult, error) {
	results, err := s.aiReviewStore.ListByQuestionIDs(ctx, questionIDs)
	if err != nil {
		return nil, err
	}
	m := make(map[string]domain.AIReviewResult, len(results))
	for _, r := range results {
		m[r.QuestionID] = r
	}
	return m, nil
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
5. **A2 格式**：是否为A-E严格五选一；题干是否以性别、年龄开头；是否删除“主诉：/现病史：/提问：”等病史与提问引导词（“查体：/专科情况：/辅助检查：/实验室检查：”等体格与检查标签允许保留）；最后提问是否不用“哪个/什么”和问号
6. **说明格式**：是否先写“正确答案为X”，给出诊断或结论及依据，逐项分析四个干扰项，并以“故选X”收尾
7. **元数据仅作提示**：可以指出难度、认知层次、考核要点、大纲代码、专业或系统等元数据与出题要求不一致，但这些字段不是本次 AI 质量门禁，不能据此判为不通过，也不能触发题目淘汰。

判定注意事项：
- 解析是可选字段；没有解析不得仅因此判为不通过。有解析时再检查其准确性和专家格式。
- “110/70mmHg”与“110/70 mmHg”等常见数值单位写法均可接受，不得仅因是否留空格判为warning或error。
- 不影响医学正确性、答案唯一性或可用性的轻微排版建议只能标为info；info不影响pass。
- 难度、认知层次、考核要点等元数据问题只能标为info，并在suggestion中提示“应修正出题提示词”；即使模型认为元数据不规范，也不得单独触发issues_found或reject，必须保持基于核心内容与格式得出的verdict。
- 若存在另一个同样合理的选项，应按答案不唯一处理，至少判为issues_found；不要只验证给定答案本身是否成立。

请严格按 JSON 格式返回检查结果，不要输出其他内容。`
}

// buildCheckPrompt 构建检查 prompt。
func buildCheckPrompt(q *domain.A2Question) string {
	var b strings.Builder
	b.WriteString("请检查以下 A2 型试题的质量：\n\n")

	b.WriteString(fmt.Sprintf("【题干】\n%s\n\n", q.ClinicalStem))

	b.WriteString("【选项】\n")
	for _, opt := range q.Options {
		b.WriteString(fmt.Sprintf("%s．%s\n", opt.Label, opt.Text))
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("【正确答案】\n%s\n\n", q.Answer))

	if q.Explanation != "" {
		b.WriteString(fmt.Sprintf("【解析】\n%s\n\n", q.Explanation))
	}

	b.WriteString("【参考元数据（只用于出题提示词反馈，不参与本次质检判定）】\n")
	if q.Difficulty != "" {
		b.WriteString(fmt.Sprintf("难度：%s\n", q.Difficulty))
	}
	if q.CognitiveLevel != "" {
		b.WriteString(fmt.Sprintf("认知层次：%s\n", q.CognitiveLevel))
	}
	if q.ExamPoints != "" {
		b.WriteString(fmt.Sprintf("考核要点：%s\n", q.ExamPoints))
	}
	if q.OutlineCode != "" {
		b.WriteString(fmt.Sprintf("大纲代码：%s\n", q.OutlineCode))
	}
	if q.Profession != "" {
		b.WriteString(fmt.Sprintf("专业：%s\n", q.Profession))
	}
	if q.System != "" {
		b.WriteString(fmt.Sprintf("系统：%s\n", q.System))
	}
	b.WriteString("\n")

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
      "field": "问题字段（stem/options/answer/explanation 或 difficulty/cognitive_level/exam_points/metadata）",
      "severity": "error 或 warning 或 info",
      "message": "问题描述"
    }
  ],
  "suggestion": "整体修改建议"
}

评分标准：
- 四个分数只评价医学内容、答案解析一致性、选项逻辑和 A2 格式，不评价元数据标签。
- 90-100：优秀，无明显核心问题
- 70-89：良好，有小问题但不影响使用
- 60-69：及格，有需要修改的核心问题
- 60 以下：核心内容不合格，需要重写

verdict 判定规则：
- pass：所有核心维度 >= 70 且无核心 error/warning；只有 info 或元数据问题仍必须 pass
- issues_found：有核心 warning 或部分核心维度 60-69
- reject：有核心 error 或任一核心维度 < 60；元数据问题不得单独触发 reject`)

	return b.String()
}

// normalizeNonBlockingMetadataIssues 将元数据问题降为信息反馈，并在没有
// 核心内容/格式问题时把误判的非 pass 结论恢复为 pass。
//
// 这层保护用于兼容旧检查模型：即使模型仍把“简单应用”等标签当成 error，
// 也不能让一条医学内容正确的草稿因元数据小问题被自动删除。
func normalizeNonBlockingMetadataIssues(result *domain.AIReviewResult) {
	if result == nil {
		return
	}
	hasMetadataIssue := false
	hasBlockingIssue := false
	for i := range result.Issues {
		issue := &result.Issues[i]
		if isMetadataIssue(*issue) {
			hasMetadataIssue = true
			issue.Severity = "info"
			continue
		}
		if issue.Severity == "error" || issue.Severity == "warning" {
			hasBlockingIssue = true
		}
	}
	if hasMetadataIssue && !hasBlockingIssue {
		result.Verdict = "pass"
	}
}

func isMetadataIssue(issue domain.ReviewIssue) bool {
	field := strings.ToLower(strings.TrimSpace(issue.Field))
	switch field {
	case "difficulty", "cognitive_level", "cognitive", "exam_points", "outline_code", "profession", "system", "metadata":
		return true
	case "难度", "认知层次", "考核要点", "大纲代码", "专业", "系统", "元数据":
		return true
	}
	message := strings.TrimSpace(issue.Message)
	for _, keyword := range []string{"元数据", "认知层次", "考核要点", "难度系数", "大纲代码"} {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}

// checkResponse LLM 返回的检查结果结构。
type checkResponse struct {
	Verdict string `json:"verdict"`
	Scores  struct {
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
		ID:         fmt.Sprintf("air-%s-%d", questionID, time.Now().UnixNano()),
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
