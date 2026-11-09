package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConfigPurpose 标识实时调用的业务用途。
type ConfigPurpose string

const (
	PurposeGeneration ConfigPurpose = "generation"
	PurposeAICheck    ConfigPurpose = "ai_check"
	PurposeLearning   ConfigPurpose = "learning_analysis"
)

// RuntimeConfigResolver 在每次请求前解析当前活动的模型配置。
// 数据库实现可以在后台切换配置，正在运行的服务无需重启。
type RuntimeConfigResolver interface {
	ResolveQwenConfig(ctx context.Context, purpose ConfigPurpose) (QwenConfig, bool, error)
}

// Resolver errors may contain database diagnostics or credentials. Keep the
// public/task error stable, and never silently switch to another paid route.
var errRuntimeConfigResolution = errors.New("读取模型配置失败，未发起调用；请检查服务配置及加密密钥")

// DynamicQwenClient 是一个很薄的动态路由层：实时生成/检查请求都在调用时
// 读取活动配置，未配置时回退到启动时的环境配置，兼容旧部署方式。
type DynamicQwenClient struct {
	resolver     RuntimeConfigResolver
	purpose      ConfigPurpose
	fallback     QwenConfig
	recovery     *EndpointRecovery
	networkTools NetworkToolsResolver
	recorder     ModelCallRecorder
}

// NewDynamicQwenClient 创建动态 Qwen 客户端。
func NewDynamicQwenClient(resolver RuntimeConfigResolver, purpose ConfigPurpose, fallback QwenConfig) *DynamicQwenClient {
	return &DynamicQwenClient{resolver: resolver, purpose: purpose, fallback: fallback}
}

func (c *DynamicQwenClient) resolve(ctx context.Context) (QwenConfig, error) {
	source := "environment"
	if c.resolver != nil {
		cfg, ok, err := c.resolver.ResolveQwenConfig(ctx, c.purpose)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return QwenConfig{ConfigSource: "database_resolution_error"}, contextErr
			}
			return QwenConfig{ConfigSource: "database_resolution_error"}, errRuntimeConfigResolution
		}
		if ok {
			if cfg.ConfigSource == "" {
				cfg.ConfigSource = "database"
			}
			return cfg, nil
		}
		source = "environment_no_active_config"
	}
	cfg := c.fallback
	if cfg.ConfigSource == "" {
		cfg.ConfigSource = source
	}
	return cfg, nil
}

// Complete 按当前活动配置创建短生命周期 HTTP 客户端并发起一次请求。
// 客户端本身不持有 API Key，配置切换会自然作用于下一次调用。
func (c *DynamicQwenClient) Complete(ctx context.Context, messages []Message, opts GenerateOptions) (answer string, callErr error) {
	// Capture state belongs to this call even if a synchronous caller reuses ctx.
	if identity, ok := ctx.Value(modelIdentityKey{}).(*ModelIdentity); ok {
		*identity = ModelIdentity{}
	}
	started := time.Now()
	meta := &modelCallMeta{Protocol: "none"}
	ctx = context.WithValue(ctx, modelCallMetaKey{}, meta)
	cfg, configErr := c.resolve(ctx)
	cfg.NetworkTools = NetworkTools{}
	defer func() {
		captureModelIdentity(ctx, cfg, meta)
		logModelCall(ctx, c.purpose, cfg, meta, started, callErr)
		c.recordCall(ctx, cfg, meta, started, answer, callErr)
	}()
	if configErr != nil {
		meta.FailureStage = "config_resolution"
		return "", configErr
	}
	if c.recovery != nil {
		meta.FailureStage = "endpoint_backoff"
		if err := c.recovery.wait(ctx, cfg); err != nil {
			return "", err
		}
	}
	// Read after endpoint backoff: queued/retried calls must honor a later off switch.
	// Config snapshots and caller options cannot grant network access.
	cfg.NetworkTools = NetworkTools{}
	if c.networkTools != nil {
		meta.FailureStage = "tool_policy"
		var policy NetworkTools
		var err error
		if versioned, ok := c.networkTools.(VersionedNetworkToolsResolver); ok {
			policy, meta.ToolsRevision, err = versioned.ResolveVersionedNetworkTools(ctx)
		} else {
			policy, err = c.networkTools.ResolveNetworkTools(ctx)
		}
		if err != nil {
			return "", fmt.Errorf("读取模型联网设置失败，未发起调用: %w", err)
		}
		if err := policy.Validate(); err != nil {
			return "", err
		}
		cfg.NetworkTools = policy
	}
	meta.FailureStage = "provider"
	raw, err := NewQwenClient(cfg).Complete(ctx, messages, opts)
	if c.recovery != nil {
		fingerprint, _ := json.Marshal(messages)
		c.recovery.observe(cfg, sha256.Sum256(fingerprint), err)
	}
	if err == nil {
		meta.FailureStage = ""
	}
	return raw, err
}

// Info 返回当前活动端点的脱敏信息，供日志和检查结果使用。
func (c *DynamicQwenClient) Info() ClientInfo {
	cfg, err := c.resolve(context.Background())
	if err != nil {
		return ClientInfo{}
	}
	return NewQwenClient(cfg).Info()
}

// CurrentModel 返回当前用途使用的模型名；没有活动配置时返回启动时回退值。
func (c *DynamicQwenClient) CurrentModel() string {
	return strings.TrimSpace(c.Info().Model)
}

var _ Client = (*DynamicQwenClient)(nil)
var _ InfoProvider = (*DynamicQwenClient)(nil)

// SetEndpointRecovery is wired once before workers start.
func (c *DynamicQwenClient) SetEndpointRecovery(g *EndpointRecovery)        { c.recovery = g }
func (c *DynamicQwenClient) SetNetworkToolsResolver(r NetworkToolsResolver) { c.networkTools = r }
func (c *DynamicQwenClient) Ready(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	cfg, err := c.resolve(ctx)
	return err == nil && (c.recovery == nil || c.recovery.remaining(cfg) <= 0)
}
func (c *DynamicQwenClient) WaitReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := c.resolve(ctx)
	if err != nil {
		return err
	}
	if c.recovery == nil {
		return ctx.Err()
	}
	return c.recovery.wait(ctx, cfg)
}
