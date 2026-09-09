package batch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aigo/internal/domain"
)

type fakeExecutor struct {
	info      Capabilities
	submitID  string
	jobs      []BatchJob
	statusJob BatchJob
	statusIDs []string
	importIDs []string
}

func (f *fakeExecutor) Capabilities() Capabilities { return f.info }
func (f *fakeExecutor) GenerateAndSubmit(context.Context, []domain.KnowledgePoint, int, string) (string, int, error) {
	return f.submitID, 1, nil
}
func (f *fakeExecutor) GetJobStatus(_ context.Context, id string) (*BatchJob, error) {
	f.statusIDs = append(f.statusIDs, id)
	job := f.statusJob
	job.JobID = id
	return &job, nil
}
func (f *fakeExecutor) ListJobs(context.Context, string, string, int) ([]BatchJob, error) {
	return append([]BatchJob(nil), f.jobs...), nil
}
func (f *fakeExecutor) ImportResults(_ context.Context, id string, _ []domain.KnowledgePoint) (*ImportResult, error) {
	f.importIDs = append(f.importIDs, id)
	return &ImportResult{Saved: 1}, nil
}

func TestNewExecutorDoesNotTreatLocalRealtimeAsDashScopeBatch(t *testing.T) {
	executor, err := NewExecutor("local", DashScopeConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	info := executor.Capabilities()
	if info.Available || info.Backend != "local" {
		t.Fatalf("local placeholder capabilities = %#v", info)
	}
	if !strings.Contains(info.Message, "尚未实现") {
		t.Errorf("local placeholder message = %q", info.Message)
	}
}

func TestNewExecutorAutoPreservesDashScopeWhenKeyExists(t *testing.T) {
	executor, err := NewExecutor("auto", DashScopeConfig{
		APIKey:         "test-key",
		BaseURL:        "https://dashscope.example/v1",
		Model:          "qwen3.5-flash-versioned",
		EnableThinking: true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	info := executor.Capabilities()
	if !info.Available || info.Backend != "dashscope" {
		t.Fatalf("DashScope capabilities = %#v", info)
	}
	if info.Model != "qwen3.5-flash-versioned" {
		t.Errorf("model = %q", info.Model)
	}
}

func TestRoutingExecutorFormatsNewRefsAndKeepsLegacyDashScopeRouting(t *testing.T) {
	if got := FormatJobRef("dashscope", "batch_current"); got != "batch_current" {
		t.Fatalf("DashScope compatibility ref = %q", got)
	}
	local := &fakeExecutor{
		info:      Capabilities{Backend: "local", Available: true},
		submitID:  "job-new",
		statusJob: BatchJob{Status: "completed"},
	}
	dash := &fakeExecutor{
		info:      Capabilities{Backend: "dashscope", Available: true},
		statusJob: BatchJob{Status: "completed"},
	}
	router := newRoutingExecutor("local", local, "dashscope", map[string]Executor{
		"local":     local,
		"dashscope": dash,
	})

	jobRef, _, err := router.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "kp"}}, 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	if jobRef != "local:job-new" {
		t.Fatalf("new job ref = %q", jobRef)
	}

	legacy, err := router.GetJobStatus(context.Background(), "batch_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.JobID != "batch_legacy" || legacy.Backend != "dashscope" {
		t.Errorf("legacy job = %#v", legacy)
	}
	if len(dash.statusIDs) != 1 || dash.statusIDs[0] != "batch_legacy" {
		t.Errorf("legacy status routed with IDs %#v", dash.statusIDs)
	}

	if _, err := router.ImportResults(context.Background(), "dashscope:batch_new", nil); err != nil {
		t.Fatal(err)
	}
	if len(dash.importIDs) != 1 || dash.importIDs[0] != "batch_new" {
		t.Errorf("import routed with IDs %#v", dash.importIDs)
	}
}

func TestRoutingExecutorCanReadOldCloudJobWhenDefaultIsUnavailableLocal(t *testing.T) {
	dash := &fakeExecutor{statusJob: BatchJob{Status: "completed"}}
	router := newRoutingExecutor(
		"local",
		NewUnavailableExecutor("local", "not ready"),
		"dashscope",
		map[string]Executor{"dashscope": dash},
	)
	job, err := router.GetJobStatus(context.Background(), "batch_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobID != "batch_legacy" {
		t.Errorf("job ID = %q", job.JobID)
	}
	_, err = router.GetJobStatus(context.Background(), "unknown:job-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown backend error = %v", err)
	}
}

func TestRoutingExecutorListJobsPrefixesDeduplicatesAndLimits(t *testing.T) {
	dash := &fakeExecutor{jobs: []BatchJob{
		{JobID: "same", CreatedAt: 10},
		{JobID: "same", CreatedAt: 20},
		{JobID: "older", CreatedAt: 5},
		{JobID: "wrong-provider", Backend: "local", CreatedAt: 40},
	}}
	local := &fakeExecutor{jobs: []BatchJob{{JobID: "local-1", CreatedAt: 30}}}
	router := newRoutingExecutor("local", local, "dashscope", map[string]Executor{
		"dashscope": dash,
		"local":     local,
	})
	jobs, err := router.ListJobs(context.Background(), "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs = %#v", jobs)
	}
	if jobs[0].JobID != "local:local-1" || jobs[1].JobID != "same" {
		t.Errorf("sorted/prefixed jobs = %#v", jobs)
	}
}

func TestGenerateJSONLUsesConfiguredModelAndSharedPrompt(t *testing.T) {
	service := NewDashScopeService(DashScopeConfig{
		APIKey:         "test-key",
		BaseURL:        "https://dashscope.example/v1",
		Model:          "qwen3.5-flash-versioned",
		EnableThinking: false,
	}, nil, nil)

	path := filepath.Join(t.TempDir(), "requests.jsonl")
	point := domain.KnowledgePoint{
		OutlineCode: "A1.2",
		Category:    "临床综合",
		Subject:     "呼吸系统",
		Unit:        "肺部感染",
		SubItem:     "诊断",
		Topic:       "社区获得性肺炎",
	}
	count, err := service.generateJSONL([]domain.KnowledgePoint{point}, 3, path)
	if err != nil {
		t.Fatalf("generateJSONL() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("request count = %d, want 1", count)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("missing JSONL line: %v", scanner.Err())
	}
	var request struct {
		CustomID string `json:"custom_id"`
		Body     struct {
			Model          string `json:"model"`
			EnableThinking bool   `json:"enable_thinking"`
			MaxTokens      *int   `json:"max_tokens"`
			Messages       []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		} `json:"body"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
		t.Fatalf("decode JSONL: %v", err)
	}
	if request.CustomID != point.OutlineCode {
		t.Errorf("custom_id = %q", request.CustomID)
	}
	if request.Body.Model != "qwen3.5-flash-versioned" {
		t.Errorf("model = %q", request.Body.Model)
	}
	if request.Body.EnableThinking {
		t.Error("enable_thinking should use configured false value")
	}
	if request.Body.MaxTokens != nil {
		t.Errorf("batch request must omit max_tokens, got %d", *request.Body.MaxTokens)
	}
	if len(request.Body.Messages) != 2 {
		t.Fatalf("messages = %#v", request.Body.Messages)
	}
	userPrompt := request.Body.Messages[1].Content
	for _, want := range []string{"大纲代码：A1.2", "单元：肺部感染", "细目：诊断", "要点：社区获得性肺炎", "数量：3道"} {
		if !strings.Contains(userPrompt, want) {
			t.Errorf("shared prompt missing %q", want)
		}
	}
}
