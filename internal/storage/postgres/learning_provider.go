package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"database/sql"
	"fmt"
)

func (s *Store) GetLearningProviderConfig(ctx context.Context) (*domain.LearningProviderConfig, error) {
	var c domain.LearningProviderConfig
	var encrypted string
	e := s.queryRowContext(ctx, `SELECT independent,deployment,base_url,api_key_ciphertext,model,temperature,updated_at FROM learning_provider_config WHERE singleton=TRUE`).Scan(&c.Independent, &c.Deployment, &c.BaseURL, &encrypted, &c.Model, &c.Temperature, &c.UpdatedAt)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	c.APIKey, e = s.decryptAIProviderKey(encrypted)
	if e != nil {
		return nil, fmt.Errorf("解密总结服务配置失败")
	}
	c.APIKeyConfigured = c.APIKey != ""
	return &c, nil
}
func (s *Store) SaveLearningProviderConfig(ctx context.Context, c domain.LearningProviderConfig) error {
	if e := c.Normalize(); e != nil {
		return e
	}
	encrypted, e := s.encryptAIProviderKey(c.APIKey)
	if e != nil {
		return e
	}
	_, e = s.execContext(ctx, `INSERT INTO learning_provider_config(singleton,independent,deployment,base_url,api_key_ciphertext,model,temperature,updated_at) VALUES(TRUE,$1,$2,$3,$4,$5,$6,NOW()) ON CONFLICT(singleton) DO UPDATE SET independent=EXCLUDED.independent,deployment=EXCLUDED.deployment,base_url=EXCLUDED.base_url,api_key_ciphertext=EXCLUDED.api_key_ciphertext,model=EXCLUDED.model,temperature=EXCLUDED.temperature,updated_at=NOW()`, c.Independent, c.Deployment, c.BaseURL, encrypted, c.Model, c.Temperature)
	return e
}

var _ storage.LearningProviderConfigStore = (*Store)(nil)
