package aicheck

import (
	"aigo/internal/domain"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestFocusedCheckKeepsOldPromptAndUsesConsistentAdvice(t *testing.T) {
	q := testDraft("frozen-context", 1)
	q.FormatVersion = domain.A2FormatContextual
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(getCheckSystemPromptForQuestion(&q)))); got != "77197f56de9d6a1338ca61d1ce8e516c664cd3855e18e73ee74c5249aea04453" {
		t.Fatal("accepted context-v1 changed", got)
	}
	oldUser := buildCheckPrompt(&q)
	q.FormatVersion = domain.A2FormatFocused
	for _, want := range []string{"非典型但不矛盾", "解析可靠性", "先排除合理推断", "pass不能同时", "不作为新增质量门槛"} {
		if !strings.Contains(getCheckSystemPromptForQuestion(&q), want) {
			t.Fatal("missing focused decision rule", want)
		}
	}
	if promptPolicyForQuestion(&q) != focusedCheckPolicy || !strings.HasPrefix(buildCheckPrompt(&q), oldUser) {
		t.Fatal("lost old contract or new policy routing")
	}
}

func TestFocusedAdviceKeepsMandatoryCheckAndBaselineVerdictHandling(t *testing.T) {
	for _, tc := range []struct{ name, verdict, severity string }{
		{"style_advice", "pass", "info"},
		{"medical_problem", "issues_found", "warning"},
		{"inconsistent_provider_not_auto_rejected", "pass", "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := validCheckPayload()
			payload["verdict"] = tc.verdict
			payload["issues"] = []domain.ReviewIssue{{Field: "explanation", Severity: tc.severity, Message: "受控响应，只核验原判定及落库行为"}}
			client := &contractClient{raw: contractJSON(t, payload)}
			svc, questions, _, _ := newTestService(client)
			q := testDraft("focused-"+tc.name, 1)
			q.FormatVersion = domain.A2FormatFocused
			q.NormalizeGeneratedA2()
			if err := questions.SaveQuestion(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			result, err := svc.CheckQuestion(t.Context(), q.ID)
			stored, e := questions.GetQuestion(t.Context(), q.ID)
			if err != nil || e != nil || result.Verdict != tc.verdict || (stored != nil) != (tc.verdict == "pass") || client.calls != 1 {
				t.Fatal(result, stored, err, e, client.calls)
			}
			if _, err = svc.CheckQuestion(t.Context(), q.ID); err != nil || client.calls != 1 {
				t.Fatal("logical first check repeated", err, client.calls)
			}
		})
	}
}

func TestContextCheckPromptSeparatesEvidenceFromPolish(t *testing.T) {
	q := testDraft("new-context", 1)
	q.FormatVersion = domain.A2FormatContextual
	system := getCheckSystemPromptForQuestion(&q)
	user := buildCheckPrompt(&q)
	for _, want := range []string{"唯一最佳", "合理推断", "罕见", "info", "不能仅因", "实际影响"} {
		if !strings.Contains(system, want) {
			t.Fatal("missing contextual check principle", want)
		}
	}
	if strings.Contains(system, "治疗原则不使用“首选”") || !strings.Contains(user, "<a2_question_data>") {
		t.Fatal("new checker still uses old lexical gate or unbounded data")
	}
	if getCheckSystemPromptForQuestion(&domain.A2Question{FormatVersion: domain.A2FormatSecondWave}) == system {
		t.Fatal("old policy was replaced")
	}
}

func TestContextStyleAdviceDoesNotDeleteQuestionOrRecheck(t *testing.T) {
	payload := validCheckPayload()
	payload["issues"] = []domain.ReviewIssue{{Field: "stem", Severity: "info", Message: "提问可简练，但病例条件支持本题判断"}}
	client := &contractClient{raw: contractJSON(t, payload)}
	svc, questions, _, _ := newTestService(client)
	q := testDraft("context-wording", 1)
	q.FormatVersion = domain.A2FormatContextual
	q.ClinicalStem = "患者，男，45岁。上腹痛2年。该患者首选的根本治疗原则是"
	q.NormalizeGeneratedA2()
	if err := questions.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	result, err := svc.CheckQuestion(t.Context(), q.ID)
	if err != nil || result.Verdict != "pass" {
		t.Fatal("wording forced a rejection", result, err)
	}
	stored, _ := questions.GetQuestion(t.Context(), q.ID)
	if stored == nil || stored.Status != domain.StatusAIReviewed {
		t.Fatal("good question deleted")
	}
	if _, err = svc.CheckQuestion(t.Context(), q.ID); err != nil || client.calls != 1 {
		t.Fatal("cached result triggered a recheck", err, client.calls)
	}
}
