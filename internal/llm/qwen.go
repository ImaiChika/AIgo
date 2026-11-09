package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultDashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// QwenConfig 千问 OpenAI-compatible 推理端点配置。
// cloud 对应百炼等云端服务，local 对应 vLLM/SGLang 等本地服务。
type QwenConfig struct {
	ConfigID        string
	ConfigUpdatedAt time.Time
	ConfigSource    string
	NetworkTools    NetworkTools   // 系统配额页解析的联网权限；零值不授予工具
	Deployment      DeploymentMode // cloud（默认）或 local
	APIKey          string         // 云端必填；本地按部署需要选填
	BaseURL         string         // OpenAI-compatible base URL，通常以 /v1 结尾
	Model           string         // 云端模型 ID 或本地 served-model-name
	EnableThinking  *bool          // nil=使用服务默认值
	HTTPTimeout     time.Duration  // HTTP 超时时间
}

// QwenClient 千问 API 客户端，使用 OpenAI 兼容接口。
type QwenClient struct {
	cfg        QwenConfig
	httpClient *http.Client
}

// NewQwenClient 创建千问客户端实例。
func NewQwenClient(cfg QwenConfig) *QwenClient {
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &QwenClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Info 返回不含凭证的运行信息。
func (c *QwenClient) Info() ClientInfo {
	return ClientInfo{
		Deployment:     normalizedDeployment(c.cfg.Deployment),
		Protocol:       "openai-compatible",
		Model:          strings.TrimSpace(c.cfg.Model),
		BaseURL:        strings.TrimSpace(c.cfg.BaseURL),
		AuthConfigured: strings.TrimSpace(c.cfg.APIKey) != "",
	}
}

// Complete 调用千问 Chat Completions API（OpenAI-compatible 格式）。
// 云端模式要求 API Key；本地模式允许由内网、网关或 mTLS 负责鉴权。
func (c *QwenClient) Complete(ctx context.Context, messages []Message, opts GenerateOptions) (string, error) {
	deployment := normalizedDeployment(c.cfg.Deployment)
	if deployment != DeploymentCloud && deployment != DeploymentLocal {
		return "", fmt.Errorf("不支持的 QWEN_DEPLOYMENT: %q", c.cfg.Deployment)
	}
	if deployment == DeploymentCloud && strings.TrimSpace(c.cfg.APIKey) == "" {
		return "", errors.New("缺少 AI 服务 API_KEY（兼容配置项 DASHSCOPE_API_KEY / QWEN_API_KEY；请在系统管理 → AI 服务配置中填写）")
	}
	if len(messages) == 0 {
		return "", errors.New("messages cannot be empty")
	}

	baseURL := strings.TrimSpace(c.cfg.BaseURL)
	if baseURL == "" {
		if deployment == DeploymentCloud {
			baseURL = defaultDashScopeBaseURL
		} else {
			return "", errors.New("本地推理缺少 API 地址（兼容配置项 QWEN_BASE_URL；请在系统管理 → AI 服务配置中填写）")
		}
	}
	model := strings.TrimSpace(c.cfg.Model)
	if model == "" {
		if deployment == DeploymentCloud {
			model = "qwen3.5-flash"
		} else {
			return "", errors.New("本地推理缺少模型名称（兼容配置项 QWEN_MODEL；请填写服务实际暴露的模型名）")
		}
	}

	if err := c.cfg.NetworkTools.Validate(); err != nil {
		return "", err
	}
	if opts.MedicalSearchHint {
		switch {
		case deployment != DeploymentCloud || !c.cfg.NetworkTools.WebSearchEnabled:
			markMedicalSearchHint(ctx, "disabled")
			opts.MedicalSearchHint = false
		case !flashResponsesSearch(model):
			// Other compatible models retain their existing messages/routing.
			markMedicalSearchHint(ctx, "unsupported_model")
			opts.MedicalSearchHint = false
		default:
			markMedicalSearchHint(ctx, "enabled")
		}
	}
	if deployment == DeploymentCloud && (c.cfg.NetworkTools.WebExtractorEnabled ||
		(c.cfg.NetworkTools.WebSearchEnabled && flashResponsesSearch(model))) {
		return c.completeWithNetworkTools(ctx, baseURL, model, messages, opts)
	}
	// 构建 OpenAI 兼容请求体
	// Only the currently verified cloud Flash family opts into the new transport.
	// Other cloud/local configurations keep their previous one-request behavior.
	streamChat := opts.StreamChat && deployment == DeploymentCloud && summaryChatStreamModel(model)
	requestBody := chatRequest{
		Model:    model,
		Messages: messages,
	}
	if deployment == DeploymentCloud && c.cfg.NetworkTools.WebSearchEnabled {
		enabled := true
		requestBody.EnableSearch = &enabled
		requestBody.IncludeToolUsage = true
	}
	if opts.Temperature > 0 || opts.TemperatureSet {
		requestBody.Temperature = &opts.Temperature
	}
	if opts.MaxTokens > 0 {
		requestBody.MaxTokens = opts.MaxTokens
	}
	if streamChat {
		requestBody.Stream = true
		requestBody.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	}
	thinking := opts.EnableThinking
	if thinking == nil {
		thinking = c.cfg.EnableThinking
	}
	if thinking != nil {
		if deployment == DeploymentLocal {
			// Qwen 开源权重经 vLLM/SGLang 部署时，思考开关属于 chat template 参数。
			requestBody.ChatTemplateKwargs = map[string]any{"enable_thinking": *thinking}
		} else {
			// 百炼将 enable_thinking 作为请求体顶层扩展字段。
			requestBody.EnableThinking = thinking
		}
	}

	payload, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	// 构建 HTTP 请求
	endpoint := chatCompletionsEndpoint(baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(c.cfg.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")
	if streamChat {
		req.Header.Set("Accept", "text/event-stream")
	}

	// 发送请求
	markModelPayload(ctx, payload, model)
	markModelRequest(ctx, "chat_completions")
	started := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	recordModelResponse(ctx, resp.StatusCode, modelUsage{})
	recordUpstreamRequestID(ctx, resp.Header)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", readProviderHTTPError(ctx, resp, "模型服务拒绝请求")
	}
	if streamChat && resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return readChatStream(ctx, resp.Body, resp.StatusCode, started)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return "", fmt.Errorf("读取推理响应失败: %w", err)
	}
	if len(body) > 4<<20 {
		return "", errors.New("模型响应超过大小上限")
	}

	markModelResponseBody(ctx, body)
	var decoded chatResponse
	decodeErr := json.Unmarshal(body, &decoded)
	recordModelResponse(ctx, resp.StatusCode, decoded.Usage)

	if decodeErr != nil {
		return "", fmt.Errorf("解析推理响应失败: %w", decodeErr)
	}
	if len(decoded.Choices) != 1 {
		return "", errors.New("模型须返回唯一的最终响应")
	}
	choice := decoded.Choices[0]
	if choice.FinishReason != "" && choice.FinishReason != "stop" {
		return "", fmt.Errorf("模型最终响应未正常完成（%s）", choice.FinishReason)
	}
	if choice.Message.Refusal != "" {
		return "", errors.New("模型拒绝了本次请求")
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return "", errors.New("模型响应没有最终答案")
	}
	return decoded.Choices[0].Message.Content, nil
}

// Only switch the verified Flash family; other cloud/local compatible models
// retain their existing protocol. Snapshot names use the same tool contract.
func flashResponsesSearch(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "qwen3.5-flash" || strings.HasPrefix(model, "qwen3.5-flash-")
}

// chatRequest OpenAI 兼容请求体。
type chatRequest struct {
	Stream             bool               `json:"stream,omitempty"`
	StreamOptions      *chatStreamOptions `json:"stream_options,omitempty"`
	EnableSearch       *bool              `json:"enable_search,omitempty"`
	IncludeToolUsage   bool               `json:"include_tool_usage,omitempty"`
	Model              string             `json:"model"`
	Messages           []Message          `json:"messages"`
	Temperature        *float64           `json:"temperature,omitempty"`
	MaxTokens          int                `json:"max_tokens,omitempty"`
	EnableThinking     *bool              `json:"enable_thinking,omitempty"`
	ChatTemplateKwargs map[string]any     `json:"chat_template_kwargs,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatResponse OpenAI 兼容响应体。
type chatResponse struct {
	Usage   modelUsage `json:"usage"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role    Role   `json:"role"`
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func normalizedDeployment(mode DeploymentMode) DeploymentMode {
	if mode == "" {
		return DeploymentCloud
	}
	return DeploymentMode(strings.ToLower(strings.TrimSpace(string(mode))))
}

func chatCompletionsEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/chat/completions") {
		return baseURL
	}
	return baseURL + "/chat/completions"
}

// Summary-only transport support; generation/check Responses routing stays unchanged.
func summaryChatStreamModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return flashResponsesSearch(model) || model == "qwen3.6-flash" || strings.HasPrefix(model, "qwen3.6-flash-") || model == "qwen3.8-flash" || strings.HasPrefix(model, "qwen3.8-flash-")
}
