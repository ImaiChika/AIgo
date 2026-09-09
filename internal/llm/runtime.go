package llm

import (
	"context"
	"strings"
)

// ConfigPurpose 标识实时调用的业务用途。
type ConfigPurpose string

const (
	PurposeGeneration ConfigPurpose = "generation"
	PurposeAICheck    ConfigPurpose = "ai_check"
	PurposeBatch      ConfigPurpose = "batch"
)

// RuntimeConfigResolver 在每次请求前解析当前活动的模型配置。
// 数据库实现可以在后台切换配置，正在运行的服务无需重启。
type RuntimeConfigResolver interface {
	ResolveQwenConfig(ctx context.Context, purpose ConfigPurpose) (QwenConfig, bool, error)
}

// DynamicQwenClient 是一个很薄的动态路由层：实时生成/检查请求都在调用时
// 读取活动配置，未配置时回退到启动时的环境配置，兼容旧部署方式。
type DynamicQwenClient struct {
	resolver RuntimeConfigResolver
	purpose  ConfigPurpose
	fallback QwenConfig
}

// NewDynamicQwenClient 创建动态 Qwen 客户端。
func NewDynamicQwenClient(resolver RuntimeConfigResolver, purpose ConfigPurpose, fallback QwenConfig) *DynamicQwenClient {
	return &DynamicQwenClient{resolver: resolver, purpose: purpose, fallback: fallback}
}

func (c *DynamicQwenClient) resolve(ctx context.Context) QwenConfig {
	if c.resolver != nil {
		if cfg, ok, err := c.resolver.ResolveQwenConfig(ctx, c.purpose); err == nil && ok {
			return cfg
		}
	}
	return c.fallback
}

// Complete 按当前活动配置创建短生命周期 HTTP 客户端并发起一次请求。
// 客户端本身不持有 API Key，配置切换会自然作用于下一次调用。
func (c *DynamicQwenClient) Complete(ctx context.Context, messages []Message, opts GenerateOptions) (string, error) {
	return NewQwenClient(c.resolve(ctx)).Complete(ctx, messages, opts)
}

// Info 返回当前活动端点的脱敏信息，供日志和检查结果使用。
func (c *DynamicQwenClient) Info() ClientInfo {
	return NewQwenClient(c.resolve(context.Background())).Info()
}

// CurrentModel 返回当前用途使用的模型名；没有活动配置时返回启动时回退值。
func (c *DynamicQwenClient) CurrentModel() string {
	return strings.TrimSpace(c.Info().Model)
}

var _ Client = (*DynamicQwenClient)(nil)
var _ InfoProvider = (*DynamicQwenClient)(nil)
