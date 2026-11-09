package batch

import (
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type promptCaptureClient struct {
	mu       sync.Mutex
	messages [][]llm.Message
	fail     bool
}

func (c *promptCaptureClient) Complete(_ context.Context, m []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, append([]llm.Message(nil), m...))
	if c.fail {
		return "", context.DeadlineExceeded
	}
	return robustQuestionJSON, nil
}
func (c *promptCaptureClient) assertSnapshot(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.messages) == 0 {
		t.Fatal("no calls")
	}
	for _, m := range c.messages {
		if !strings.Contains(m[1].Content, "提交时个人约束") || m[0].Content != "提交时底座系统" || !strings.HasPrefix(m[1].Content, "提交时底座用户") {
			t.Fatalf("lost snapshot: %+v", m)
		}
	}
}
func TestLocalBatchPromptSnapshotSubmitRestartRetry(t *testing.T) {
	point := domain.KnowledgePoint{ID: "snapshot-point", OutlineCode: "110.1", Subject: "循环", Topic: "快照考点"}
	req := domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{point}, Count: 1, Difficulty: "0.65"}
	snapshot := &domain.GenerationPromptSnapshot{OwnerID: "owner", Appendix: "\n提交时个人约束"}
	generator.FreezePromptBaseline(snapshot, []domain.GenerationRequest{req})
	snapshot.SystemPrompt = "提交时底座系统"
	for key := range snapshot.UserPrompts {
		snapshot.UserPrompts[key] = "提交时底座用户"
	}
	req.PromptSnapshot = snapshot
	if _, e := generator.NewService(&promptCaptureClient{}).Generate(context.Background(), req); e != nil {
		t.Fatalf("direct snapshot generate: %v", e)
	}
	for _, mode := range []string{"submit", "restart", "retry"} {
		t.Run(mode, func(t *testing.T) {
			client := &promptCaptureClient{}
			bs := &localBatchStore{}
			qs := testutil.NewMemoryStore()
			raw, _ := json.Marshal(snapshot)
			points, _ := json.Marshal([]domain.KnowledgePoint{point})
			if mode != "submit" {
				status, output := "in_progress", `{"questions":[],"items":[]}`
				if mode == "retry" {
					status = "completed"
					output = `{"questions":[],"items":[{"status":"failed","error":"timeout"}]}`
				}
				bs.job = storage.BatchJobRecord{ID: "snapshot-job", OwnerID: "owner", Backend: localSingleAPIBackend, Status: status, TotalCount: 1, PointsJSON: string(points), OutputJSON: output, PromptSnapshotJSON: string(raw)}
			}
			executor := NewLocalExecutor(generator.NewService(client), qs, bs, "test")
			jobID := "snapshot-job"
			if mode == "submit" {
				var e error
				jobID, _, e = executor.GenerateAndSubmit(generator.WithPromptSnapshot(context.Background(), snapshot), []domain.KnowledgePoint{point}, 1, "规则测试")
				if e != nil {
					t.Fatal(e)
				}
			}
			if mode == "retry" {
				if _, e := executor.RetryFailed(context.Background(), jobID); e != nil {
					t.Fatal(e)
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				job, _ := bs.GetBatchJob(context.Background(), jobID)
				if job.Status == "completed" && job.Completed == 1 {
					if job.PromptSnapshotJSON == "" {
						t.Fatal("snapshot not saved")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("not completed %+v", job)
				}
				time.Sleep(5 * time.Millisecond)
			}
			client.assertSnapshot(t)
			job, _ := executor.GetJobStatus(context.Background(), jobID)
			public, _ := json.Marshal(job)
			if strings.Contains(string(public), "个人约束") || strings.Contains(string(public), "底座") {
				t.Fatal("snapshot content leaked through job status")
			}
		})
	}
}
