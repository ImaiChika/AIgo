package domain

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidQuestion 标识题目内容不满足进入审核或正式题库的基本条件。
// API 层可用 errors.Is 将此类错误映射为 400，而不会把数据库故障误报为参数错误。
var ErrInvalidQuestion = errors.New("题目格式不合格")

func invalidQuestionError(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuestion, message)
}

// Validate 校验 A2Question 是否满足基本要求。
// 校验规则：
// 1. 题干不能为空
// 2. 选项数量必须在 4-5 个之间（兼容四选项历史题）
// 3. 答案不能为空
// 4. 每个选项必须有非空且不重复的标签和非空内容
// 5. 答案标签必须在选项列表中唯一存在
func (q *A2Question) Validate() error {
	if q == nil {
		return invalidQuestionError("题目不能为空")
	}
	if strings.TrimSpace(q.ClinicalStem) == "" {
		return invalidQuestionError("题干不能为空")
	}
	if len(q.Options) < 4 || len(q.Options) > 5 {
		return invalidQuestionError("选项数量必须在4到5个之间")
	}
	answer := strings.TrimSpace(q.Answer)
	if answer == "" {
		return invalidQuestionError("答案不能为空")
	}

	labels := make(map[string]struct{}, len(q.Options))
	answerMatches := 0
	for i, opt := range q.Options {
		label := strings.TrimSpace(opt.Label)
		if label == "" {
			return invalidQuestionError(fmt.Sprintf("第%d个选项标签不能为空", i+1))
		}
		if strings.TrimSpace(opt.Text) == "" {
			return invalidQuestionError(fmt.Sprintf("选项%s内容不能为空", label))
		}
		if _, exists := labels[label]; exists {
			return invalidQuestionError(fmt.Sprintf("选项标签%s重复", label))
		}
		labels[label] = struct{}{}
		if label == answer {
			answerMatches++
		}
	}
	if answerMatches != 1 {
		return invalidQuestionError("答案必须在选项中唯一存在")
	}
	return nil
}
