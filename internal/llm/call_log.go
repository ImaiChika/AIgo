package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

type modelUsage struct {
	CompletionDetails *reasoningUsage `json:"completion_tokens_details,omitempty"`
	OutputDetails     *reasoningUsage `json:"output_tokens_details,omitempty"`

	PromptTokens     int `json:"prompt_tokens"`
	InputTokens      int `json:"input_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	OutputTokens     int `json:"output_tokens"`
	TotalTokens      int `json:"total_tokens"`
	XTools           map[string]struct {
		Count *int `json:"count"`
	} `json:"x_tools"`
}

type reasoningUsage struct {
	ReasoningTokens *int `json:"reasoning_tokens"`
}

func (u modelUsage) reasoning() (int, bool) {
	for _, detail := range []*reasoningUsage{u.CompletionDetails, u.OutputDetails} {
		if detail != nil && detail.ReasoningTokens != nil {
			return *detail.ReasoningTokens, true
		}
	}
	return 0, false
}

type modelCallMeta struct {
	Stream               streamEvidence
	UpstreamRequestID    string
	RequestedTemperature *float64

	Protocol                                                       string
	Attempted                                                      bool
	Status                                                         int
	Usage                                                          modelUsage
	Events                                                         toolCallEvidence
	NetworkHint                                                    string
	InputHash, ResponseHash, ActualModel, FailureStage             string
	InputBytes, ResponseBytes                                      int
	ToolsRevision                                                  int64
	UsageReported                                                  bool
	InputTokensReported, OutputTokensReported, TotalTokensReported bool
	Thinking                                                       *bool
}

type toolCallEvidence struct {
	Observed                           bool
	SearchCalls, SearchCompleted       int
	ExtractorCalls, ExtractorCompleted int
	SearchSources, ExtractorOutputs    int
	ExtractorFailureNotices            int
}
type modelCallMetaKey struct{}
type modelCallOperationKey struct{}

// WithCallOperation correlates calls with server-generated run/job/question IDs.
func WithCallOperation(ctx context.Context, operation string) context.Context {
	return context.WithValue(ctx, modelCallOperationKey{}, operation)
}

func markModelRequest(ctx context.Context, protocol string) {
	if m, ok := ctx.Value(modelCallMetaKey{}).(*modelCallMeta); ok {
		m.Protocol = protocol
		m.Attempted = true
	}
}
func recordModelResponse(ctx context.Context, status int, usage modelUsage) {
	if m, ok := ctx.Value(modelCallMetaKey{}).(*modelCallMeta); ok {
		m.Status = status
		m.Usage = usage
	}
}

func recordToolEvents(ctx context.Context, items []responsesOutputItem) {
	m, ok := ctx.Value(modelCallMetaKey{}).(*modelCallMeta)
	if !ok || items == nil {
		return
	}
	m.Events = toolEvents(items)
}

func toolEvents(items []responsesOutputItem) toolCallEvidence {
	e := toolCallEvidence{Observed: items != nil}
	for _, item := range items {
		switch item.Type {
		case "web_search_call":
			e.SearchCalls++
			if item.Status != "completed" {
				continue
			}
			e.SearchCompleted++
			var action struct{ Sources []json.RawMessage }
			if json.Unmarshal(item.Action, &action) == nil {
				for _, raw := range action.Sources {
					var source struct{ URL string }
					_ = json.Unmarshal(raw, &source)
					if source.URL == "" {
						_ = json.Unmarshal(raw, &source.URL)
					}
					u, err := url.Parse(source.URL)
					if err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
						e.SearchSources++
					}
				}
			}
		case "web_extractor_call":
			e.ExtractorCalls++
			if item.Status != "completed" {
				continue
			}
			e.ExtractorCompleted++
			switch strings.TrimSpace(string(item.Output)) {
			case "", "null", `""`, "{}", "[]":
			default:
				e.ExtractorOutputs++
			}
			// The provider can mark a call completed while returning an access
			// failure notice, sometimes mixed with another page's text. Count
			// this signal separately; nonempty is not proof of usable evidence.
			if strings.Contains(strings.ToLower(string(item.Output)), "the provided webpage content could not be accessed") ||
				strings.Contains(strings.ToLower(string(item.Output)), "the webpage content could not be processed") {
				e.ExtractorFailureNotices++
			}
		}
	}
	return e
}

func (u modelUsage) toolCount(keys ...string) (count int, reported bool) {
	for _, key := range keys {
		if v, ok := u.XTools[key]; ok && v.Count != nil {
			count = max(count, *v.Count)
			reported = true
		}
	}
	return
}
func logModelCall(ctx context.Context, purpose ConfigPurpose, cfg QwenConfig, m *modelCallMeta, started time.Time, err error) {
	kind := modelErrorKind(err)
	input := m.Usage.InputTokens
	if input == 0 {
		input = m.Usage.PromptTokens
	}
	output := m.Usage.OutputTokens
	if output == 0 {
		output = m.Usage.CompletionTokens
	}
	cloud := normalizedDeployment(cfg.Deployment) == DeploymentCloud
	searchCount, searchReported := m.Usage.toolCount("search", "web_search")
	extractorCount, extractorReported := m.Usage.toolCount("web_extractor")
	model := cfg.Model
	if m.ActualModel != "" {
		model = m.ActualModel
	}
	var requestedTemperature any
	if m.RequestedTemperature != nil {
		requestedTemperature = *m.RequestedTemperature
	}
	reasoning, reasoningReported := m.Usage.reasoning()
	attrs := []any{"purpose", purpose, "model", model, "deployment", normalizedDeployment(cfg.Deployment), "protocol", m.Protocol, "http_attempted", m.Attempted, "status", m.Status, "duration_ms", time.Since(started).Milliseconds(), "web_search", cloud && cfg.NetworkTools.WebSearchEnabled, "web_extractor", cloud && cfg.NetworkTools.WebExtractorEnabled, "input_tokens", input, "output_tokens", output, "total_tokens", m.Usage.TotalTokens,
		"requested_temperature", requestedTemperature, "upstream_request_id", m.UpstreamRequestID, "stream_finish_reason", m.Stream.FinishReason, "streamed", m.Stream.Streamed, "stream_chunks", m.Stream.Chunks, "first_reasoning_ms", m.Stream.FirstReasoningMS, "first_answer_ms", m.Stream.FirstAnswerMS, "last_stream_content_ms", m.Stream.LastContentMS, "max_stream_gap_ms", m.Stream.MaxGapMS, "reasoning_characters", m.Stream.ReasoningCharacters, "reasoning_tokens", reasoning, "reasoning_tokens_reported", reasoningReported,
		"reported_search_count", searchCount, "reported_extractor_count", extractorCount,
		"search_usage_reported", searchReported, "extractor_usage_reported", extractorReported,
		"tool_events_observed", m.Events.Observed, "search_calls_observed", m.Events.SearchCalls, "search_calls_completed", m.Events.SearchCompleted,
		"search_sources_returned", m.Events.SearchSources, "extractor_calls_observed", m.Events.ExtractorCalls, "extractor_calls_completed", m.Events.ExtractorCompleted, "extractor_outputs_nonempty", m.Events.ExtractorOutputs, "error_kind", kind}
	attempt, _ := ctx.Value(callAttemptKey{}).(int)
	version, _ := ctx.Value(callWorkflowKey{}).(string)
	attrs = append(attrs, "attempt", attempt, "workflow_version", version, "config_id", cfg.ConfigID, "config_updated_at", cfg.ConfigUpdatedAt, "config_source", cfg.ConfigSource, "tools_revision", m.ToolsRevision, "usage_reported", m.UsageReported, "input_tokens_reported", m.InputTokensReported, "output_tokens_reported", m.OutputTokensReported, "total_tokens_reported", m.TotalTokensReported, "failure_stage", m.FailureStage)
	operation, _ := ctx.Value(modelCallOperationKey{}).(string)
	attrs = append(attrs, "operation", operation)
	attrs = append(attrs, "extractor_outputs_with_failure_notice", m.Events.ExtractorFailureNotices)
	if m.NetworkHint != "" {
		attrs = append(attrs, "network_hint", m.NetworkHint, "network_hint_policy", medicalSearchHintPolicy)
	}
	// No prompt, answer, source URL, endpoint, credential or provider error text.
	if err != nil {
		slog.WarnContext(ctx, "model_call", attrs...)
	} else {
		slog.InfoContext(ctx, "model_call", attrs...)
	}
}

// CallOperation exposes only the run/job correlation ID for worker logs.
func CallOperation(ctx context.Context) string {
	value, _ := ctx.Value(modelCallOperationKey{}).(string)
	return value
}
