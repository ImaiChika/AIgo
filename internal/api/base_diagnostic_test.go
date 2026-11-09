//go:build integration

package api

import (
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

type baseDiagnosticCapture struct {
	inner    llm.Client
	messages []llm.Message
	raw      string
	records  []llm.ModelCallRecord
}

func (c *baseDiagnosticCapture) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	c.messages = m
	raw, err := c.inner.Complete(ctx, m, o)
	c.raw = raw
	return raw, err
}
func (c *baseDiagnosticCapture) SaveModelCall(_ context.Context, r llm.ModelCallRecord) error {
	c.records = append(c.records, r)
	return nil
}

type diagnosticTools struct{}

func (diagnosticTools) ResolveNetworkTools(context.Context) (llm.NetworkTools, error) {
	return llm.NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}, nil
}

func TestRealBaseGenerationDiagnostic(t *testing.T) {
	if os.Getenv("AIGO_RUN_BASE_GEN_DIAGNOSTIC") != "1" {
		t.Skip("explicit one-call diagnostic opt-in required")
	}
	raw, err := os.ReadFile(os.Getenv("AIGO_BASE_DIAGNOSTIC_SPEC"))
	if err != nil {
		t.Fatal(err)
	}
	var spec pipeline.GenerationSpec
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("AIGO_BASE_DIAGNOSTIC_CURRENT") == "1" {
		if len(spec.Request.KnowledgePoints) != 1 || spec.Request.Count < 1 || spec.Request.Count > 2 || spec.Request.Workflow != nil {
			t.Fatal("current diagnostic requires one direct topic and count 1 or 2")
		}
		spec.Request.PromptSnapshot = generator.FreezeCurrentBaseline(nil, []domain.GenerationRequest{spec.Request})
	}
	key := os.Getenv("AIGO_REAL_QWEN_KEY")
	if key == "" {
		t.Fatal("explicit key required")
	}
	client := llm.NewDynamicQwenClient(nil, llm.PurposeGeneration, llm.QwenConfig{APIKey: key, BaseURL: os.Getenv("AIGO_REAL_QWEN_BASE"), Model: "qwen3.5-flash", HTTPTimeout: 150 * time.Second})
	capture := &baseDiagnosticCapture{inner: client}
	client.SetNetworkToolsResolver(diagnosticTools{})
	client.SetCallRecorder(capture)
	_, err = generator.NewService(capture).Generate(t.Context(), spec.Request)
	status := "valid"
	if err != nil {
		status = err.Error()
	}
	artifact, _ := json.Marshal(map[string]any{"messages": capture.messages, "final_text": capture.raw, "validation": status, "calls": capture.records})
	if path := os.Getenv("AIGO_BASE_DIAGNOSTIC_REPORT"); path != "" {
		if e := os.WriteFile(path, artifact, 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("BASE_DIAGNOSTIC calls=%d response_bytes=%d validation=%s", len(capture.records), len(capture.raw), status)
}
