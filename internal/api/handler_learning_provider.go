package api

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"net/http"
	"strings"
)

type learningProviderRequest struct {
	Independent bool     `json:"independent"`
	Deployment  string   `json:"deployment"`
	BaseURL     string   `json:"base_url"`
	APIKey      string   `json:"api_key"`
	ClearAPIKey bool     `json:"clear_api_key"`
	Model       string   `json:"model"`
	Temperature *float64 `json:"temperature"`
}

func (s *Server) handleGetLearningProvider(w http.ResponseWriter, r *http.Request) {
	store, ok := s.questionStore.(storage.LearningProviderConfigStore)
	if !ok {
		writeError(w, 503, "当前存储未启用总结配置")
		return
	}
	c, e := store.GetLearningProviderConfig(r.Context())
	if e != nil {
		writeError(w, 500, "读取总结服务配置失败")
		return
	}
	if c == nil {
		v := domain.DefaultLearningProviderConfig()
		c = &v
	}
	writeJSON(w, 200, publicLearningProvider(*c))
}
func publicLearningProvider(c domain.LearningProviderConfig) any {
	return struct {
		domain.LearningProviderConfig
		APIKeyMasked string `json:"api_key_masked,omitempty"`
	}{c, maskAPIKey(c.APIKey)}
}
func (s *Server) handleSaveLearningProvider(w http.ResponseWriter, r *http.Request) {
	store, ok := s.questionStore.(storage.LearningProviderConfigStore)
	if !ok {
		writeError(w, 503, "当前存储未启用总结配置")
		return
	}
	var req learningProviderRequest
	if readJSON(r, &req) != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	old, e := store.GetLearningProviderConfig(r.Context())
	if e != nil {
		writeError(w, 500, "读取总结服务配置失败")
		return
	}
	if old == nil {
		v := domain.DefaultLearningProviderConfig()
		old = &v
	}
	c := domain.LearningProviderConfig{Independent: req.Independent, Deployment: req.Deployment, BaseURL: req.BaseURL, APIKey: req.APIKey, Model: req.Model, Temperature: old.Temperature}
	if req.Temperature != nil {
		c.Temperature = *req.Temperature
	}
	// Match existing credential semantics: blank preserves, cloud-to-local never carries a cloud key.
	c.Deployment = strings.ToLower(strings.TrimSpace(c.Deployment))
	c.APIKey = strings.TrimSpace(c.APIKey)
	if req.ClearAPIKey {
		c.APIKey = ""
	}
	if c.APIKey == "" && !req.ClearAPIKey && (c.Deployment != "local" || old.Deployment == "local") {
		c.APIKey = old.APIKey
	}
	if e = c.Normalize(); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	if e = store.SaveLearningProviderConfig(r.Context(), c); e != nil {
		writeError(w, 500, "保存总结服务配置失败")
		return
	}
	s.logAIProviderChange(r, "learning_update", "AI总结")
	saved, e := store.GetLearningProviderConfig(r.Context())
	if e != nil || saved == nil {
		writeError(w, 500, "读取已保存总结配置失败")
		return
	}
	writeJSON(w, 200, publicLearningProvider(*saved))
}
