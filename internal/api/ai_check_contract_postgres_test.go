//go:build integration

package api

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/domain"
	"aigo/internal/llm"
)

type contractAPIClient struct {
	responses []string
	calls     atomic.Int64
}

func (c *contractAPIClient) Complete(context.Context, []llm.Message, llm.GenerateOptions) (string, error) {
	index := min(int(c.calls.Add(1))-1, len(c.responses)-1)
	return c.responses[index], nil
}
func TestAICheckContractControlsVisibilityAndPersistentCounts(t *testing.T) {
	high := `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`
	bad := `{"verdict":"pass","scores":null,"issues":[]}`
	cases := []struct {
		name, mode, want string
		responses        []string
		attempts         int
		archiveVerdict   string
	}{
		{"baseline_preserves_model_pass_with_error", "knowledge", "passed", []string{`{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[{"field":"answer","severity":"error","message":"答案不唯一，与元数据无关"}]}`}, 1, "reject"},
		{"baseline_preserves_model_pass_with_low_score", "direct", "passed", []string{`{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":59},"issues":[]}`}, 1, "reject"},
		{"metadata_only_keeps_pass", "knowledge", "passed", []string{`{"verdict":"reject","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[{"field":"cognitive_level","severity":"error","message":"认知层次标签需修正"}]}`}, 1, ""},
		{"protocol_recovery", "knowledge", "passed", []string{bad, high}, 2, ""},
		{"protocol_exhaustion", "knowledge", "exhausted", []string{bad}, 3, ""},
		// Baseline replay: warning remains feedback; the model verdict is retained.
		{"reported_style_warning", "knowledge", "passed", []string{`{"verdict":"pass","scores":{"scientific":95,"logic":95,"a2_fit":85,"answer":100},"issues":[{"field":"expert_format","severity":"warning","message":"提问结尾应使用‘为’而非‘是’，轻微瑕疵不影响使用"}]}`}, 1, "issues_found"},
		{"reported_style_as_info", "knowledge", "passed", []string{`{"verdict":"pass","scores":{"scientific":95,"logic":95,"a2_fit":85,"answer":100},"issues":[{"field":"expert_format","severity":"info","message":"提问结尾建议使用‘为’而非‘是’，轻微瑕疵不影响使用"}]}`}, 1, ""},
		{"baseline_preserves_model_pass_with_warning", "knowledge", "passed", []string{`{"verdict":"pass","scores":{"scientific":95,"logic":95,"a2_fit":85,"answer":100},"issues":[{"field":"options","severity":"warning","message":"社区获得性肺炎与其病原亚型选项存在包含关系，答案不唯一；只是轻微瑕疵不影响使用"}]}`}, 1, "issues_found"},
		{"model_explicit_quality_failure", "knowledge", "discarded", []string{`{"verdict":"issues_found","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[{"field":"answer","severity":"warning","message":"模型明确判定需修改"}]}`}, 1, "issues_found"},
		{"model_explicit_rejection", "direct", "discarded", []string{`{"verdict":"reject","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":59},"issues":[{"field":"answer","severity":"error","message":"模型明确判定不通过"}]}`}, 1, "reject"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, store, handler, token, point, _, _, cleanup := setupWorkflowAPI(t)
			defer cleanup()
			client := &contractAPIClient{responses: tc.responses}
			checker := aicheck.NewService(client, store, store, store, "contract-test")
			checker.RetryBackoff = 10 * time.Millisecond
			server.aiCheckSvc = checker
			server.pipe.SetDraftChecker(checker)
			id := "contract-api-" + tc.name
			payload := generationPayload(id, point, 1)
			payload["workflow_mode"] = tc.mode
			response := serveGenerateJSON(t, handler, "POST", "/api/questions/generate", token, payload)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checker.StartWorkers(ctx, 2)
			server.pipe.StartGenerationWorkers(ctx, 2)
			var qid string
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				run, e := store.GetGenerationRun(t.Context(), id)
				if e != nil {
					t.Fatal(e)
				}
				if run.Status == domain.GenerationRunFailed {
					t.Fatal(run.Error)
				}
				if len(run.QuestionIDs) == 1 {
					qid = run.QuestionIDs[0]
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if qid == "" {
				t.Fatal("no generated question")
			}
			type progressResponse struct {
				Items  []aicheck.CheckProgress `json:"items"`
				Counts map[string]int          `json:"counts"`
			}
			read := func() progressResponse {
				r := serveGenerateJSON(t, handler, "POST", "/api/ai-check/progress", token, map[string]any{"question_ids": []string{qid}})
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body)
				}
				var p progressResponse
				if e := json.Unmarshal(r.Body.Bytes(), &p); e != nil {
					t.Fatal(e)
				}
				return p
			}
			var progress progressResponse
			for time.Now().Before(deadline) {
				progress = read()
				if progress.Counts[tc.want] == 1 {
					break
				}
				time.Sleep(15 * time.Millisecond)
			}
			if progress.Counts[tc.want] != 1 || len(progress.Items) != 1 || (tc.want != "discarded" && progress.Items[0].Attempts != tc.attempts) || client.calls.Load() != int64(tc.attempts) {
				t.Fatal(progress, client.calls.Load())
			}
			// A fresh HTTP read retains terminal counts without creating another task.
			again := read()
			if again.Counts[tc.want] != 1 {
				t.Fatal("terminal count not durable", again)
			}
			result := serveGenerateJSON(t, handler, "GET", "/api/generation-runs/"+id, token, nil)
			if result.Code != 200 {
				t.Fatal(result.Code, result.Body)
			}
			var runResponse struct {
				Questions []domain.A2Question `json:"questions"`
			}
			json.Unmarshal(result.Body.Bytes(), &runResponse)
			if (len(runResponse.Questions) == 1) != (tc.want == "passed") {
				t.Fatal("unpassed content visible", result.Body)
			}
			stored, e := store.GetQuestion(t.Context(), qid)
			if e != nil {
				t.Fatal(e)
			}
			if (stored != nil) != (tc.want == "passed") {
				t.Fatal("unpassed question retained", stored)
			}
			latest, e := store.GetLatestByQuestionID(t.Context(), qid)
			if e != nil {
				t.Fatal(e)
			}
			if tc.want == "passed" && latest == nil {
				t.Fatal("passed review missing")
			}
			if tc.want != "passed" && latest != nil {
				t.Fatal("discarded question left a live review", latest)
			}
			archives, e := store.ListDiscardResultsByQuestionIDs(t.Context(), []string{qid})
			if e != nil {
				t.Fatal(e)
			}
			if tc.want == "exhausted" {
				if len(archives) != 1 || archives[qid].Failure == nil || progress.Items[0].Scores != nil || progress.Items[0].Question != nil || progress.Items[0].StemSummary != "" || !progress.Items[0].CheckFailed {
					t.Fatal("invalid output became a medical result", progress, archives)
				}
			}
			if tc.want == "discarded" {
				if len(archives) != 1 || archives[qid].Failure != nil || archives[qid].Verdict != tc.archiveVerdict {
					t.Fatal("original model rejection archive missing", archives)
				}
			}
			var count int
			if e = store.DB().QueryRow(`SELECT count(*) FROM ai_check_tasks WHERE question_id=$1`, qid).Scan(&count); e != nil {
				t.Fatal(e)
			}
			expectedTasks := 0
			if tc.want == "passed" {
				expectedTasks = 1
			}
			if count != expectedTasks {
				t.Fatal("unexpected task lifecycle", count, expectedTasks)
			}

		})
	}
}
