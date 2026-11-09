package aicheck

import (
	"aigo/internal/domain"
	"aigo/internal/llm"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

type contractClient struct {
	raw   string
	calls int
}

func (c *contractClient) Complete(context.Context, []llm.Message, llm.GenerateOptions) (string, error) {
	c.calls++
	return c.raw, nil
}
func validCheckPayload() map[string]any {
	return map[string]any{"verdict": "pass", "scores": map[string]any{"scientific": 90, "logic": 90, "a2_fit": 90, "answer": 90}, "issues": []any{}, "suggestion": "测试建议"}
}
func contractJSON(t *testing.T, p map[string]any) string {
	t.Helper()
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func TestResponseContractPreservesPreWorkflowVerdict(t *testing.T) {
	cases := []struct {
		name    string
		verdict string
		issue   domain.ReviewIssue
		score   string
		value   int
		want    string
	}{
		{name: "pass_with_core_error", verdict: "pass", issue: domain.ReviewIssue{Field: "answer", Severity: "error", Message: "给定答案错误"}, want: "pass"},
		{name: "pass_with_core_warning", verdict: "pass", issue: domain.ReviewIssue{Field: "explanation", Severity: "warning", Message: "解析依据不足"}, want: "pass"},
		{name: "pass_below_sixty", verdict: "pass", score: "answer", value: 59, want: "pass"},
		{name: "pass_between_sixty_and_seventy", verdict: "pass", score: "logic", value: 69, want: "pass"},
		{name: "core_field_with_metadata_keyword", verdict: "pass", issue: domain.ReviewIssue{Field: "answer", Severity: "error", Message: "答案错误，与元数据标签无关"}, want: "pass"},
		{name: "unknown_core_field_with_metadata_keyword", verdict: "pass", issue: domain.ReviewIssue{Field: "clinical_claim", Severity: "error", Message: "大纲代码对应的病例证据不足，无法唯一作答"}, want: "pass"},
		{name: "core_reject_mentions_metadata", verdict: "reject", issue: domain.ReviewIssue{Field: "answer", Severity: "error", Message: "答案错误，与元数据无关"}, want: "reject"},
		{name: "medical_rejection_plus_optional_metadata", verdict: "reject", issue: domain.ReviewIssue{Field: "answer", Severity: "warning", Message: "实质问题"}, want: "reject"},
		{name: "metadata_only_high_scores", verdict: "reject", issue: domain.ReviewIssue{Field: "cognitive_level", Severity: "error", Message: "认知层次标签需修正"}, want: "pass"},
		{name: "legacy_metadata_exemption_matches_baseline", verdict: "reject", score: "scientific", value: 45, issue: domain.ReviewIssue{Field: "difficulty", Severity: "error", Message: "难度标签需修正"}, want: "pass"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validCheckPayload()
			p["verdict"] = tc.verdict
			if tc.issue.Field != "" {
				p["issues"] = []domain.ReviewIssue{tc.issue}
			}
			if tc.score != "" {
				p["scores"].(map[string]any)[tc.score] = tc.value
			}
			client := &contractClient{raw: contractJSON(t, p)}
			svc, questions, results, _ := newTestService(client)
			q := testDraft("contract-"+tc.name, 1)
			if e := questions.SaveQuestion(t.Context(), q); e != nil {
				t.Fatal(e)
			}
			result, e := svc.CheckQuestion(t.Context(), q.ID)
			if e != nil {
				t.Fatal(e)
			}
			if result.Verdict != tc.want {
				t.Fatalf("got %s want %s issues=%+v", result.Verdict, tc.want, result.Issues)
			}
			saved, e := questions.GetQuestion(t.Context(), q.ID)
			if e != nil {
				t.Fatal(e)
			}
			if (saved != nil) != (tc.want == "pass") {
				t.Fatalf("visibility bypass: saved=%v verdict=%s", saved, result.Verdict)
			}
			if client.calls != 1 {
				t.Fatal("deterministic check added model calls", client.calls)
			}
			records, e := results.ListDiscardResultsByQuestionIDs(t.Context(), []string{q.ID})
			if e != nil {
				t.Fatal(e)
			}
			if (len(records) > 0) != (tc.want != "pass") {
				t.Fatal(records)
			}
		})
	}
}
func TestResponseContractRejectsMalformedProtocol(t *testing.T) {
	cases := []struct {
		name   string
		modify func(map[string]any)
	}{
		{"missing_verdict", func(p map[string]any) { delete(p, "verdict") }},
		{"unknown_verdict", func(p map[string]any) { p["verdict"] = "approved" }},
		{"missing_scores", func(p map[string]any) { delete(p, "scores") }},
		{"null_scores", func(p map[string]any) { p["scores"] = nil }},
		{"missing_score", func(p map[string]any) { delete(p["scores"].(map[string]any), "answer") }},
		{"null_score", func(p map[string]any) { p["scores"].(map[string]any)["logic"] = nil }},
		{"negative_score", func(p map[string]any) { p["scores"].(map[string]any)["scientific"] = -1 }},
		{"over_hundred_score", func(p map[string]any) { p["scores"].(map[string]any)["answer"] = 101 }},
		{"missing_issues", func(p map[string]any) { delete(p, "issues") }},
		{"null_issues", func(p map[string]any) { p["issues"] = nil }},
		{"unknown_severity", func(p map[string]any) {
			p["issues"] = []any{map[string]any{"field": "answer", "severity": "fatal", "message": "错误"}}
		}},
		{"empty_issue_message", func(p map[string]any) {
			p["issues"] = []any{map[string]any{"field": "answer", "severity": "error", "message": " "}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validCheckPayload()
			tc.modify(p)
			if result, e := parseCheckResponse(contractJSON(t, p), "q", "test"); e == nil {
				t.Fatalf("malformed protocol accepted: %+v", result)
			}
		})
	}
}

func TestResponseContractDuplicateKeysAndMetadataFeedback(t *testing.T) {
	valid := contractJSON(t, validCheckPayload())
	for _, raw := range []string{
		`{"verdict":"reject","verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`,
		`{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":0,"Answer":90},"issues":[]}`,
		`{"verdict":"pass","scores":{"scientific":0,"ſcientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`,
	} {
		if _, e := parseCheckResponse(raw, "q", "test"); !errors.Is(e, ErrCheckProtocol) {
			t.Fatal("duplicate key was not a protocol failure", e)
		}
	}
	p := validCheckPayload()
	p["metadata_feedback"] = []any{map[string]any{"field": "difficulty", "message": "预估难度可调整"}}
	result, e := parseCheckResponse(contractJSON(t, p), "q", "test")
	if e != nil || result.Verdict != "pass" || len(result.Issues) != 1 || result.Issues[0].Severity != "info" {
		t.Fatal(result, e)
	}
	p["verdict"] = "reject"
	result, e = parseCheckResponse(contractJSON(t, p), "q", "test")
	if e != nil || result.Verdict != "reject" {
		t.Fatal("optional feedback changed model verdict", result, e)
	}
	p["metadata_feedback"] = []any{map[string]any{"field": "answer", "message": "给定答案错误"}}
	if _, e = parseCheckResponse(contractJSON(t, p), "q", "test"); !errors.Is(e, ErrCheckProtocol) {
		t.Fatal("core issue hidden in metadata feedback", e)
	}
	if _, e = parseCheckResponse("```json\n"+valid+"\n```", "q", "test"); e != nil {
		t.Fatal("fenced response compatibility lost", e)
	}
	p = validCheckPayload()
	p["scores"].(map[string]any)["answer"] = 0
	result, e = parseCheckResponse(contractJSON(t, p), "q", "test")
	if e != nil || result.Verdict != "pass" {
		t.Fatal("a valid numeric score must not invent a second verdict", result, e)
	}
}

type contractSequenceClient struct {
	responses []string
	calls     int
}

func (c *contractSequenceClient) Complete(context.Context, []llm.Message, llm.GenerateOptions) (string, error) {
	index := min(c.calls, len(c.responses)-1)
	c.calls++
	return c.responses[index], nil
}
func TestResponseContractUsesOriginalTaskRetryAndExhaustion(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				responses := []string{`{"verdict":"pass","scores":null,"issues":[]}`}
				if recover {
					responses = append(responses, contractJSON(t, validCheckPayload()))
				}
				client := &contractSequenceClient{responses: responses}
				svc, questions, reviews, tasks := newTestService(client)
				ctx := context.Background()
				q := testDraft("protocol-retry", 1)
				if e := questions.SaveQuestion(ctx, q); e != nil {
					t.Fatal(e)
				}
				svc.CheckAsync(q.ID)
				limit := 3
				if recover {
					limit = 2
				}
				for i := 0; i < limit; i++ {
					task, e := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration())
					if e != nil || task == nil {
						t.Fatal(i, task, e)
					}
					svc.executeTask(ctx, task)
					if i < limit-1 {
						stored, _ := questions.GetQuestion(ctx, q.ID)
						result, _ := reviews.GetLatestByQuestionID(ctx, q.ID)
						if stored == nil || result != nil {
							t.Fatal("unusable response became a medical result", stored, result)
						}
						if next, e := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration()); e != nil || next != nil {
							t.Fatal("retry bypassed backoff", next, e)
						}
						delay := svc.retryBackoffFor(i + 1)
						if delay != time.Duration(15*(1<<i))*time.Second {
							t.Fatal("default backoff changed", delay)
						}
						time.Sleep(delay)
					}
				}
				svc.CheckAsync(q.ID)
				if next, e := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration()); e != nil || next != nil || client.calls != limit {
					t.Fatal("task repeated or exceeded budget", next, e, client.calls)
				}
				stored, _ := questions.GetQuestion(ctx, q.ID)
				items, counts, e := svc.ProgressByQuestionIDs(ctx, []string{q.ID})
				if e != nil || len(items) != 1 {
					t.Fatal(items, counts, e)
				}
				if recover {
					if stored == nil || stored.Status != domain.StatusAIReviewed || counts["passed"] != 1 || items[0].Attempts != 2 {
						t.Fatal(stored, items, counts)
					}
				} else {
					if stored != nil || counts["exhausted"] != 1 || counts["passed"] != 0 || !items[0].CheckFailed || items[0].Attempts != 3 || items[0].Scores != nil || items[0].StemSummary != "" {
						t.Fatal(stored, items, counts)
					}
				}
			})
		})
	}
}
