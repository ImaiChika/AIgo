package llm

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

type GenerateOptions struct {
	Temperature float64
	MaxTokens   int
}

type Client interface {
	Complete(ctx context.Context, messages []Message, opts GenerateOptions) (string, error)
}
