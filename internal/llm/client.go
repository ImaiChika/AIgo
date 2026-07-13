// Package llm 提供大语言模型（LLM）调用接口。
// 当前实现为阿里云百炼千问 OpenAI 兼容接口。
package llm

import "context"

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
	Temperature float64 // 温度（0-1），越高越随机
	MaxTokens   int     // 最大输出 token 数
}

// Client LLM 客户端接口，支持不同模型的实现。
type Client interface {
	// Complete 发送对话消息并返回 AI 回复文本。
	Complete(ctx context.Context, messages []Message, opts GenerateOptions) (string, error)
}
