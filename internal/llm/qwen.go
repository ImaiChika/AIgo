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

type QwenConfig struct {
	APIKey      string
	BaseURL     string
	Model       string
	HTTPTimeout time.Duration
}

type QwenClient struct {
	cfg        QwenConfig
	httpClient *http.Client
}

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

func (c *QwenClient) Complete(ctx context.Context, messages []Message, opts GenerateOptions) (string, error) {
	if c.cfg.APIKey == "" {
		return "", errors.New("missing DASHSCOPE_API_KEY or QWEN_API_KEY")
	}
	if len(messages) == 0 {
		return "", errors.New("messages cannot be empty")
	}

	requestBody := chatRequest{
		Model:    firstNonEmpty(c.cfg.Model, "qwen-plus"),
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

	endpoint := strings.TrimRight(firstNonEmpty(c.cfg.BaseURL, "https://dashscope.aliyuncs.com/compatible-mode/v1"), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var decoded chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("qwen api status %d: %s", resp.StatusCode, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("qwen api returned no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
