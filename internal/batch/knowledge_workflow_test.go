package batch

import (
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type knowledgeBatchClient struct {
	mu       sync.Mutex
	calls    int
	badFirst bool
	prompts  []string
}

type targetBatchClient struct {
	inner   knowledgeBatchClient
	mu      sync.Mutex
	targets map[string]string
}

func (c *targetBatchClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	part := strings.SplitN(strings.SplitN(m[1].Content, "<knowledge_material>", 2)[1], "</knowledge_material>", 2)[0]
	var data struct {
		Plans []domain.WorkflowTarget `json:"ordered_targets"`
	}
	if err := json.Unmarshal([]byte(part), &data); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.targets[llm.CallOperation(ctx)] = data.Plans[0].Target
	c.mu.Unlock()
	return c.inner.Complete(ctx, m, o)
}

func TestRandomPlanFreezesPerPointTargetOffsetsAcrossExecutionAndRecovery(t *testing.T) {
	for _, mode := range []string{"submit", "restart", "retry", "restart_empty", "restart_corrupt", "retry_empty", "retry_corrupt"} {
		t.Run(mode, func(t *testing.T) {
			points := []domain.KnowledgePoint{{ID: "a", VersionID: "v", Topic: "A", OutlineCode: "a", Subject: "消化"}, {ID: "b", VersionID: "v", Topic: "B", OutlineCode: "b", Subject: "消化"}}
			before := randomIndex
			seq := []int{0, 1, 0}
			draw := 0
			randomIndex = func(int) int { v := seq[draw]; draw++; return v }
			plan, err := DrawRandomPlan(points, "v", 3)
			randomIndex = before
			if err != nil {
				t.Fatal(err)
			}
			snapshot := &domain.KnowledgeWorkflowSnapshot{Version: knowledgebase.LegacyWorkflowVersion, CapturedAt: time.Now()}
			for _, p := range points {
				targets := []string{"目标B"}
				if p.ID == "a" {
					targets = []string{"目标A一", "目标A二"}
				}
				snapshot.Bundles = append(snapshot.Bundles, domain.KnowledgeBundle{KnowledgePointID: p.ID, Cards: []domain.KnowledgeCard{{ID: "card-" + p.ID, Revision: 1, Kind: "medical", Status: "active", Source: "测试教材", Content: domain.KnowledgeContent{ExamTargets: targets, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "测试事实"}}}}}})
			}
			bs := &localBatchStore{}
			client := &targetBatchClient{targets: map[string]string{}}
			id := "offset-" + mode
			if mode != "submit" {
				pointsJSON, _ := json.Marshal(points)
				planJSON, _ := json.Marshal(plan)
				scopeJSON, _ := json.Marshal(snapshot)
				output := `{"questions":[],"items":[]}`
				status := "in_progress"
				if strings.HasPrefix(mode, "retry") {
					status = "completed"
					output = `{"questions":[],"items":[{"status":"failed"},{"status":"failed"},{"status":"failed"}]}`
				}
				bs.job = storage.BatchJobRecord{ID: id, Backend: localSingleAPIBackend, Status: status, TotalCount: 3, PointsJSON: string(pointsJSON), PlanJSON: string(planJSON), WorkflowSnapshotJSON: string(scopeJSON), OutputJSON: output}
				if strings.HasSuffix(mode, "_empty") {
					bs.job.PointsJSON = ""
				}
				if strings.HasSuffix(mode, "_corrupt") {
					bs.job.PointsJSON = "broken diagnostic snapshot"
				}
			}
			ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), bs, "test", LocalConfig{RecoveryInterval: 10 * time.Millisecond})
			defer ex.Close()
			if mode == "submit" {
				id, _, err = ex.SubmitPlan(knowledgebase.WithSnapshot(t.Context(), snapshot), PlanSubmission{Plan: plan})
				if err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(mode, "retry") {
				if _, err = ex.RetryFailed(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			job := waitForJobs(t, ex, []string{id}, 3*time.Second)[0]
			if job.Completed != 3 {
				t.Fatal(job)
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			for i, want := range []string{"目标A一", "目标B", "目标A二"} {
				if got := client.targets[fmt.Sprintf("batch:%s:%d", id, i)]; got != want {
					t.Fatalf("unit %d target=%q want=%q", i, got, want)
				}
			}
		})
	}
}

func (c *knowledgeBatchClient) Complete(_ context.Context, m []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	prompt := m[1].Content
	c.prompts = append(c.prompts, prompt)
	section := strings.SplitN(strings.SplitN(prompt, "<knowledge_material>", 2)[1], "</knowledge_material>", 2)[0]
	var prepared struct {
		Version   string                   `json:"workflow_version"`
		Plans     []domain.WorkflowTarget  `json:"ordered_targets"`
		Materials []knowledgebase.Material `json:"materials"`
	}
	if e := json.Unmarshal([]byte(section), &prepared); e != nil {
		return "", e
	}
	var q []map[string]any
	json.Unmarshal([]byte(robustQuestionJSON), &q)
	ref := prepared.Materials[0].Content.Facts[0].ID
	if c.badFirst && c.calls == 1 {
		ref = "invented-fact"
	}
	basis := domain.KnowledgeBasis{Target: prepared.Plans[0].Target, FactRefs: []string{ref}, ConditionNotes: "测试前提", AnswerSupport: "测试依据"}
	if knowledgebase.UsesClinicalPlan(prepared.Version) {
		basis.TargetID = prepared.Plans[0].ID
		basis.CasePlan = testutil.ClinicalPlanFixture("A", ref, "突发胸痛2小时。")
	}
	q[0]["knowledge_basis"] = basis
	raw, e := json.Marshal(q)
	return string(raw), e
}
func TestBatchKnowledgeSnapshotsSurviveRestartAndFailedUnitRetry(t *testing.T) {
	for _, version := range []string{knowledgebase.LegacyWorkflowVersion, knowledgebase.InitialClinicalWorkflowVersion, knowledgebase.WorkflowVersion, knowledgebase.PresentationWorkflowVersion} {
		for _, mode := range []string{"submit", "restart", "retry", "auto-retry"} {
			t.Run(version+"/"+mode, func(t *testing.T) {
				p := domain.KnowledgePoint{ID: "kp", VersionID: "kv", Topic: "测试考点", OutlineCode: "kb.001", Subject: "消化"}
				card := domain.KnowledgeCard{ID: "card", Revision: 2, Kind: "medical", Status: "active", Title: "冻结卡", Source: "测试教材", Content: domain.KnowledgeContent{ExamTargets: []string{"目标一", "目标二"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "必须保留的冻结事实"}}}}
				snapshot := &domain.KnowledgeWorkflowSnapshot{Version: version, CapturedAt: time.Now(), Bundles: []domain.KnowledgeBundle{{KnowledgePointID: p.ID, Cards: []domain.KnowledgeCard{card}}}}
				if version == knowledgebase.PresentationWorkflowVersion {
					snapshot.Presentation = &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
				}
				bs := &localBatchStore{}
				qs := testutil.NewMemoryStore()
				client := &knowledgeBatchClient{badFirst: mode == "auto-retry"}
				if mode == "restart" || mode == "retry" {
					raw, _ := json.Marshal(snapshot)
					points, _ := json.Marshal([]domain.KnowledgePoint{p})
					status, output := "in_progress", `{"questions":[],"items":[]}`
					if mode == "retry" {
						status = "completed"
						output = `{"questions":[],"items":[{"status":"failed","error":"provider timeout"}]}`
					}
					bs.job = storage.BatchJobRecord{ID: "frozen-job", OwnerID: "owner", Backend: localSingleAPIBackend, Status: status, TotalCount: 1, PointsJSON: string(points), OutputJSON: output, WorkflowSnapshotJSON: string(raw)}
				}
				ex := NewLocalExecutorWithConfig(generator.NewService(client), qs, bs, "test", LocalConfig{RecoveryInterval: 10 * time.Millisecond})
				defer ex.Close()
				ex.RetryDelays = []time.Duration{10 * time.Millisecond}
				id := "frozen-job"
				if mode == "submit" || mode == "auto-retry" {
					var e error
					id, _, e = ex.GenerateAndSubmit(knowledgebase.WithSnapshot(context.Background(), snapshot), []domain.KnowledgePoint{p}, 1, "冻结测试")
					if e != nil {
						t.Fatal(e)
					}
				}
				if mode == "retry" {
					if _, e := ex.RetryFailed(context.Background(), id); e != nil {
						t.Fatal(e)
					}
				}
				jobs := waitForJobs(t, ex, []string{id}, 3*time.Second)
				if jobs[0].Completed != 1 || jobs[0].Failed != 0 {
					t.Fatal(jobs[0])
				}
				record, _ := bs.GetBatchJob(context.Background(), id)
				if record.WorkflowSnapshotJSON == "" {
					t.Fatal("durable snapshot missing")
				}
				client.mu.Lock()
				defer client.mu.Unlock()
				want := 1
				if mode == "auto-retry" {
					want = 2
				}
				if client.calls != want {
					t.Fatal(client.calls, want)
				}
				for _, prompt := range client.prompts {
					if !strings.Contains(prompt, "必须保留的冻结事实") {
						t.Fatal("recovery lost knowledge snapshot")
					}
				}
			})
		}
	}
}
