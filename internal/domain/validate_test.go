package domain

import (
	"errors"
	"testing"
)

func validQuestionForTest() A2Question {
	return A2Question{
		ClinicalStem: "患者男性，50岁，突发胸痛2小时，最可能的诊断是？",
		Options: []Option{
			{Label: "A", Text: "急性心肌梗死"},
			{Label: "B", Text: "主动脉夹层"},
			{Label: "C", Text: "肺栓塞"},
			{Label: "D", Text: "气胸"},
		},
		Answer: "A",
	}
}

func TestQuestionValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*A2Question)
		valid  bool
	}{
		{name: "valid four options", valid: true},
		{name: "valid five options", mutate: func(q *A2Question) {
			q.Options = append(q.Options, Option{Label: "E", Text: "心包炎"})
		}, valid: true},
		{name: "blank stem", mutate: func(q *A2Question) { q.ClinicalStem = "  " }},
		{name: "too few options", mutate: func(q *A2Question) { q.Options = q.Options[:3] }},
		{name: "blank option label", mutate: func(q *A2Question) { q.Options[1].Label = " " }},
		{name: "blank option text", mutate: func(q *A2Question) { q.Options[1].Text = " " }},
		{name: "duplicate option label", mutate: func(q *A2Question) { q.Options[1].Label = "A" }},
		{name: "answer absent", mutate: func(q *A2Question) { q.Answer = "E" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := validQuestionForTest()
			if tt.mutate != nil {
				tt.mutate(&q)
			}
			err := q.Validate()
			if tt.valid && err != nil {
				t.Fatalf("合法题目校验失败: %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidQuestion) {
				t.Fatalf("不合法题目应返回 ErrInvalidQuestion，实际: %v", err)
			}
		})
	}
}
