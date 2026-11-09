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
)

// NetworkTools is an operator policy, never accepted from question requests or prompts.
type NetworkTools struct {
	WebSearchEnabled    bool `json:"web_search_enabled"`
	WebExtractorEnabled bool `json:"web_extractor_enabled"`
}

func (p NetworkTools) Validate() error {
	if p.WebExtractorEnabled && !p.WebSearchEnabled {
		return errors.New("网页抓取必须先开启联网搜索")
	}
	return nil
}

type VersionedNetworkToolsResolver interface {
	ResolveVersionedNetworkTools(context.Context) (NetworkTools, int64, error)
}

type NetworkToolsResolver interface {
	ResolveNetworkTools(context.Context) (NetworkTools, error)
}

type responsesOutputItem struct {
	Type, Role, Status string
	// Tool/reasoning payloads are heterogeneous; decode only the needed fields.
	Content json.RawMessage
	Action  json.RawMessage
	Output  json.RawMessage
}

// Flash search and extractor calls share Responses and its explicit tool events.
// The allowlist is built from the live policy; search alone never grants fetch.
func (c *QwenClient) completeWithNetworkTools(ctx context.Context, baseURL, model string, messages []Message, opts GenerateOptions) (string, error) {
	if opts.MedicalSearchHint {
		messages = medicalSearchHintMessages(messages, c.cfg.NetworkTools.WebExtractorEnabled)
	}
	tools := []map[string]string{{"type": "web_search"}}
	if c.cfg.NetworkTools.WebExtractorEnabled {
		tools = append(tools, map[string]string{"type": "web_extractor"})
	}
	body := struct {
		Model           string              `json:"model"`
		Input           []Message           `json:"input"`
		Tools           []map[string]string `json:"tools"`
		EnableThinking  bool                `json:"enable_thinking"`
		Temperature     *float64            `json:"temperature,omitempty"`
		MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	}{Model: model, Input: messages, Tools: tools, EnableThinking: true}
	if opts.Temperature > 0 || opts.TemperatureSet {
		body.Temperature = &opts.Temperature
	}
	if opts.MaxTokens > 0 {
		body.MaxOutputTokens = opts.MaxTokens
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	endpoint := baseURL
	if !strings.HasSuffix(endpoint, "/responses") {
		endpoint += "/responses"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(c.cfg.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	markModelPayload(ctx, payload, model)
	markModelRequest(ctx, "responses")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	recordModelResponse(ctx, resp.StatusCode, modelUsage{})
	recordUpstreamRequestID(ctx, resp.Header)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", readProviderHTTPError(ctx, resp, "模型工具请求失败")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return "", fmt.Errorf("读取工具响应失败: %w", err)
	}
	if len(raw) > 4<<20 {
		return "", errors.New("模型工具响应超过大小上限")
	}
	markModelResponseBody(ctx, raw)
	var result struct {
		Usage  modelUsage `json:"usage"`
		Status string     `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		Output []responsesOutputItem `json:"output"`
	}
	decodeErr := json.Unmarshal(raw, &result)
	recordModelResponse(ctx, resp.StatusCode, result.Usage)
	if decodeErr == nil {
		recordToolEvents(ctx, result.Output)
	}

	if decodeErr != nil {
		return "", fmt.Errorf("解析模型工具响应失败: %w", decodeErr)
	}
	if result.Error != nil {
		return "", fmt.Errorf("模型工具调用失败: %.500s", result.Error.Message)
	}
	if result.Status != "completed" {
		return "", fmt.Errorf("模型工具响应未完成（%s），未接收部分答案", result.Status)
	}
	var answer strings.Builder
	for _, item := range result.Output {
		if item.Type != "message" || (item.Role != "" && item.Role != "assistant") {
			continue
		}
		if item.Status != "" && item.Status != "completed" {
			return "", errors.New("模型答案未完成")
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item.Content, &parts); err != nil {
			return "", fmt.Errorf("解析模型最终答案失败: %w", err)
		}
		for _, part := range parts {
			if part.Type == "refusal" {
				return "", errors.New("模型拒绝了本次请求")
			}
			if part.Type == "output_text" {
				answer.WriteString(part.Text)
			}
		}
	}
	if strings.TrimSpace(answer.String()) == "" {
		return "", errors.New("模型工具响应没有最终答案")
	}
	return answer.String(), nil
}
