//go:build integration

package api

import (
	"aigo/internal/aicheck"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage/postgres"
	"aigo/internal/workflowtrace"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type executionFaultClient struct {
	base                workflowAPIClient
	mode                string
	generations, checks atomic.Int32
}

func (c *executionFaultClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	prompt := m[len(m)-1].Content
	if strings.Contains(prompt, "请检查以下 A2 型试题的质量") {
		c.checks.Add(1)
		if c.mode == "reject" {
			return `{"verdict":"reject","scores":{"scientific":40,"logic":40,"a2_fit":90,"answer":40},"issues":[{"field":"answer","severity":"error","message":"受控模型明确不通过"}]}`, nil
		}
		return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`, nil
	}
	attempt := c.generations.Add(1)
	raw, err := c.base.Complete(ctx, m, o)
	if err != nil || attempt != 1 {
		return raw, err
	}
	switch c.mode {
	case "json":
		return "{PRIVATE-BAD-JSON", nil
	case "knowledge", "a2":
		var items []map[string]any
		if err = json.Unmarshal([]byte(raw), &items); err != nil {
			return "", err
		}
		if c.mode == "knowledge" {
			items[0]["knowledge_basis"].(map[string]any)["fact_refs"] = []string{"missing-fact"}
		} else {
			plan := items[0]["case_plan"]
			if basis, ok := items[0]["knowledge_basis"].(map[string]any); ok {
				plan = basis["case_plan"]
			}
			plan.(map[string]any)["lead_in"] = "该患者最可能的诊断"
		}
		result, _ := json.Marshal(items)
		return string(result), nil
	}
	return raw, nil
}

type failedExecutionRecorder struct{ *postgres.Store }

func (s *failedExecutionRecorder) SaveWorkflowExecution(context.Context, workflowtrace.Record) error {
	return errors.New("PRIVATE-DIAGNOSTIC-FAILURE")
}

func TestWorkflowExecutionFailureStagesRecoveryAndDiscardAreDistinct(t *testing.T) {
	for _, mode := range []string{"json", "knowledge", "a2", "handoff", "diagnostics", "reject"} {
		t.Run(mode, func(t *testing.T) {
			s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
			defer cleanup()
			client := &executionFaultClient{mode: mode}
			checker := aicheck.NewService(client, store, store, store, "execution-controlled")
			s.aiCheckSvc = checker
			if mode == "diagnostics" {
				s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, &failedExecutionRecorder{Store: store})
			} else {
				s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
			}
			s.pipe.SetDraftChecker(checker)
			s.pipe.GenerationPollInterval = time.Millisecond
			s.pipe.GenerationRetryBackoff = 10 * time.Millisecond
			if mode == "handoff" {
				// Sequence increments survive rollback: fail exactly the first real INSERT.
				_, err := store.DB().Exec(`CREATE SEQUENCE execution_fail_once;
 CREATE FUNCTION execution_inject_insert_failure() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN IF nextval('execution_fail_once')=1 THEN RAISE EXCEPTION 'controlled first-insert fault'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER execution_insert_fault BEFORE INSERT ON questions FOR EACH ROW EXECUTE FUNCTION execution_inject_insert_failure()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			runID := "execution-fault-" + mode
			payload := generationPayload(runID, p, 1)
			if mode == "a2" {
				payload["workflow_mode"] = "direct"
			}
			response := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, payload)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checker.StartWorkers(ctx, 2)
			s.pipe.StartGenerationWorkers(ctx, 2)
			expectedAttempts := 2
			if mode == "diagnostics" || mode == "reject" {
				expectedAttempts = 1
			}
			wanted := "passed"
			if mode == "reject" {
				wanted = "discarded"
			}
			deadline := time.Now().Add(10 * time.Second)
			var qID string
			for time.Now().Before(deadline) {
				run, err := store.GetGenerationRun(t.Context(), runID)
				if err != nil {
					t.Fatal(err)
				}
				if run.Status == domain.GenerationRunFailed {
					t.Fatal(run.Error)
				}
				if len(run.QuestionIDs) == 1 {
					_, counts, e := checker.ProgressByQuestionIDs(t.Context(), run.QuestionIDs)
					if e == nil && counts[wanted] == 1 {
						qID = run.QuestionIDs[0]
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if qID == "" || client.generations.Load() != int32(expectedAttempts) || client.checks.Load() != 1 {
				t.Fatal("business flow or retries changed", qID, client.generations.Load(), client.checks.Load())
			}
			var executions []workflowtrace.Record
			for time.Now().Before(deadline) {
				rows, err := store.DB().QueryContext(t.Context(), `SELECT record FROM workflow_execution_records WHERE operation=$1 AND phase='generation' ORDER BY attempt`, "generate:"+runID)
				if err != nil {
					t.Fatal(err)
				}
				executions = nil
				for rows.Next() {
					var raw []byte
					var record workflowtrace.Record
					if err = rows.Scan(&raw); err != nil {
						t.Fatal(err)
					}
					if err = json.Unmarshal(raw, &record); err != nil {
						t.Fatal(err)
					}
					executions = append(executions, record)
				}
				if err = rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				if len(executions) == expectedAttempts || mode == "diagnostics" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if mode == "diagnostics" {
				if len(executions) != 0 {
					t.Fatal("fault injection did not run")
				}
				return
			}
			if len(executions) != expectedAttempts || executions[len(executions)-1].Outcome != "committed" || len(executions[len(executions)-1].QuestionIDs) != 1 || executions[len(executions)-1].QuestionIDs[0] != qID {
				t.Fatal(executions)
			}
			if expectedAttempts == 2 {
				failedStage := map[string]string{"json": "json_parse", "knowledge": "knowledge_validation", "a2": "a2_validation", "handoff": "handoff"}[mode]
				failed, modelReturned := false, false
				for _, stage := range executions[0].Stages {
					if stage.Name == failedStage && stage.Status == "failed" {
						failed = true
					}
					if stage.Name == "model_call" && stage.Status == "returned" {
						modelReturned = true
					}
				}
				if !failed || !modelReturned || executions[0].Outcome == "committed" || len(executions[0].QuestionIDs) > 0 {
					t.Fatal("wrong failure boundary", executions[0])
				}
			}
			current, err := store.GetQuestion(t.Context(), qID)
			if err != nil || (current != nil) != (mode != "reject") {
				t.Fatal("diagnostics replaced actual first-check visibility", current, err)
			}
			// A committed generation trace is retained after a genuine checker discard.
			if mode == "reject" {
				archived, err := store.ListDiscardResultsByQuestionIDs(t.Context(), []string{qID})
				if err != nil || len(archived) != 1 {
					t.Fatal(archived, err)
				}
			}
		})
	}
}
