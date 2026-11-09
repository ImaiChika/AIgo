package batch

import (
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage/testutil"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestBatchRetryYieldsPermitToAnotherJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &robustLLM{failTopics: map[string]bool{"retry-held": true}}
		ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), &localBatchStore{}, "test", LocalConfig{Concurrency: 1})
		defer ex.Close()
		ex.MaxAttempts = 2
		ex.RetryDelays = []time.Duration{700 * time.Millisecond}
		bad, _, err := ex.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "retry-held", Topic: "retry-held", Subject: "内科", OutlineCode: "retry.held"}}, 1, "retrying")
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for client.calls() < 1 {
			if time.Now().After(deadline) {
				t.Fatal("first attempt not called")
			}
			time.Sleep(time.Millisecond)
		}
		healthy, _, err := ex.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "healthy", Topic: "正常", Subject: "内科", OutlineCode: "healthy"}}, 1, "healthy")
		if err != nil {
			t.Fatal(err)
		}
		if job := waitForJobs(t, ex, []string{healthy}, 500*time.Millisecond)[0]; job.Completed != 1 {
			t.Fatal(job)
		}
		waiting, err := ex.GetJobStatus(context.Background(), bad)
		if err != nil || waiting.Status != "in_progress" || waiting.Items[0].Attempts != 1 || waiting.Items[0].NextAttemptAt == 0 {
			t.Fatalf("retry did not yield: %+v %v", waiting, err)
		}
		if job := waitForJobs(t, ex, []string{bad}, 2*time.Second)[0]; job.Failed != 1 || job.Items[0].Attempts != 2 {
			t.Fatalf("retry cap lost: %+v", job)
		}
	})
}

type emptyBatchReplyClient struct{ calls atomic.Int32 }

func (c *emptyBatchReplyClient) Complete(context.Context, []llm.Message, llm.GenerateOptions) (string, error) {
	if c.calls.Add(1) < 3 {
		return "[]", nil
	}
	return robustQuestionJSON, nil
}
func TestBatchRetriesEmptyModelReply(t *testing.T) {
	client := &emptyBatchReplyClient{}
	ex := NewLocalExecutor(generator.NewService(client), testutil.NewMemoryStore(), &localBatchStore{}, "test")
	defer ex.Close()
	ex.RetryDelays = []time.Duration{0, 0}
	id, _, err := ex.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "empty", Topic: "正常", Subject: "内科", OutlineCode: "empty"}}, 1, "empty-retry")
	if err != nil {
		t.Fatal(err)
	}
	j := waitForJobs(t, ex, []string{id}, time.Second)[0]
	if j.Completed != 1 || j.Items[0].Attempts != 3 || client.calls.Load() != 3 {
		t.Fatalf("empty model reply reported success or did not retry: %+v calls=%d", j, client.calls.Load())
	}
}

// 基础医学选题范围调整在批量单元中也是终态：不重试、不调用模型。
func TestBatchUnitBasicScopeFailsWithoutRetry(t *testing.T) {
	gen := generator.NewService(localTestLLM{})
	ex := NewLocalExecutorWithConfig(gen, testutil.NewMemoryStore(), &localBatchStore{}, "test", LocalConfig{Concurrency: 1})
	t.Cleanup(ex.Close)
	ctx := context.Background()
	item := ex.generateUnit(ctx, domain.KnowledgePoint{ID: "20.1", Topic: "静息电位", Subject: "生理", OutlineCode: "20.1", Category: "基础医学"})
	if item.Status != "failed" {
		t.Fatalf("状态应为 failed：%s", item.Status)
	}
	if !strings.Contains(item.Error, "选题范围调整") {
		t.Fatalf("失败原因应说明选题范围调整：%q", item.Error)
	}
	if !item.nonRetryable {
		t.Fatal("基础医学范围调整必须标记为不可重试")
	}
}
