// Package llm 提供大语言模型（LLM）调用接口。
// 当前实现为阿里云百炼千问 OpenAI 兼容接口。
package llm

import "context"

// DeploymentMode 描述模型服务部署位置。协议统一使用 OpenAI-compatible，
// 部署位置只影响鉴权要求和少量 Qwen 扩展参数的编码方式。
type DeploymentMode string

const (
	DeploymentCloud DeploymentMode = "cloud"
	DeploymentLocal DeploymentMode = "local"
)

// Role 消息角色。
type Role string

const (
	RoleSystem    Role = "system"    // 系统提示词
	RoleUser      Role = "user"      // 用户输入
	RoleAssistant Role = "assistant" // AI 回复
)

// Message 对话消息。
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// GenerateOptions 生成参数。
type GenerateOptions struct {
	Temperature    float64 // 温度（0-1），越高越随机
	MaxTokens      int     // 最大输出 token 数；<=0 时不向模型服务发送限制参数
	EnableThinking *bool   // nil=使用模型服务默认值；非 nil=显式开关思考模式
}

// ClientInfo 是可安全用于日志或只读管理状态的运行信息，不包含密钥。
type ClientInfo struct {
	Deployment     DeploymentMode `json:"deployment"`
	Protocol       string         `json:"protocol"`
	Model          string         `json:"model"`
	BaseURL        string         `json:"-"`
	AuthConfigured bool           `json:"auth_configured"`
}

// InfoProvider 由能够描述自身运行配置的客户端选择性实现。
// Client 保持最小接口，现有 Mock 和未来非 HTTP 实现无需被迫提供元数据。
type InfoProvider interface {
	Info() ClientInfo
}

// Client LLM 客户端接口，支持不同模型的实现。
type Client interface {
	// Complete 发送对话消息并返回 AI 回复文本。
	Complete(ctx context.Context, messages []Message, opts GenerateOptions) (string, error)
}
