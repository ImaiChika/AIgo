package llm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const maxChatContentBytes = 4 << 20

type streamEvidence struct {
	FinishReason                              string
	Streamed                                  bool
	Chunks                                    int
	ReasoningCharacters                       int
	FirstReasoningMS, FirstAnswerMS, MaxGapMS int64
	LastContentMS                             int64
}

// Only a bounded identifier is kept; never persist arbitrary upstream headers.
func recordUpstreamRequestID(ctx context.Context, header http.Header) {
	m, ok := ctx.Value(modelCallMetaKey{}).(*modelCallMeta)
	if !ok {
		return
	}
	for _, key := range []string{"X-Request-ID", "X-DashScope-Request-ID"} {
		id := strings.TrimSpace(header.Get(key))
		if len(id) == 0 || len(id) > 160 {
			continue
		}
		valid := true
		for _, c := range id {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
				valid = false
				break
			}
		}
		if valid {
			m.UpstreamRequestID = id
			return
		}
	}
}

type streamMeter struct {
	io.Reader
	hash  io.Writer
	bytes int
}

func (m *streamMeter) Read(p []byte) (int, error) {
	n, e := m.Reader.Read(p)
	if n > 0 {
		m.hash.Write(p[:n])
		m.bytes += n
	}
	return n, e
}

// readChatStream keeps only final answer text. Reasoning is counted and discarded.
// JSON fallback is handled by Complete in the same HTTP request; this reader never
// retries a paid call or returns partial content after a transport failure.
func readChatStream(ctx context.Context, body io.Reader, status int, started time.Time) (string, error) {
	hash := sha256.New()
	meter := &streamMeter{Reader: body, hash: hash}
	meta, _ := ctx.Value(modelCallMetaKey{}).(*modelCallMeta)
	if meta != nil {
		meta.Stream.Streamed = true
		defer func() { meta.ResponseHash = hex.EncodeToString(hash.Sum(nil)); meta.ResponseBytes = meter.bytes }()
	}
	scanner := bufio.NewScanner(meter)
	scanner.Buffer(make([]byte, 4096), maxChatContentBytes+1024)
	var data strings.Builder
	event := ""
	firstLine := true
	answer := strings.Builder{}
	logicalBytes := 0
	finished := false
	lastEvent := time.Time{}
	consume := func() (bool, error) {
		raw := data.String()
		data.Reset()
		kind := event
		event = ""
		if raw == "" {
			return false, nil
		}
		if kind == "error" {
			return false, errors.New("模型流式返回错误")
		}
		if strings.TrimSpace(raw) == "[DONE]" {
			if !finished || strings.TrimSpace(answer.String()) == "" {
				return false, errors.New("模型流式响应没有完整最终答案")
			}
			return true, nil
		}
		var packet struct {
			Error   json.RawMessage `json:"error"`
			Usage   json.RawMessage `json:"usage"`
			Choices []struct {
				Index  int    `json:"index"`
				Finish string `json:"finish_reason"`
				Delta  struct {
					Content   string          `json:"content"`
					Reasoning string          `json:"reasoning_content"`
					Refusal   string          `json:"refusal"`
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(raw), &packet) != nil {
			return false, errors.New("模型流式事件格式无效")
		}
		if len(packet.Error) > 0 && string(packet.Error) != "null" {
			return false, errors.New("模型流式返回错误")
		}
		now := time.Now()
		if meta != nil {
			meta.Stream.Chunks++
			if !lastEvent.IsZero() {
				meta.Stream.MaxGapMS = max(meta.Stream.MaxGapMS, now.Sub(lastEvent).Milliseconds())
			}
		}
		lastEvent = now
		// Usage is optional and may arrive in a separate tail event with no choices.
		if len(packet.Usage) > 0 && string(packet.Usage) != "null" {
			var usage modelUsage
			if json.Unmarshal(packet.Usage, &usage) == nil {
				recordModelResponse(ctx, status, usage)
				markModelUsage(ctx, packet.Usage)
			}
		}
		if len(packet.Choices) > 1 {
			return false, errors.New("模型须返回唯一的最终响应")
		}
		for _, choice := range packet.Choices {
			if choice.Index != 0 {
				return false, errors.New("模型须返回唯一的最终响应")
			}
			if choice.Delta.Refusal != "" {
				return false, errors.New("模型拒绝了本次请求")
			}
			if len(choice.Delta.ToolCalls) > 0 && strings.TrimSpace(string(choice.Delta.ToolCalls)) != "null" {
				var calls []json.RawMessage
				if json.Unmarshal(choice.Delta.ToolCalls, &calls) != nil || len(calls) != 0 {
					return false, errors.New("模型流式响应不是最终文本")
				}
			}
			if finished && (choice.Delta.Content != "" || choice.Delta.Reasoning != "" || choice.Finish != "") {
				return false, errors.New("模型结束后仍返回额外内容")
			}
			logicalBytes += len(choice.Delta.Content) + len(choice.Delta.Reasoning)
			if logicalBytes > maxChatContentBytes {
				return false, errors.New("模型响应超过大小上限")
			}
			if meta != nil && (choice.Delta.Content != "" || choice.Delta.Reasoning != "") {
				meta.Stream.LastContentMS = max(int64(1), now.Sub(started).Milliseconds())
			}
			if choice.Delta.Reasoning != "" && meta != nil && meta.Stream.FirstReasoningMS == 0 {
				meta.Stream.FirstReasoningMS = max(int64(1), now.Sub(started).Milliseconds())
			}
			if meta != nil {
				meta.Stream.ReasoningCharacters += utf8.RuneCountInString(choice.Delta.Reasoning)
			}
			if choice.Delta.Content != "" {
				if meta != nil && meta.Stream.FirstAnswerMS == 0 {
					meta.Stream.FirstAnswerMS = max(int64(1), now.Sub(started).Milliseconds())
				}
				answer.WriteString(choice.Delta.Content)
			}
			if choice.Finish != "" {
				if meta != nil {
					switch choice.Finish {
					case "stop", "length", "content_filter", "tool_calls", "function_call":
						meta.Stream.FinishReason = choice.Finish
					default:
						meta.Stream.FinishReason = "other"
					}
				}
				if choice.Finish != "stop" {
					return false, errors.New("模型流式最终响应未正常完成")
				}
				finished = true
			}
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		if line == "" {
			done, e := consume()
			if e != nil {
				return "", e
			}
			if done {
				return answer.String(), nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			if data.Len()+len(value) > maxChatContentBytes {
				return "", errors.New("模型流式事件超过大小上限")
			}
			data.WriteString(value)
		}
	}
	if e := scanner.Err(); e != nil {
		return "", fmt.Errorf("读取模型流式响应失败: %w", e)
	}
	if data.Len() > 0 {
		done, e := consume()
		if e != nil {
			return "", e
		}
		if done {
			return answer.String(), nil
		}
	}
	return "", errors.New("模型流式响应中断，未收到完整结束标记")
}
