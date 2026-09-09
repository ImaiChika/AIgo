package domain

import "time"

// AIProviderConfig 是系统级 OpenAI-compatible 文本模型配置。
// APIKey 仅在服务内部使用，HTTP 响应不得直接序列化该字段。
// GenerationModel 与 CheckModel 分开保存，默认可以相同，也允许使用不同模型。
type AIProviderConfig struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Deployment            string    `json:"deployment"` // cloud / local
	BaseURL               string    `json:"base_url"`
	APIKey                string    `json:"-"`
	APIKeyConfigured      bool      `json:"api_key_configured"`
	GenerationModel       string    `json:"generation_model"`
	CheckModel            string    `json:"check_model"`
	BatchAPIKey           string    `json:"-"`
	BatchAPIKeyConfigured bool      `json:"batch_api_key_configured"`
	BatchBaseURL          string    `json:"batch_base_url"`
	BatchModel            string    `json:"batch_model"`
	Active                bool      `json:"active"`
	Source                string    `json:"source"` // manual / env-bootstrap
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}
