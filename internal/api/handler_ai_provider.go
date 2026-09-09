package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

type aiProviderRequest struct {
	Name             string `json:"name"`
	Deployment       string `json:"deployment"`
	BaseURL          string `json:"base_url"`
	APIKey           string `json:"api_key"`
	ClearAPIKey      bool   `json:"clear_api_key"`
	GenerationModel  string `json:"generation_model"`
	CheckModel       string `json:"check_model"`
	BatchAPIKey      string `json:"batch_api_key"`
	ClearBatchAPIKey bool   `json:"clear_batch_api_key"`
	BatchBaseURL     string `json:"batch_base_url"`
	BatchModel       string `json:"batch_model"`
	Active           *bool  `json:"active"`
}

type aiProviderResponse struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Deployment            string    `json:"deployment"`
	BaseURL               string    `json:"base_url"`
	APIKeyConfigured      bool      `json:"api_key_configured"`
	APIKeyMasked          string    `json:"api_key_masked,omitempty"`
	GenerationModel       string    `json:"generation_model"`
	CheckModel            string    `json:"check_model"`
	BatchAPIKeyConfigured bool      `json:"batch_api_key_configured"`
	BatchAPIKeyMasked     string    `json:"batch_api_key_masked,omitempty"`
	BatchBaseURL          string    `json:"batch_base_url"`
	BatchModel            string    `json:"batch_model"`
	Active                bool      `json:"active"`
	Source                string    `json:"source"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func (s *Server) handleListAIProviders(w http.ResponseWriter, r *http.Request) {
	if s.aiProviderStore == nil {
		writeError(w, http.StatusServiceUnavailable, "当前存储未启用 AI 服务配置功能")
		return
	}
	configs, err := s.aiProviderStore.ListAIProviderConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 AI 服务配置失败")
		return
	}
	items := make([]aiProviderResponse, 0, len(configs))
	for _, config := range configs {
		items = append(items, publicAIProvider(config))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) handleCreateAIProvider(w http.ResponseWriter, r *http.Request) {
	if s.aiProviderStore == nil {
		writeError(w, http.StatusServiceUnavailable, "当前存储未启用 AI 服务配置功能")
		return
	}
	var req aiProviderRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	config, err := normalizeAIProviderRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if config.Deployment == "cloud" && strings.TrimSpace(config.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "云端配置必须填写 API Key")
		return
	}
	config.ID = newAIProviderID()
	config.Source = "manual"
	if strings.TrimSpace(req.BatchAPIKey) == "" && config.Deployment == "cloud" {
		config.BatchAPIKey = config.APIKey
	}
	config.Active = req.Active == nil || *req.Active
	if err := s.aiProviderStore.SaveAIProviderConfig(r.Context(), config); err != nil {
		writeError(w, http.StatusInternalServerError, "保存 AI 服务配置失败")
		return
	}
	s.logAIProviderChange(r, "create", config.Name)
	created, err := s.findAIProvider(r, config.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取刚保存的 AI 服务配置失败")
		return
	}
	writeJSON(w, http.StatusCreated, publicAIProvider(*created))
}

func (s *Server) handleUpdateAIProvider(w http.ResponseWriter, r *http.Request) {
	if s.aiProviderStore == nil {
		writeError(w, http.StatusServiceUnavailable, "当前存储未启用 AI 服务配置功能")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	current, err := s.findAIProvider(r, id)
	if err != nil {
		if errors.Is(err, storage.ErrAIProviderConfigNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "读取 AI 服务配置失败")
		}
		return
	}
	var req aiProviderRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	config, err := normalizeAIProviderRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	config.ID = id
	if strings.TrimSpace(req.APIKey) == "" && !req.ClearAPIKey {
		// 从云端切到本地时不能沿用云端凭证，避免把云 Key 发送到
		// 内网/自建端点；如本地网关确实需要凭证，管理员应在表单中重新填写。
		if config.Deployment != "local" || current.Deployment == "local" {
			config.APIKey = current.APIKey
		}
	}
	if strings.TrimSpace(req.BatchAPIKey) == "" && !req.ClearBatchAPIKey {
		if config.Deployment != "local" || current.Deployment == "local" {
			config.BatchAPIKey = current.BatchAPIKey
		}
	}
	if req.Active == nil {
		config.Active = current.Active
	} else {
		config.Active = *req.Active
	}
	config.Source = "manual"
	if config.Deployment == "cloud" && strings.TrimSpace(config.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "云端配置必须保留或填写 API Key；如需清除请切换为本地配置")
		return
	}
	if err := s.aiProviderStore.SaveAIProviderConfig(r.Context(), config); err != nil {
		writeError(w, http.StatusInternalServerError, "更新 AI 服务配置失败")
		return
	}
	s.logAIProviderChange(r, "update", config.Name)
	updated, err := s.findAIProvider(r, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取更新后的 AI 服务配置失败")
		return
	}
	writeJSON(w, http.StatusOK, publicAIProvider(*updated))
}

func (s *Server) handleActivateAIProvider(w http.ResponseWriter, r *http.Request) {
	if s.aiProviderStore == nil {
		writeError(w, http.StatusServiceUnavailable, "当前存储未启用 AI 服务配置功能")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.aiProviderStore.ActivateAIProviderConfig(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrAIProviderConfigNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "切换 AI 服务配置失败")
		return
	}
	s.logAIProviderChange(r, "activate", id)
	config, err := s.findAIProvider(r, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取已启用的 AI 服务配置失败")
		return
	}
	writeJSON(w, http.StatusOK, publicAIProvider(*config))
}

func (s *Server) handleDeleteAIProvider(w http.ResponseWriter, r *http.Request) {
	if s.aiProviderStore == nil {
		writeError(w, http.StatusServiceUnavailable, "当前存储未启用 AI 服务配置功能")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.aiProviderStore.DeleteAIProviderConfig(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrAIProviderConfigNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "删除 AI 服务配置失败")
		return
	}
	s.logAIProviderChange(r, "delete", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (s *Server) findAIProvider(r *http.Request, id string) (*domain.AIProviderConfig, error) {
	configs, err := s.aiProviderStore.ListAIProviderConfigs(r.Context())
	if err != nil {
		return nil, err
	}
	for i := range configs {
		if configs[i].ID == id {
			return &configs[i], nil
		}
	}
	return nil, storage.ErrAIProviderConfigNotFound
}

func normalizeAIProviderRequest(req aiProviderRequest) (domain.AIProviderConfig, error) {
	name := strings.TrimSpace(req.Name)
	deployment := strings.ToLower(strings.TrimSpace(req.Deployment))
	if deployment == "" {
		deployment = "cloud"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	generationModel := strings.TrimSpace(req.GenerationModel)
	checkModel := strings.TrimSpace(req.CheckModel)
	if name == "" {
		return domain.AIProviderConfig{}, fmt.Errorf("配置名称不能为空")
	}
	if len([]rune(name)) > 80 {
		return domain.AIProviderConfig{}, fmt.Errorf("配置名称不能超过 80 个字符")
	}
	if deployment != "cloud" && deployment != "local" {
		return domain.AIProviderConfig{}, fmt.Errorf("部署方式只能是 cloud 或 local")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return domain.AIProviderConfig{}, fmt.Errorf("API 地址必须是完整的 http/https 地址")
	}
	if parsed.User != nil {
		return domain.AIProviderConfig{}, fmt.Errorf("API 地址不能包含用户名或密码")
	}
	if generationModel == "" {
		return domain.AIProviderConfig{}, fmt.Errorf("生成模型不能为空")
	}
	if checkModel == "" {
		checkModel = generationModel
	}
	if len([]rune(generationModel)) > 200 || len([]rune(checkModel)) > 200 {
		return domain.AIProviderConfig{}, fmt.Errorf("模型名称不能超过 200 个字符")
	}
	if len(req.APIKey) > 4096 {
		return domain.AIProviderConfig{}, fmt.Errorf("API Key 长度异常")
	}
	batchBaseURL := strings.TrimRight(strings.TrimSpace(req.BatchBaseURL), "/")
	if batchBaseURL == "" {
		batchBaseURL = baseURL
	}
	batchParsed, err := url.Parse(batchBaseURL)
	if err != nil || batchParsed.Scheme == "" || batchParsed.Host == "" || (batchParsed.Scheme != "http" && batchParsed.Scheme != "https") {
		return domain.AIProviderConfig{}, fmt.Errorf("批量 API 地址必须是完整的 http/https 地址")
	}
	if batchParsed.User != nil {
		return domain.AIProviderConfig{}, fmt.Errorf("批量 API 地址不能包含用户名或密码")
	}
	batchModel := strings.TrimSpace(req.BatchModel)
	if batchModel == "" {
		batchModel = generationModel
	}
	if len([]rune(batchModel)) > 200 {
		return domain.AIProviderConfig{}, fmt.Errorf("批量模型名称不能超过 200 个字符")
	}
	if len(req.BatchAPIKey) > 4096 {
		return domain.AIProviderConfig{}, fmt.Errorf("批量 API Key 长度异常")
	}
	return domain.AIProviderConfig{
		Name:            name,
		Deployment:      deployment,
		BaseURL:         baseURL,
		APIKey:          strings.TrimSpace(req.APIKey),
		GenerationModel: generationModel,
		CheckModel:      checkModel,
		BatchAPIKey:     strings.TrimSpace(req.BatchAPIKey),
		BatchBaseURL:    batchBaseURL,
		BatchModel:      batchModel,
	}, nil
}

func publicAIProvider(config domain.AIProviderConfig) aiProviderResponse {
	return aiProviderResponse{
		ID:                    config.ID,
		Name:                  config.Name,
		Deployment:            config.Deployment,
		BaseURL:               config.BaseURL,
		APIKeyConfigured:      config.APIKeyConfigured,
		APIKeyMasked:          maskAPIKey(config.APIKey),
		GenerationModel:       config.GenerationModel,
		CheckModel:            config.CheckModel,
		BatchAPIKeyConfigured: config.BatchAPIKeyConfigured,
		BatchAPIKeyMasked:     maskAPIKey(config.BatchAPIKey),
		BatchBaseURL:          config.BatchBaseURL,
		BatchModel:            config.BatchModel,
		Active:                config.Active,
		Source:                config.Source,
		CreatedAt:             config.CreatedAt,
		UpdatedAt:             config.UpdatedAt,
	}
}

func maskAPIKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return "已配置"
	}
	return string(runes[:3]) + "••••" + string(runes[len(runes)-4:])
}

func newAIProviderID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return "llm-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("llm-%d", time.Now().UnixNano())
}

func (s *Server) logAIProviderChange(r *http.Request, action, detail string) {
	if s.auditSvc == nil {
		return
	}
	_ = s.auditSvc.Log(r.Context(), "", "ai_provider_"+action, auth.GetUsername(r.Context()), detail)
}
