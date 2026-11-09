package postgres

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// SaveAIProviderConfig 新增或更新一个 AI 服务配置。
// active=true 时在同一事务内先取消旧配置，保证切换不会出现两个活动端点。
func (s *Store) SaveAIProviderConfig(ctx context.Context, config domain.AIProviderConfig) error {
	config.ID = strings.TrimSpace(config.ID)
	config.Name = strings.TrimSpace(config.Name)
	config.Deployment = strings.ToLower(strings.TrimSpace(config.Deployment))
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.GenerationModel = strings.TrimSpace(config.GenerationModel)
	config.CheckModel = strings.TrimSpace(config.CheckModel)
	config.BatchBaseURL = strings.TrimRight(strings.TrimSpace(config.BatchBaseURL), "/")
	config.BatchModel = strings.TrimSpace(config.BatchModel)
	if config.ID == "" || config.Name == "" || config.BaseURL == "" || config.GenerationModel == "" {
		return fmt.Errorf("AI 服务配置缺少必填字段")
	}
	if config.CheckModel == "" {
		config.CheckModel = config.GenerationModel
	}
	if config.BatchBaseURL == "" {
		config.BatchBaseURL = config.BaseURL
	}
	if config.BatchModel == "" {
		config.BatchModel = config.GenerationModel
	}
	if config.Deployment != "cloud" && config.Deployment != "local" {
		return fmt.Errorf("不支持的 AI 服务部署方式: %s", config.Deployment)
	}
	if config.Source == "" {
		config.Source = "manual"
	}

	ciphertext, err := s.encryptAIProviderKey(config.APIKey)
	if err != nil {
		return err
	}
	batchCiphertext, err := s.encryptAIProviderKey(config.BatchAPIKey)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("保存 AI 服务配置事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if config.Active {
		if _, err := tx.ExecContext(ctx, `UPDATE ai_provider_configs SET active=FALSE, updated_at=NOW() WHERE active=TRUE`); err != nil {
			return fmt.Errorf("切换 AI 服务配置失败: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO ai_provider_configs
			(id, name, deployment, base_url, api_key_ciphertext, generation_model, check_model,
			 batch_api_key_ciphertext, batch_base_url, batch_model, active, source, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW(),NOW())
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name,
			deployment=EXCLUDED.deployment,
			base_url=EXCLUDED.base_url,
			api_key_ciphertext=EXCLUDED.api_key_ciphertext,
			generation_model=EXCLUDED.generation_model,
			check_model=EXCLUDED.check_model,
			batch_api_key_ciphertext=EXCLUDED.batch_api_key_ciphertext,
			batch_base_url=EXCLUDED.batch_base_url,
			batch_model=EXCLUDED.batch_model,
			active=EXCLUDED.active,
			source=EXCLUDED.source,
			updated_at=NOW()
	`, config.ID, config.Name, config.Deployment, config.BaseURL, ciphertext,
		config.GenerationModel, config.CheckModel, batchCiphertext, config.BatchBaseURL,
		config.BatchModel, config.Active, config.Source); err != nil {
		return fmt.Errorf("写入 AI 服务配置失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 AI 服务配置失败: %w", err)
	}
	committed = true
	return nil
}

// ListAIProviderConfigs 返回配置列表，但 API Key 已在数据库中加密；只有服务内部
// 在调用链路中短暂解密，不向前端直接返回。
func (s *Store) ListAIProviderConfigs(ctx context.Context) ([]domain.AIProviderConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, deployment, base_url, api_key_ciphertext,
		       generation_model, check_model, batch_api_key_ciphertext, batch_base_url, batch_model,
		       active, source, created_at, updated_at
		FROM ai_provider_configs
		ORDER BY active DESC, updated_at DESC, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []domain.AIProviderConfig
	for rows.Next() {
		config, err := s.scanAIProviderConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *config)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return configs, nil
}

// GetActiveAIProviderConfig 获取当前活动配置。没有配置时返回 nil,nil，调用方可
// 安全回退到环境变量或给出“尚未配置”的明确错误。
func (s *Store) GetActiveAIProviderConfig(ctx context.Context) (*domain.AIProviderConfig, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, deployment, base_url, api_key_ciphertext,
		       generation_model, check_model, batch_api_key_ciphertext, batch_base_url, batch_model,
		       active, source, created_at, updated_at
		FROM ai_provider_configs
		WHERE active=TRUE
		LIMIT 1
	`)
	config, err := s.scanAIProviderConfig(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return config, nil
}

// ActivateAIProviderConfig 原子切换活动配置。
func (s *Store) ActivateAIProviderConfig(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return storage.ErrAIProviderConfigNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("切换 AI 服务配置事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE ai_provider_configs SET active=FALSE, updated_at=NOW() WHERE active=TRUE`); err != nil {
		return fmt.Errorf("停用旧 AI 服务配置失败: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE ai_provider_configs SET active=TRUE, updated_at=NOW() WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("启用 AI 服务配置失败: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return storage.ErrAIProviderConfigNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 AI 服务配置切换失败: %w", err)
	}
	committed = true
	return nil
}

// DeleteAIProviderConfig 删除配置。删除活动配置后系统会回退到环境变量兼容配置，
// 如果没有兼容配置，生成接口会返回未配置错误，而不会误用其他端点。
func (s *Store) DeleteAIProviderConfig(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM ai_provider_configs WHERE id=$1`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return storage.ErrAIProviderConfigNotFound
	}
	return nil
}

type aiProviderScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanAIProviderConfig(scanner aiProviderScanner) (*domain.AIProviderConfig, error) {
	var (
		config          domain.AIProviderConfig
		ciphertext      string
		batchCiphertext string
	)
	if err := scanner.Scan(
		&config.ID, &config.Name, &config.Deployment, &config.BaseURL, &ciphertext,
		&config.GenerationModel, &config.CheckModel, &batchCiphertext, &config.BatchBaseURL, &config.BatchModel,
		&config.Active, &config.Source,
		&config.CreatedAt, &config.UpdatedAt,
	); err != nil {
		return nil, err
	}
	apiKey, err := s.decryptAIProviderKey(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("解密 AI 服务配置 %q 失败: %w", config.ID, err)
	}
	config.APIKey = apiKey
	config.APIKeyConfigured = strings.TrimSpace(config.APIKey) != ""
	batchAPIKey, err := s.decryptAIProviderKey(batchCiphertext)
	if err != nil {
		return nil, fmt.Errorf("解密 AI 批量服务配置 %q 失败: %w", config.ID, err)
	}
	// 兼容 20 号迁移前的配置：批量默认沿用云端实时凭证、地址和生成模型；
	// 本地端点不自动继承本地实时凭证，避免未来真实 Batch 误发内网 Key。
	if strings.TrimSpace(config.BatchBaseURL) == "" {
		config.BatchBaseURL = config.BaseURL
	}
	if strings.TrimSpace(config.BatchModel) == "" {
		config.BatchModel = config.GenerationModel
	}
	if batchAPIKey == "" && config.Deployment == "cloud" {
		batchAPIKey = config.APIKey
	}
	config.BatchAPIKey = batchAPIKey
	config.BatchAPIKeyConfigured = strings.TrimSpace(batchAPIKey) != ""
	return &config, nil
}

func (s *Store) encryptAIProviderKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	block, err := s.aiProviderCipher()
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 AI 服务密钥加密器失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成 AI 服务密钥随机数失败: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(ciphertext), nil
}

func (s *Store) decryptAIProviderKey(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	block, err := s.aiProviderCipher()
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 AI 服务密钥解密器失败: %w", err)
	}
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("AI 服务密钥密文格式错误: %w", err)
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("AI 服务密钥密文长度错误")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("AI 服务密钥校验失败（JWT_SECRET 可能已变更）")
	}
	return string(plain), nil
}

func (s *Store) aiProviderCipher() (cipher.Block, error) {
	if len(s.llmEncryptionKey) == 0 {
		return nil, fmt.Errorf("未设置 AI 服务配置加密密钥，请检查 JWT_SECRET")
	}
	block, err := aes.NewCipher(s.llmEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("创建 AI 服务配置加密器失败: %w", err)
	}
	return block, nil
}

var _ storage.AIProviderConfigStore = (*Store)(nil)
