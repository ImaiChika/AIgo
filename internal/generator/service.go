package generator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/workflowtrace"
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

// CurrentModel 返回实际活动生成客户端的脱敏模型名；不改变客户端的路由或配置。
func (s *Service) CurrentModel() string {
	if s == nil {
		return ""
	}
	if provider, ok := s.client.(llm.InfoProvider); ok {
		return strings.TrimSpace(provider.Info().Model)
	}
	return ""
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

	if req.PromptSnapshot != nil && req.PromptSnapshot.OutputPolicy != "" && !delegatedOutput(req) {
		return nil, fmt.Errorf("不支持的生成输出策略")
	}
	temperature := domain.DefaultGenerationTemperature
	if req.PromptSnapshot != nil && req.PromptSnapshot.GenerationTemperature != nil {
		temperature = *req.PromptSnapshot.GenerationTemperature
	}
	if err := domain.ValidateTemperature(temperature); err != nil {
		return nil, err
	}
	kp := req.KnowledgePoints[0]
	experiences, err := ExperiencesForPoint(req.PersonalExperiences, kp.ID)
	if err != nil {
		return nil, err
	}
	// 有效大纲已剔除基础医学范围（含迁移前存量任务）：在途任务以此终态停止，
	// 不烧重试、不换考点补生；判定不依赖生成策略快照版本。
	for _, point := range req.KnowledgePoints {
		if !domain.IsEligibleClinicalPoint(point) {
			return nil, fmt.Errorf("选题范围调整：%w", domain.ErrBasicMedicalScope)
		}
	}
	prepare := workflowtrace.Begin(ctx, "generation_prepare", -1, struct {
		Point      domain.KnowledgePoint
		Count      int
		Offset     int
		Difficulty domain.Difficulty
	}{kp, req.Count, req.WorkflowOffset, req.Difficulty})
	defer prepare.FailUnlessEnded("preparation_failed")

	// 构建提示词（包含完整知识点信息）
	systemPrompt, prompt, err := generationMessages(req, s.Brief)
	if err != nil {
		return nil, err
	}

	addition, bundle, plans, err := knowledgeWorkflowPrompt(req)
	if err != nil {
		return nil, err
	}
	usesKnowledgePlan := req.Workflow != nil && len(plans) > 0
	if usesKnowledgePlan {
		systemPrompt = knowledgeWorkflowSystemPrompt(systemPrompt, req.Workflow.Version)
		if knowledgebase.UsesClinicalPlan(req.Workflow.Version) {
			prompt = clinicalUserOutputContract(prompt)
		}
	}
	if !usesKnowledgePlan && currentPolicy(req) {
		systemPrompt, prompt = directNarrativeMessages(systemPrompt, prompt, req.Count, req.PromptSnapshot.PolicyVersion)
	}
	prompt += addition
	prompt += personalExperienceAddition(experiences)
	prompt += generationRetryReminder(ctx, req)

	workflowtrace.Materials(ctx, len(bundle.Cards), len(plans))
	workflowtrace.Points(ctx, len(req.KnowledgePoints))
	prepare.End("prepared", "", []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}, {Role: llm.RoleUser, Content: prompt}})
	// 调用千问 API
	version := "direct"
	if req.Workflow != nil {
		version = req.Workflow.Version
	}
	if experiences != nil {
		version += "+" + domain.ExperienceProtocolVersion
	}
	ctx = llm.WithCallWorkflow(ctx, version)
	callCtx, identity := llm.CaptureModelIdentity(ctx)
	modelStage := workflowtrace.Begin(ctx, "model_call", -1, nil)
	raw, err := s.client.Complete(callCtx, []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: temperature, TemperatureSet: temperature == 0, MedicalSearchHint: true})
	workflowtrace.LinkModel(ctx, identity.CallID, identity.Recorded)
	if err != nil {
		modelStage.End("failed", "model_call_failed", nil)
		return nil, err
	}
	modelStage.End("returned", "", raw)

	// 解析 JSON 响应
	parseStage := workflowtrace.Begin(ctx, "json_parse", -1, raw)
	defer parseStage.FailUnlessEnded("json_or_count_contract")
	items, err := parseQuestions(raw)
	if err != nil {
		logInvalidGenerationResponse(ctx, raw, req.Count, "json", err)
		return nil, fmt.Errorf("生成内容解析失败: %w", err)
	}

	// 转换为领域对象，自动从知识点填充元数据
	var questions []domain.A2Question
	if len(items) != req.Count {
		logInvalidGenerationResponse(ctx, raw, req.Count, "count", nil)
		return nil, fmt.Errorf("生成结果数量不一致: 请求%d道，返回%d道", req.Count, len(items))
	}
	parseStage.End("parsed", "", items)
	for index, item := range items {
		// 无大纲代码时用知识点主题做 ID 前缀，避免出现 "q--xxx" 空段
		idPrefix := SanitizeIDPrefix(kp.OutlineCode)
		if idPrefix == "" {
			idPrefix = SanitizeIDPrefix(kp.Topic)
		}
		if idPrefix == "" {
			idPrefix = "custom"
		}

		// Timestamp alone can collide within a multi-question response or concurrent
		// runs on coarse clocks. Keep the readable prefix and add 128 random bits.
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("生成题目标识失败: %w", err)
		}
		profession, system := QuestionMetadataForKnowledgePoint(kp, req.Subject)
		q := domain.A2Question{
			PersonalExperiences: experiences,
			ID:                  fmt.Sprintf("q-%s-%d-%s", idPrefix, time.Now().UnixNano(), hex.EncodeToString(nonce)),
			OutlineCode:         kp.OutlineCode, // 大纲代码
			Profession:          profession,     // 专业按请求或大纲系统映射填写
			System:              system,         // 系统按考试大纲名称填写
			Difficulty:          req.Difficulty, // 默认使用请求的难度
			KnowledgePoints:     []domain.KnowledgePoint{kp},
			Status:              domain.StatusAIDraft,
			Version:             1,
			FormatVersion:       domain.A2FormatSecondWave,
			CreatedAt:           time.Now(),
			UpdatedAt:           time.Now(),
		}
		if req.PromptSnapshot != nil && domain.UsesContextualCheckFormat(req.PromptSnapshot.PolicyVersion) {
			q.FormatVersion = domain.A2FormatContextual
			if domain.UsesFocusedCheckFormat(req.PromptSnapshot.PolicyVersion) {
				q.FormatVersion = domain.A2FormatFocused
			}
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
					if value, present := m["label"]; present && value != nil {
						if _, ok := value.(string); !ok {
							return nil, fmt.Errorf("选项标签类型无效")
						}
					}
					label, _ := m["label"].(string)
					text, _ := m["text"].(string)
					if (label != "" || delegatedOutput(req)) && text != "" {
						q.Options = append(q.Options, domain.Option{
							Label: label,
							Text:  text,
						})
					}
				}
			}
		}

		if delegatedOutput(req) {
			if err := fillOptionLabels(q.Options); err != nil {
				return nil, err
			}
		}
		if usesKnowledgePlan {
			if knowledgebase.UsesClinicalPlan(req.Workflow.Version) {
				q.NormalizeGeneratedA2()
			}
			basisStage := workflowtrace.Begin(ctx, "knowledge_validation", index, item)
			artifact, e := knowledgeGenerationContext(req, bundle, plans[index], item, q.Answer)
			if e != nil {
				basisStage.End("failed", "knowledge_contract", nil)
				logInvalidGenerationResponse(ctx, raw, req.Count, "knowledge_basis", e)
				return nil, e
			}
			basisStage.End("validated", "", artifact)
			q.GenerationContext = artifact
			q.WorkflowVersion = artifact.WorkflowVersion
		}

		renderStage := workflowtrace.Begin(ctx, "render", index, struct {
			Question domain.A2Question
			Basis    *domain.QuestionGenerationContext
		}{q, q.GenerationContext})
		if !usesKnowledgePlan && currentPolicy(req) {
			q.ClinicalStem, err = renderDirectNarrative(item)
			if err != nil {
				renderStage.End("failed", "narrative_contract", nil)
				logInvalidGenerationResponse(ctx, raw, req.Count, "case_plan", err)
				return nil, err
			}
		}
		if q.GenerationContext != nil && knowledgebase.UsesClinicalPlan(q.WorkflowVersion) {
			q.ClinicalStem, q.Explanation = knowledgebase.RenderClinicalPlan(q.GenerationContext.Basis.CasePlan, q.WorkflowVersion)
		}
		q.NormalizeGeneratedA2()
		if delegatedOutput(req) && !usesKnowledgePlan {
			q.Explanation, err = assembleExplanation(q.Explanation, q.Answer)
			if err != nil {
				return nil, err
			}
		}
		if !usesKnowledgePlan {
			q.Explanation = appendOptionalExpressionNotes(q.Explanation, bundle)
		}
		renderStage.End("rendered", "", q)
		contractStage := workflowtrace.Begin(ctx, "a2_validation", index, q)
		if err := q.ValidateGeneratedA2(); err != nil {
			contractStage.End("failed", "a2_contract", nil)
			logInvalidGenerationResponse(ctx, raw, req.Count, "a2_contract", err)
			return nil, fmt.Errorf("第%d道生成结果未通过A2专家规范: %w", index+1, err)
		}
		contractStage.End("validated", "", nil)
		questions = append(questions, q)
	}
	if req.Workflow != nil {
		slog.Info("knowledge_workflow.validated", "operation", llm.CallOperation(ctx), "version", req.Workflow.Version, "question_count", len(questions), "card_count", len(bundle.Cards))
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

func (s *Service) Ready(ctx context.Context) bool {
	if client, ok := s.client.(interface{ Ready(context.Context) bool }); ok {
		return client.Ready(ctx)
	}
	return true
}
func (s *Service) WaitReady(ctx context.Context) error {
	if client, ok := s.client.(interface{ WaitReady(context.Context) error }); ok {
		return client.WaitReady(ctx)
	}
	return ctx.Err()
}
