package domain

import "errors"

// Validate 校验 A2Question 是否满足基本要求。
// 校验规则：
// 1. 题干不能为空
// 2. 选项数量必须在 4-5 个之间（兼容四选项历史题）
// 3. 答案不能为空
// 4. 答案标签必须在选项列表中存在
func (q *A2Question) Validate() error {
	if q.ClinicalStem == "" {
		return errors.New("题干不能为空")
	}
	if len(q.Options) < 4 || len(q.Options) > 5 {
		return errors.New("选项数量必须在4到5个之间")
	}
	if q.Answer == "" {
		return errors.New("答案不能为空")
	}
	// 检查答案标签是否在选项中
	found := false
	for _, opt := range q.Options {
		if opt.Label == q.Answer {
			found = true
			break
		}
	}
	if !found {
		return errors.New("答案不在选项中")
	}
	return nil
}
