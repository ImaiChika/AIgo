package domain

import (
	"errors"
)

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
