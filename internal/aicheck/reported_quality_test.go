package aicheck

import (
	"strings"
	"testing"

	"aigo/internal/domain"
)

// Regression for the reported style warning: preserve the original model verdict.
func TestReportedStyleWarningPreservesBaselineVerdict(t *testing.T) {
	for _, ending := range []string{"是", "为"} {
		t.Run(ending, func(t *testing.T) {
			q := testDraft("reported-style-"+ending, 1)
			q.FormatVersion = domain.A2FormatSecondWave
			q.ClinicalStem = "患者，" + strings.TrimSuffix(q.ClinicalStem, "是") + ending
			q.NormalizeGeneratedA2()
			if err := q.ValidateA2ContentForReview(); err != nil {
				t.Fatal("both endings must satisfy the existing format", err)
			}
			if !strings.Contains(getCheckSystemPromptForQuestion(&q), "以“是/为”结束") {
				t.Fatal("checker no longer declares both endings acceptable")
			}
			for _, severity := range []string{"warning", "info"} {
				t.Run(severity, func(t *testing.T) {
					payload := validCheckPayload()
					payload["scores"] = map[string]any{"scientific": 95, "logic": 95, "a2_fit": 85, "answer": 100}
					payload["issues"] = []domain.ReviewIssue{{Field: "expert_format", Severity: severity, Message: "提问结尾建议使用‘为’而非‘是’，轻微瑕疵不影响使用"}}
					client := &contractClient{raw: contractJSON(t, payload)}
					svc, questions, results, _ := newTestService(client)
					if err := questions.SaveQuestion(t.Context(), q); err != nil {
						t.Fatal(err)
					}
					result, err := svc.CheckQuestion(t.Context(), q.ID)
					if err != nil {
						t.Fatal(err)
					}
					want := "pass"
					if result.Verdict != want || client.calls != 1 {
						t.Fatal(result.Verdict, client.calls)
					}
					stored, err := questions.GetQuestion(t.Context(), q.ID)
					if err != nil || (stored != nil) != (want == "pass") {
						t.Fatal("unexpected question visibility", stored, err)
					}
					archives, err := results.ListDiscardResultsByQuestionIDs(t.Context(), []string{q.ID})
					if err != nil || (len(archives) == 1) != (want != "pass") {
						t.Fatal("unexpected discard archive", archives, err)
					}
				})
			}
		})
	}
}

func TestReportedMedicalWarningDoesNotInventSecondVerdict(t *testing.T) {
	payload := validCheckPayload()
	payload["scores"] = map[string]any{"scientific": 95, "logic": 95, "a2_fit": 85, "answer": 100}
	payload["issues"] = []domain.ReviewIssue{{Field: "options", Severity: "warning", Message: "社区获得性肺炎与其病原亚型选项存在包含关系，答案不唯一；只是轻微瑕疵不影响使用"}}
	result, err := parseCheckResponse(contractJSON(t, payload), "reported-overlap", "controlled-replay")
	if err != nil || result.Verdict != "pass" {
		t.Fatal("server must preserve the baseline model verdict", result, err)
	}
}
