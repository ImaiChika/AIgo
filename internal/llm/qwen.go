package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// QwenConfig 千问 API 配置。
type QwenConfig struct {
	APIKey      string        // API 密钥
	BaseURL     string        // 接口地址（OpenAI 兼容格式）
	Model       string        // 模型名称，如 qwen3.5-flash
	HTTPTimeout time.Duration // HTTP 超时时间
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

// Complete 调用千问 Chat Completions API（OpenAI 兼容格式）。
func (c *QwenClient) Complete(ctx context.Context, messages []Message, opts GenerateOptions) (string, error) {
	if c.cfg.APIKey == "" {
		return "", errors.New("缺少 DASHSCOPE_API_KEY 或 QWEN_API_KEY")
	}
	if len(messages) == 0 {
		return "", errors.New("messages cannot be empty")
	}

	// 构建 OpenAI 兼容请求体
	requestBody := chatRequest{
		Model:    firstNonEmpty(c.cfg.Model, "qwen3.5-flash"),
		Messages: messages,
	}
	if opts.Temperature > 0 {
		requestBody.Temperature = opts.Temperature
	}
	if opts.MaxTokens > 0 {
		requestBody.MaxTokens = opts.MaxTokens
	}

	payload, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	// 构建 HTTP 请求
	endpoint := strings.TrimRight(firstNonEmpty(c.cfg.BaseURL, "https://dashscope.aliyuncs.com/compatible-mode/v1"), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// 解析响应
	var decoded chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("qwen api status %d: %s", resp.StatusCode, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("qwen api returned no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

// chatRequest OpenAI 兼容请求体。
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
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

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
