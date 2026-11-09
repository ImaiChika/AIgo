package generator

import (
	"aigo/internal/llm"
	"context"
	"fmt"

	"aigo/internal/domain"
)

type generationAttemptKey struct{}

// The queue owns attempts. Callers cannot select this via a generation request.
func WithGenerationAttempt(ctx context.Context, attempt int) context.Context {
	return context.WithValue(llm.WithCallAttempt(ctx, attempt), generationAttemptKey{}, attempt)
}

func generationRetryReminder(ctx context.Context, req domain.GenerationRequest) string {
	attempt, _ := ctx.Value(generationAttemptKey{}).(int)
	if attempt <= 1 {
		return ""
	}
	return fmt.Sprintf("\n\n【原任务自动重试：输出格式提醒】\n本次为第%d次尝试。任务资料、考查目标和个人规则仍按上文，不改变医学要求。请只返回恰好%d道的完整合法JSON数组，每道为独立对象，遵守本次已有字段协议。各对象用逗号分隔，花括号与方括号分别正确闭合，避免把下一道题写进上一题的嵌套字段；字符串中的引号/换行须正确转义。不输出前言、Markdown或检查过程，不省略任何题目。", attempt, req.Count)
}
