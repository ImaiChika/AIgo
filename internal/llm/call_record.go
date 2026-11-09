package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// ModelCallRecord is transport evidence, never a question-quality verdict.
// No endpoint, credentials, prompts, answers, URLs or provider error text are stored.
type ModelCallRecord struct {
	RequestedTemperature *float64 `json:"requested_temperature,omitempty"`

	UpstreamRequestID  string `json:"upstream_request_id,omitempty"`
	StreamFinishReason string `json:"stream_finish_reason,omitempty"`

	Streamed                bool  `json:"streamed,omitempty"`
	StreamChunks            int   `json:"stream_chunks,omitempty"`
	ReasoningCharacters     int   `json:"reasoning_characters,omitempty"`
	FirstReasoningMS        int64 `json:"first_reasoning_ms,omitempty"`
	FirstAnswerMS           int64 `json:"first_answer_ms,omitempty"`
	LastStreamContentMS     int64 `json:"last_stream_content_ms,omitempty"`
	MaxStreamGapMS          int64 `json:"max_stream_gap_ms,omitempty"`
	ReasoningTokens         int   `json:"reasoning_tokens,omitempty"`
	ReasoningTokensReported bool  `json:"reasoning_tokens_reported,omitempty"`

	InputTokensReported     bool           `json:"input_tokens_reported"`
	OutputTokensReported    bool           `json:"output_tokens_reported"`
	TotalTokensReported     bool           `json:"total_tokens_reported"`
	EnableThinking          *bool          `json:"enable_thinking,omitempty"`
	ID                      string         `json:"id"`
	Operation               string         `json:"operation"`
	Purpose                 ConfigPurpose  `json:"purpose"`
	Stage                   string         `json:"stage"`
	Attempt                 int            `json:"attempt"`
	WorkflowVersion         string         `json:"workflow_version"`
	StartedAt               time.Time      `json:"started_at"`
	DurationMS              int64          `json:"duration_ms"`
	Outcome                 string         `json:"outcome"`
	ErrorKind               string         `json:"error_kind"`
	FailureStage            string         `json:"failure_stage,omitempty"`
	Model                   string         `json:"model"`
	Deployment              DeploymentMode `json:"deployment"`
	ConfigID                string         `json:"config_id,omitempty"`
	ConfigUpdatedAt         *time.Time     `json:"config_updated_at,omitempty"`
	ConfigSource            string         `json:"config_source"`
	ConfigFingerprint       string         `json:"config_fingerprint"`
	ToolsRevision           int64          `json:"tools_revision"`
	WebSearch               bool           `json:"web_search"`
	WebExtractor            bool           `json:"web_extractor"`
	Protocol                string         `json:"protocol"`
	HTTPAttempted           bool           `json:"http_attempted"`
	HTTPStatus              int            `json:"http_status"`
	InputSHA256             string         `json:"input_sha256,omitempty"`
	ResponseSHA256          string         `json:"response_sha256,omitempty"`
	OutputSHA256            string         `json:"output_sha256,omitempty"`
	InputBytes              int            `json:"input_bytes"`
	ResponseBytes           int            `json:"response_bytes"`
	OutputBytes             int            `json:"output_bytes"`
	UsageReported           bool           `json:"usage_reported"`
	InputTokens             int            `json:"input_tokens"`
	OutputTokens            int            `json:"output_tokens"`
	TotalTokens             int            `json:"total_tokens"`
	SearchUsageReported     bool           `json:"search_usage_reported"`
	ExtractorUsageReported  bool           `json:"extractor_usage_reported"`
	ReportedSearchCount     int            `json:"reported_search_count"`
	ReportedExtractorCount  int            `json:"reported_extractor_count"`
	ToolEventsObserved      bool           `json:"tool_events_observed"`
	SearchCalls             int            `json:"search_calls"`
	SearchCompleted         int            `json:"search_completed"`
	SearchSources           int            `json:"search_sources"`
	ExtractorCalls          int            `json:"extractor_calls"`
	ExtractorCompleted      int            `json:"extractor_completed"`
	ExtractorOutputs        int            `json:"extractor_outputs"`
	ExtractorFailureNotices int            `json:"extractor_failure_notices"`
}

// ModelCallRecorder is wired before workers start. Persistence failures must never
// retry a successful model request, bypass first-check or change a task outcome.
type ModelCallRecorder interface {
	SaveModelCall(context.Context, ModelCallRecord) error
}

// ModelIdentity is local to one synchronous call, never shared by concurrent calls.
type ModelIdentity struct {
	Model    string
	CallID   string
	Recorded bool
}
type modelIdentityKey struct{}

func CaptureModelIdentity(ctx context.Context) (context.Context, *ModelIdentity) {
	value := &ModelIdentity{}
	return context.WithValue(ctx, modelIdentityKey{}, value), value
}
func captureModelIdentity(ctx context.Context, cfg QwenConfig, m *modelCallMeta) {
	if value, ok := ctx.Value(modelIdentityKey{}).(*ModelIdentity); ok {
		value.Model = cfg.Model
		if m.ActualModel != "" {
			value.Model = m.ActualModel
		}
	}
}

type callAttemptKey struct{}
type callWorkflowKey struct{}

func WithCallAttempt(ctx context.Context, attempt int) context.Context {
	return context.WithValue(ctx, callAttemptKey{}, attempt)
}
func WithCallWorkflow(ctx context.Context, version string) context.Context {
	if version == "" {
		version = "direct"
	}
	return context.WithValue(ctx, callWorkflowKey{}, version)
}
func (c *DynamicQwenClient) SetCallRecorder(r ModelCallRecorder) { c.recorder = r }
func hashBytes(raw []byte) string                                { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func markModelPayload(ctx context.Context, raw []byte, model string) {
	if m, ok := ctx.Value(modelCallMetaKey{}).(*modelCallMeta); ok {
		m.InputHash = hashBytes(raw)
		m.InputBytes = len(raw)
		m.ActualModel = model
		var wire struct {
			Temperature *float64 `json:"temperature"`
			Thinking    *bool    `json:"enable_thinking"`
			Template    struct {
				Thinking *bool `json:"enable_thinking"`
			} `json:"chat_template_kwargs"`
		}
		if json.Unmarshal(raw, &wire) == nil {
			m.RequestedTemperature = wire.Temperature
			m.Thinking = wire.Thinking
			if wire.Template.Thinking != nil {
				m.Thinking = wire.Template.Thinking
			}
		}
	}
}
func markModelResponseBody(ctx context.Context, raw []byte) {
	if m, ok := ctx.Value(modelCallMetaKey{}).(*modelCallMeta); ok {
		m.ResponseHash = hashBytes(raw)
		m.ResponseBytes = len(raw)
		var body struct {
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(raw, &body) == nil {
			markModelUsage(ctx, body.Usage)
		}
	}
}

func markModelUsage(ctx context.Context, raw json.RawMessage) {
	if m, ok := ctx.Value(modelCallMetaKey{}).(*modelCallMeta); ok {
		if len(raw) > 0 && string(raw) != "null" {
			m.UsageReported = true
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) == nil {
				present := func(keys ...string) bool {
					for _, key := range keys {
						if v, ok := fields[key]; ok && string(v) != "null" {
							return true
						}
					}
					return false
				}
				m.InputTokensReported = present("input_tokens", "prompt_tokens")
				m.OutputTokensReported = present("output_tokens", "completion_tokens")
				m.TotalTokensReported = present("total_tokens")
			}
		}
	}
}
func modelErrorKind(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, errRuntimeConfigResolution) {
		return "config_resolution"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return "upstream_http"
	}
	return "response_error"
}
func (c *DynamicQwenClient) recordCall(ctx context.Context, cfg QwenConfig, m *modelCallMeta, started time.Time, answer string, callErr error) {
	if c.recorder == nil {
		return
	}
	// Do not spend a tail-write budget when a successful call is already near
	// its task deadline. Missing diagnostic evidence is preferable to a new retry.
	if callErr == nil {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < 250*time.Millisecond {
			slog.WarnContext(ctx, "model_call.record_skipped", "operation", CallOperation(ctx), "reason", "task_budget_low")
			return
		}
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		slog.WarnContext(ctx, "model_call.record_failed", "reason", "id_unavailable")
		return
	}
	attempt, _ := ctx.Value(callAttemptKey{}).(int)
	version, _ := ctx.Value(callWorkflowKey{}).(string)
	// Hash a nonsecret routing description. The raw endpoint is deliberately absent.
	config, _ := json.Marshal(struct {
		Model      string
		Deployment DeploymentMode
		Endpoint   string
		Thinking   *bool
		Timeout    time.Duration
	}{cfg.Model, normalizedDeployment(cfg.Deployment), cfg.BaseURL, cfg.EnableThinking, cfg.HTTPTimeout})
	id := hex.EncodeToString(nonce)
	if identity, ok := ctx.Value(modelIdentityKey{}).(*ModelIdentity); ok {
		identity.CallID = id
	}
	r := ModelCallRecord{
		RequestedTemperature:    m.RequestedTemperature,
		UpstreamRequestID:       m.UpstreamRequestID,
		StreamFinishReason:      m.Stream.FinishReason,
		Streamed:                m.Stream.Streamed,
		StreamChunks:            m.Stream.Chunks,
		ReasoningCharacters:     m.Stream.ReasoningCharacters,
		FirstReasoningMS:        m.Stream.FirstReasoningMS,
		FirstAnswerMS:           m.Stream.FirstAnswerMS,
		LastStreamContentMS:     m.Stream.LastContentMS,
		MaxStreamGapMS:          m.Stream.MaxGapMS,
		EnableThinking:          m.Thinking,
		InputTokensReported:     m.InputTokensReported,
		OutputTokensReported:    m.OutputTokensReported,
		TotalTokensReported:     m.TotalTokensReported,
		ID:                      id,
		Operation:               CallOperation(ctx),
		Purpose:                 c.purpose,
		Stage:                   "model_call",
		Attempt:                 attempt,
		WorkflowVersion:         version,
		StartedAt:               started.UTC(),
		DurationMS:              time.Since(started).Milliseconds(),
		Outcome:                 "returned",
		ErrorKind:               modelErrorKind(callErr),
		FailureStage:            m.FailureStage,
		Model:                   cfg.Model,
		Deployment:              normalizedDeployment(cfg.Deployment),
		ConfigID:                cfg.ConfigID,
		ConfigSource:            cfg.ConfigSource,
		ConfigFingerprint:       hashBytes(config),
		ToolsRevision:           m.ToolsRevision,
		WebSearch:               normalizedDeployment(cfg.Deployment) == DeploymentCloud && cfg.NetworkTools.WebSearchEnabled,
		WebExtractor:            normalizedDeployment(cfg.Deployment) == DeploymentCloud && cfg.NetworkTools.WebExtractorEnabled,
		Protocol:                m.Protocol,
		HTTPAttempted:           m.Attempted,
		HTTPStatus:              m.Status,
		InputSHA256:             m.InputHash,
		InputBytes:              m.InputBytes,
		ResponseSHA256:          m.ResponseHash,
		ResponseBytes:           m.ResponseBytes,
		OutputBytes:             len(answer),
		UsageReported:           m.UsageReported,
		InputTokens:             m.Usage.InputTokens,
		OutputTokens:            m.Usage.OutputTokens,
		TotalTokens:             m.Usage.TotalTokens,
		ToolEventsObserved:      m.Events.Observed,
		SearchCalls:             m.Events.SearchCalls,
		SearchCompleted:         m.Events.SearchCompleted,
		SearchSources:           m.Events.SearchSources,
		ExtractorCalls:          m.Events.ExtractorCalls,
		ExtractorCompleted:      m.Events.ExtractorCompleted,
		ExtractorOutputs:        m.Events.ExtractorOutputs,
		ExtractorFailureNotices: m.Events.ExtractorFailureNotices,
	}
	r.ReasoningTokens, r.ReasoningTokensReported = m.Usage.reasoning()
	if !cfg.ConfigUpdatedAt.IsZero() {
		v := cfg.ConfigUpdatedAt.UTC()
		r.ConfigUpdatedAt = &v
	}
	if m.ActualModel != "" {
		r.Model = m.ActualModel
	}
	if r.InputTokens == 0 {
		r.InputTokens = m.Usage.PromptTokens
	}
	if r.OutputTokens == 0 {
		r.OutputTokens = m.Usage.CompletionTokens
	}
	r.ReportedSearchCount, r.SearchUsageReported = m.Usage.toolCount("search", "web_search")
	r.ReportedExtractorCount, r.ExtractorUsageReported = m.Usage.toolCount("web_extractor")
	if callErr != nil {
		r.Outcome = "failed"
	}
	if !m.Attempted {
		r.Outcome = "not_started"
	}
	if answer != "" {
		r.OutputSHA256 = hashBytes([]byte(answer))
	}
	// A short bounded tail write survives request cancellation; no extra goroutine,
	// queue or provider request. A crash before it commits can leave an evidence gap.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 200*time.Millisecond)
	defer cancel()
	if err := c.recorder.SaveModelCall(saveCtx, r); err != nil {
		slog.WarnContext(ctx, "model_call.record_failed", "operation", r.Operation, "call_id", r.ID, "reason", "storage_unavailable")
		return
	}
	if identity, ok := ctx.Value(modelIdentityKey{}).(*ModelIdentity); ok {
		identity.Recorded = true
	}
}
