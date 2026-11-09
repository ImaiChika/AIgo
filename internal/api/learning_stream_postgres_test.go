//go:build integration

package api

import (
	"aigo/internal/domain"
	"aigo/internal/learning"
	"aigo/internal/llm"
	"aigo/internal/storage/postgres"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLearningStreamingHTTPRecoveryKeepsOriginalQueueGuards(t *testing.T) {
	for _, mode := range []string{"complete", "missing_done_once", "invalid_candidates_once", "attempt_timeout_once", "reasoning_only_once", "two_timeouts", "missing_usage", "legacy_retry"} {
		t.Run(mode, func(t *testing.T) {
			s, h, teacher, version, cleanup := libraryTestEnv(t)
			t.Cleanup(cleanup)
			owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
			perms := append(append([]string{}, owner.DirectPermissions...), domain.PermLearningAnalyze, domain.PermLearningSave)
			if _, e := s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, perms, nil, nil); e != nil {
				t.Fatal(e)
			}
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				var req map[string]any
				json.NewDecoder(r.Body).Decode(&req)
				wantTemperature := 0.6
				if mode == "legacy_retry" {
					wantTemperature = 0.2
				}
				if req["temperature"] != wantTemperature {
					t.Errorf("sampling = %v, want %v", req["temperature"], wantTemperature)
				}
				if req["stream"] != true {
					t.Error("worker did not enable stream")
				}
				for _, key := range []string{"max_tokens", "max_completion_tokens", "thinking_budget"} {
					if _, ok := req[key]; ok {
						t.Error("unexpected token cap", key)
					}
				}
				w.Header().Set("X-Request-ID", fmt.Sprintf("stream-call-%d", n))
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"PRIVATE_REASONING_NOT_A_CANDIDATE\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if mode == "two_timeouts" || mode == "attempt_timeout_once" && n == 1 {
					<-r.Context().Done()
					return
				}

				if mode == "reasoning_only_once" && n == 1 {
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":900,\"total_tokens\":912,\"completion_tokens_details\":{\"reasoning_tokens\":0}}}\n\ndata: [DONE]\n\n")
					return
				}
				answer := []domain.LearningCandidate{}
				for _, style := range []string{"concise", "detailed", "checklist"} {
					answer = append(answer, domain.LearningCandidate{ID: style, Style: style, Title: "表达经验", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: style + "-rule", Text: "保留病例决策线索", Field: "clinical_stem", Before: "旧表达", After: "新表达"}}})
				}
				raw, _ := json.Marshal(map[string]any{"candidates": answer})
				if (mode == "invalid_candidates_once" || mode == "legacy_retry") && n == 1 {
					raw = []byte(`{"candidates":[]}`)
				}
				runes := []rune(string(raw))
				for _, piece := range []string{string(runes[:len(runes)/2]), string(runes[len(runes)/2:])} {
					frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": piece}}}})
					fmt.Fprintf(w, "data: %s\n\n", frame)
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				if mode != "missing_usage" {
					fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":900,\"total_tokens\":912,\"completion_tokens_details\":{\"reasoning_tokens\":400}}}\n\n")
				}
				if mode != "missing_done_once" || n != 1 {
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			t.Cleanup(provider.Close)
			store := s.questionStore.(*postgres.Store)
			client := llm.NewDynamicQwenClient(nil, llm.PurposeLearning, llm.QwenConfig{APIKey: "test", BaseURL: provider.URL, Model: "qwen3.5-flash", HTTPTimeout: 3 * time.Second})
			client.SetCallRecorder(store)
			service := learning.NewService(store, client)
			service.PollInterval = 5 * time.Millisecond
			s.SetLearningService(service)
			q := modifiedLearningSource(t, s, h, teacher, version, "stream-"+mode)
			job := submitLearningFixture(t, h, teacher, q, "stream-request")
			if _, e := store.DB().ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET body=body||'{"attempt_timeout_seconds":1,"execution_budget_seconds":8}'::jsonb WHERE id=$1`, job.ID); e != nil {
				t.Fatal(e)
			}

			if mode == "legacy_retry" {
				if _, e := store.DB().ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET body=body-'sampling_policy'-'prompt_policy' WHERE id=$1`, job.ID); e != nil {
					t.Fatal(e)
				}
				replay := submitLearningFixture(t, h, teacher, q, "stream-request")
				if replay.SamplingPolicy != "" || replay.PromptPolicy != "" {
					t.Fatal("replay upgraded legacy sampling")
				}
			}
			second := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/learning-analyses", teacher, learningRequest(q, "second-active"))
			if second.Code != 429 || second.Header().Get("Retry-After") == "" {
				t.Fatal("same account admitted second in-flight", second.Code)
			}
			ctx, cancel := context.WithCancel(t.Context())
			service.StartWorkers(ctx, 2)
			t.Cleanup(func() {
				cancel()
				select {
				case <-service.WorkersDone():
				case <-time.After(time.Second):
					t.Error("streaming worker did not stop")
				}
			})
			done := waitLearningJob(t, h, teacher, job.ID)
			wantAttempts := 1
			if strings.HasSuffix(mode, "_once") || mode == "two_timeouts" || mode == "legacy_retry" {
				wantAttempts = 2
			}
			wantStatus := "succeeded"
			if mode == "two_timeouts" {
				wantStatus = "failed"
			}
			if done.Status != wantStatus || done.Attempts != wantAttempts || int(calls.Load()) != wantAttempts {
				t.Fatal("unexpected retry outcome", done, calls.Load())
			}
			if len(done.AttemptHistory) != wantAttempts || len(done.ModelCallIDs) != wantAttempts {
				t.Fatal("attempt evidence lost", done)
			}
			if mode == "attempt_timeout_once" && done.AttemptHistory[0].ErrorKind != "attempt_timeout" {
				t.Fatal(done.AttemptHistory)
			}
			if mode == "invalid_candidates_once" && done.AttemptHistory[0].ErrorKind != "protocol_error" {
				t.Fatal(done.AttemptHistory)
			}
			if wantStatus == "succeeded" && (len(done.Candidates) != 3 || done.Candidates[0].Rules[0].Text != "保留病例决策线索") {
				t.Fatal("partial candidate published", done)
			}
			unchanged := mustLibraryQuestion(t, s, q.ID)
			if unchanged.Version != q.Version || unchanged.ClinicalStem != q.ClinicalStem {
				t.Fatal("analysis changed source")
			}
			for _, id := range done.ModelCallIDs {
				var raw []byte
				if e := store.DB().QueryRowContext(t.Context(), `SELECT record FROM model_call_records WHERE id=$1`, id).Scan(&raw); e != nil {
					t.Fatal(e)
				}
				if strings.Contains(string(raw), "PRIVATE_REASONING") {
					t.Fatal("reasoning persisted")
				}
				var evidence llm.ModelCallRecord
				if json.Unmarshal(raw, &evidence) != nil || !evidence.Streamed || evidence.ReasoningCharacters == 0 || evidence.LastStreamContentMS == 0 || evidence.UpstreamRequestID != fmt.Sprintf("stream-call-%d", evidence.Attempt) {
					t.Fatal("stream evidence missing", string(raw))
				}
				wantTemperature := 0.6
				if mode == "legacy_retry" {
					wantTemperature = 0.2
				}
				if evidence.RequestedTemperature == nil || *evidence.RequestedTemperature != wantTemperature {
					t.Fatal("sampling audit mismatch", evidence)
				}
				if mode == "missing_usage" && evidence.UsageReported {
					t.Fatal("missing usage counted as reported")
				}
				wantReasoning := 400
				if mode == "reasoning_only_once" && evidence.Attempt == 1 {
					wantReasoning = 0
					if evidence.FirstAnswerMS != 0 || evidence.StreamFinishReason != "stop" {
						t.Fatal("reasoning-only failure lost its evidence", evidence)
					}
				}
				if evidence.UsageReported && (!evidence.ReasoningTokensReported || evidence.ReasoningTokens != wantReasoning) {
					t.Fatal("reasoning usage missing", evidence)
				}
			}
		})
	}
}
