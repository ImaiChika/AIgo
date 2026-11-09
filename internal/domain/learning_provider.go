package domain

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

const DefaultLearningTemperature = 0.6
const LearningConfiguredSampling = "configured-temperature-v1"

// LearningProviderConfig is independent from the active generation/check profile.
type LearningProviderConfig struct {
	Independent      bool      `json:"independent"`
	Deployment       string    `json:"deployment"`
	BaseURL          string    `json:"base_url"`
	APIKey           string    `json:"-"`
	APIKeyConfigured bool      `json:"api_key_configured"`
	Model            string    `json:"model"`
	Temperature      float64   `json:"temperature"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func DefaultLearningProviderConfig() LearningProviderConfig {
	return LearningProviderConfig{Deployment: "cloud", Model: "qwen3.5-flash", Temperature: DefaultLearningTemperature}
}
func (c *LearningProviderConfig) Normalize() error {
	c.Deployment = strings.ToLower(strings.TrimSpace(c.Deployment))
	if c.Deployment == "" {
		c.Deployment = "cloud"
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.Model = strings.TrimSpace(c.Model)
	c.APIKey = strings.TrimSpace(c.APIKey)
	if c.Deployment != "cloud" && c.Deployment != "local" {
		return fmt.Errorf("部署方式只能是 cloud 或 local")
	}
	if math.IsNaN(c.Temperature) || math.IsInf(c.Temperature, 0) || c.Temperature < 0 || c.Temperature > 2 {
		return fmt.Errorf("总结温度须在 0 到 2 之间")
	}
	if len([]rune(c.Model)) > 200 || len(c.APIKey) > 4096 {
		return fmt.Errorf("总结模型名称或密钥长度异常")
	}
	if c.BaseURL != "" {
		u, e := url.Parse(c.BaseURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return fmt.Errorf("总结 API 地址须为不含用户名密码的完整 http/https 地址")
		}
	}
	if c.Independent && (c.BaseURL == "" || c.Model == "") {
		return fmt.Errorf("请填写总结 API 地址和模型")
	}
	if c.Independent && c.Deployment == "cloud" && c.APIKey == "" {
		return fmt.Errorf("独立云端总结服务必须保留或填写 API Key")
	}
	return nil
}
