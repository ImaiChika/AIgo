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
	Deployment     DeploymentMode // cloud（默认）或 local
	APIKey         string         // 云端必填；本地按部署需要选填
	BaseURL        string         // OpenAI-compatible base URL，通常以 /v1 结尾
	Model          string         // 云端模型 ID 或本地 served-model-name
	EnableThinking *bool          // nil=使用服务默认值
	HTTPTimeout    time.Duration  // HTTP 超时时间
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

	// 构建 OpenAI 兼容请求体
	requestBody := chatRequest{
		Model:    model,
		Messages: messages,
	}
	if opts.Temperature > 0 {
		requestBody.Temperature = opts.Temperature
	}
	if opts.MaxTokens > 0 {
		requestBody.MaxTokens = opts.MaxTokens
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

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("读取推理响应失败: %w", err)
	}

	var decoded chatResponse
	decodeErr := json.Unmarshal(body, &decoded)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(decoded.Error.Message)
		if message == "" {
			message = strings.TrimSpace(string(body))
		}
		if len(message) > 500 {
			message = message[:500]
		}
		return "", fmt.Errorf("推理接口 HTTP %d: %s", resp.StatusCode, message)
	}
	if decodeErr != nil {
		return "", fmt.Errorf("解析推理响应失败: %w", decodeErr)
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("qwen api returned no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

// chatRequest OpenAI 兼容请求体。
type chatRequest struct {
	Model              string         `json:"model"`
	Messages           []Message      `json:"messages"`
	Temperature        float64        `json:"temperature,omitempty"`
	MaxTokens          int            `json:"max_tokens,omitempty"`
	EnableThinking     *bool          `json:"enable_thinking,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

// chatResponse OpenAI 兼容响应体。
type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
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
