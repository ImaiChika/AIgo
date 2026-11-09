package domain

import "testing"

func TestContextA2JudgesStructureWithoutPhraseBlacklist(t *testing.T) {
	for _, tc := range []struct{ name, lead string }{
		{"qualified principle", "该患者首选的根本治疗原则是"},
		{"best option", "该患者最合理的处理是"},
		{"redundant purpose", "为明确诊断，最有价值的确诊性检查是"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := validExpertA2Question()
			q.ClinicalStem = "患者，男，45岁。反复上腹痛2年。" + tc.lead
			q.FormatVersion = A2FormatContextual
			q.NormalizeGeneratedA2()
			if err := q.ValidateGeneratedA2(); err != nil {
				t.Fatal("context wording should be assessed semantically", err)
			}
			if tc.name != "best option" {
				q.FormatVersion = A2FormatSecondWave
				if err := q.ValidateGeneratedA2(); err == nil {
					t.Fatal("historical policy changed")
				}
			}
		})
	}
}

func TestContextA2PreservesCoreOutputGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*A2Question)
	}{
		{"missing answer", func(q *A2Question) { q.Answer = "" }},
		{"four options", func(q *A2Question) { q.Options = q.Options[:4] }},
		{"missing question", func(q *A2Question) { q.ClinicalStem = "患者，男，45岁。上腹痛2年。" }},
		{"missing explanation for option", func(q *A2Question) { q.Explanation = "正确答案为A。结论。故选A。" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := validExpertA2Question()
			q.FormatVersion = A2FormatContextual
			q.NormalizeGeneratedA2()
			tc.mutate(&q)
			if err := q.ValidateGeneratedA2(); err == nil {
				t.Fatal("core guard removed")
			}
		})
	}
}
