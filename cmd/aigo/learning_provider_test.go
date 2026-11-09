package main

import (
	"aigo/internal/domain"
	"aigo/internal/llm"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type dedicatedLearningResolverStore struct {
	resolverProviderStore
	learning *domain.LearningProviderConfig
	err      error
}

func (s *dedicatedLearningResolverStore) GetLearningProviderConfig(context.Context) (*domain.LearningProviderConfig, error) {
	return s.learning, s.err
}
func (s *dedicatedLearningResolverStore) SaveLearningProviderConfig(_ context.Context, c domain.LearningProviderConfig) error {
	s.learning = &c
	return nil
}
func TestDedicatedLearningRouteNeverChangesGenerationOrIndependentCheck(t *testing.T) {
	on := true
	s := &dedicatedLearningResolverStore{learning: &domain.LearningProviderConfig{Independent: true, Deployment: "cloud", BaseURL: "https://summary.invalid/v1", APIKey: "summary-key", Model: "qwen3.8-flash"}}
	s.active = &domain.AIProviderConfig{Deployment: "cloud", BaseURL: "https://generation.invalid/v1", APIKey: "gen-key", GenerationModel: "gen-model", CheckModel: "check-model"}
	r := databaseQwenResolver{store: s, generationFallback: llm.QwenConfig{Model: "fallback-gen", HTTPTimeout: 300 * time.Second}, checkFallback: llm.QwenConfig{Model: "check-env", HTTPTimeout: 150 * time.Second}, learningFallback: llm.QwenConfig{Model: "fallback-gen", HTTPTimeout: 210 * time.Second, EnableThinking: &on}, independentCheck: true}
	summary, _, e := r.ResolveQwenConfig(t.Context(), llm.PurposeLearning)
	if e != nil || summary.Model != "qwen3.8-flash" || summary.APIKey != "summary-key" || summary.HTTPTimeout != 210*time.Second || summary.EnableThinking == nil || !*summary.EnableThinking {
		t.Fatal("dedicated summary resolution incorrect")
	}
	gen, _, _ := r.ResolveQwenConfig(t.Context(), llm.PurposeGeneration)
	check, _, _ := r.ResolveQwenConfig(t.Context(), llm.PurposeAICheck)
	if gen.Model != "gen-model" || gen.APIKey != "gen-key" || check.Model != "check-env" {
		t.Fatal("unrelated routes changed")
	}
	s.active.GenerationModel = "changed-gen"
	again, _, _ := r.ResolveQwenConfig(t.Context(), llm.PurposeLearning)
	if again.Model != summary.Model || again.APIKey != summary.APIKey {
		t.Fatal("generation switch overwrote summary")
	}
	s.learning.Independent = false
	again, _, _ = r.ResolveQwenConfig(t.Context(), llm.PurposeLearning)
	if again.Model != "changed-gen" || again.HTTPTimeout != 210*time.Second {
		t.Fatal("inherit route or summary budget lost")
	}
	s.err = errors.New("private config store failure")
	if _, _, e = r.ResolveQwenConfig(t.Context(), llm.PurposeLearning); e == nil {
		t.Fatal("failed summary config read silently fell back")
	}
}

func TestDedicatedLearningHTTPUsesOwnEndpointKeyAndSampling(t *testing.T) {
	on := true
	genCalls, summaryCalls := 0, 0
	gen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		genCalls++
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if r.Header.Get("Authorization") != "Bearer generation-test-key" {
			t.Error("generation credential changed")
		}
		want := 0.4
		if b["model"] == "check-model" {
			want = 0.2
		}
		if b["temperature"] != want || b["stream"] != nil {
			t.Error("generation/check transport or sampling changed")
		}
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"complete"}}]}`)
	}))
	defer gen.Close()
	summary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summaryCalls++
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if r.Header.Get("Authorization") != "Bearer summary-test-key" || b["model"] != "qwen3.8-flash" || b["temperature"] != 0.6 || b["stream"] != true || b["enable_thinking"] != true {
			t.Error("summary wire routing/sampling/thinking mismatch")
		}
		if _, ok := b["tools"]; ok {
			t.Error("summary gained network tools")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"complete\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer summary.Close()
	s := &dedicatedLearningResolverStore{learning: &domain.LearningProviderConfig{Independent: true, Deployment: "cloud", BaseURL: summary.URL, APIKey: "summary-test-key", Model: "qwen3.8-flash"}}
	s.active = &domain.AIProviderConfig{Deployment: "cloud", BaseURL: gen.URL, APIKey: "generation-test-key", GenerationModel: "gen-model", CheckModel: "check-model"}
	r := databaseQwenResolver{store: s, generationFallback: llm.QwenConfig{EnableThinking: &on}, checkFallback: llm.QwenConfig{EnableThinking: &on}, learningFallback: llm.QwenConfig{Model: "fallback", EnableThinking: &on, HTTPTimeout: 210 * time.Second}}
	for _, p := range []llm.ConfigPurpose{llm.PurposeGeneration, llm.PurposeAICheck, llm.PurposeLearning} {
		o := llm.GenerateOptions{Temperature: 0.4}
		if p == llm.PurposeAICheck {
			o.Temperature = 0.2
		}
		if p == llm.PurposeLearning {
			o.Temperature = 0.6
			o.StreamChat = true
		}
		c := llm.NewDynamicQwenClient(r, p, llm.QwenConfig{})
		if v, e := c.Complete(t.Context(), []llm.Message{{Role: llm.RoleUser, Content: "test"}}, o); e != nil || v != "complete" {
			t.Fatal("request did not complete", e)
		}
	}
	if genCalls != 2 || summaryCalls != 1 {
		t.Fatal("requests crossed endpoint routes", genCalls, summaryCalls)
	}
}
