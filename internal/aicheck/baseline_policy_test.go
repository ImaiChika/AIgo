package aicheck

import (
	"aigo/internal/domain"
	"crypto/sha256"
	"fmt"
	"testing"
)

// The pre-workflow policy trusts a usable model verdict; warnings alone do not
// let the server invent a second quality verdict. Actual model rejections stay rejected.
func TestPreWorkflowVerdictPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, verdict, severity string
		score                   int
		want                    string
	}{
		{"reported_style_warning", "pass", "warning", 95, "pass"},
		{"model_medical_warning_with_pass", "pass", "warning", 85, "pass"},
		{"model_pass_low_score", "pass", "info", 69, "pass"},
		{"model_pass_inconsistent_error", "pass", "error", 59, "pass"},
		{"model_quality_failure", "issues_found", "warning", 85, "issues_found"},
		{"model_reject", "reject", "error", 40, "reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := validCheckPayload()
			p["verdict"] = tc.verdict
			p["scores"].(map[string]any)["answer"] = tc.score
			p["issues"] = []domain.ReviewIssue{{Field: "explanation", Severity: tc.severity, Message: "受控回放，不评价医学语义"}}
			client := &contractClient{raw: contractJSON(t, p)}
			svc, questions, _, _ := newTestService(client)
			q := testDraft(tc.name, 1)
			if err := questions.SaveQuestion(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			result, err := svc.CheckQuestion(t.Context(), q.ID)
			if err != nil || result.Verdict != tc.want {
				t.Fatalf("baseline verdict changed: result=%+v err=%v", result, err)
			}
			stored, err := questions.GetQuestion(t.Context(), q.ID)
			if err != nil || (stored != nil) != (tc.want == "pass") || client.calls != 1 {
				t.Fatal("visibility/call count", stored, err, client.calls)
			}
		})
	}
}

// Hashes were checked against e132491, the saved pre-workflow baseline.
// A deliberate future policy change must update these with user authorization.
func TestFirstCheckPromptMatchesPreWorkflowBaseline(t *testing.T) {
	for _, tc := range []struct{ name, format, system, user string }{
		{"legacy", "", "ea82bcbd3b6050fd480f5fb7a40b4cf13bdebcd4123db80aaeee09e7d44f1a2a", "8a486f154b58b78f4430198b892f5d60a5b6e531250944805fbdb83074e54147"},
		{"current", domain.A2FormatSecondWave, "24e513f83110c3a4f94c3b9b2b47409c610c1b0713c009725f80572112ae8229", "6b4ab59ba00a5a0ae1cd479fd3081133b79cab7db8c214c09f47eedbb0de7634"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := testDraft("baseline", 1)
			q.FormatVersion = tc.format
			if got := fmt.Sprintf("%x", sha256.Sum256([]byte(getCheckSystemPromptForQuestion(&q)))); got != tc.system {
				t.Fatal("system quality policy changed", got)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256([]byte(buildCheckPrompt(&q)))); got != tc.user {
				t.Fatal("user quality policy changed", got)
			}
		})
	}
}
