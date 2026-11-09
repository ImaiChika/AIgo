//go:build integration

package api

import (
	"aigo/internal/domain"
	"aigo/internal/storage/postgres"
	"encoding/json"
	"strings"
	"testing"
)

func TestLearningProviderIsIndependentPrivateAndSuperAdminOnly(t *testing.T) {
	s, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	store.SetLLMEncryptionKey("summary-config-test-secret")
	h := s.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.220")
	r := serveAuthJSON(t, h, "GET", "/api/system/ai-learning", admin, "198.51.100.220", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"temperature":0.6`) {
		t.Fatal(r.Code, r.Body)
	}
	original := domain.AIProviderConfig{ID: "generation-untouched", Name: "出题原配置", Deployment: "cloud", BaseURL: "https://generation.invalid/v1", APIKey: "generation-private", GenerationModel: "generation-model", CheckModel: "check-model", Active: true}
	if e := store.SaveAIProviderConfig(t.Context(), original); e != nil {
		t.Fatal(e)
	}
	payload := map[string]any{"independent": true, "deployment": "cloud", "base_url": "https://summary.invalid/v1", "api_key": "summary-secret-unique", "model": "qwen3.8-flash", "temperature": 0.6}
	r = serveAuthJSON(t, h, "PUT", "/api/system/ai-learning", admin, "198.51.100.220", payload)
	if r.Code != 200 || strings.Contains(r.Body.String(), "summary-secret-unique") {
		t.Fatal("summary save or secret disclosure", r.Code)
	}
	c, e := store.GetLearningProviderConfig(t.Context())
	if e != nil || c == nil || c.APIKey != "summary-secret-unique" || c.Temperature != 0.6 || !c.Independent {
		t.Fatal("stored summary mismatch", e)
	}
	var encrypted string
	if e = store.DB().QueryRowContext(t.Context(), `SELECT api_key_ciphertext FROM learning_provider_config`).Scan(&encrypted); e != nil || encrypted == "" || encrypted == c.APIKey {
		t.Fatal("key not encrypted")
	}
	payload["api_key"] = "  "
	payload["model"] = "qwen3.6-flash"
	payload["temperature"] = 0.0
	r = serveAuthJSON(t, h, "PUT", "/api/system/ai-learning", admin, "198.51.100.220", payload)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	c, _ = store.GetLearningProviderConfig(t.Context())
	if c.APIKey != "summary-secret-unique" || c.Temperature != 0 {
		t.Fatal("blank key or zero temperature lost")
	}
	before, _ := store.GetActiveAIProviderConfig(t.Context())
	if before.ID != original.ID || before.GenerationModel != original.GenerationModel || before.CheckModel != original.CheckModel || before.APIKey != original.APIKey {
		t.Fatal("summary save modified generation")
	}
	payload["deployment"] = " LOCAL "
	payload["base_url"] = "http://127.0.0.1:18081/v1"
	r = serveAuthJSON(t, h, "PUT", "/api/system/ai-learning", admin, "198.51.100.220", payload)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	c, _ = store.GetLearningProviderConfig(t.Context())
	if c.APIKey != "" {
		t.Fatal("cloud key was carried to local endpoint")
	}

	payload["api_key"] = "ignored-local-key-when-clear"
	payload["clear_api_key"] = true
	r = serveAuthJSON(t, h, "PUT", "/api/system/ai-learning", admin, "198.51.100.220", payload)
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	c, _ = store.GetLearningProviderConfig(t.Context())
	if c.APIKey != "" {
		t.Fatal("clear flag did not override typed key")
	}
	payload["temperature"] = 2.1
	r = serveAuthJSON(t, h, "PUT", "/api/system/ai-learning", admin, "198.51.100.220", payload)
	if r.Code != 400 {
		t.Fatal("invalid temperature allowed", r.Code)
	}
	r = serveAuthJSON(t, h, "POST", "/api/users", admin, "198.51.100.220", map[string]any{"username": "summary-ordinary", "password": "ordinary-password"})
	if r.Code != 201 {
		t.Fatal(r.Code)
	}
	teacher := loginForAuthTest(t, h, "summary-ordinary", "ordinary-password", "198.51.100.221")
	for _, method := range []string{"GET", "PUT"} {
		r = serveAuthJSON(t, h, method, "/api/system/ai-learning", teacher, "198.51.100.221", payload)
		if r.Code != 403 {
			t.Fatal("ordinary user accessed summary config", method, r.Code)
		}
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "summary-secret-unique") {
		t.Fatal("domain JSON leaked key")
	}
}

func TestLearningConfiguredTemperatureFrozenAcrossConfigChange(t *testing.T) {
	s, h, teacher, version, _, cleanup := learningFlowEnv(t)
	defer cleanup()
	store := s.questionStore.(*postgres.Store)
	if e := store.SaveLearningProviderConfig(t.Context(), domain.LearningProviderConfig{Temperature: 0.9}); e != nil {
		t.Fatal(e)
	}
	q := modifiedLearningSource(t, s, h, teacher, version, "configured-temp-freeze")
	job := submitLearningFixture(t, h, teacher, q, "frozen-temperature")
	if job.SamplingPolicy != domain.LearningConfiguredSampling || job.SamplingTemperature == nil || job.AnalysisTemperature() != 0.9 {
		t.Fatal("temperature not frozen")
	}
	done := waitLearningJob(t, h, teacher, job.ID)
	if done.Status != "succeeded" {
		t.Fatal(done.Status)
	}
	if e := store.SaveLearningProviderConfig(t.Context(), domain.LearningProviderConfig{Temperature: 0.6}); e != nil {
		t.Fatal(e)
	}
	replay := submitLearningFixture(t, h, teacher, q, "frozen-temperature")
	if replay.ID != job.ID || replay.AnalysisTemperature() != 0.9 {
		t.Fatal("config update mutated accepted task")
	}
	newer := submitLearningFixture(t, h, teacher, q, "new-temperature")
	if newer.AnalysisTemperature() != 0.6 {
		t.Fatal("new task did not use current temperature")
	}
}
