package batch

import (
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type scanReactivatedStore struct {
	localBatchStore
	afterReactivate func()
}

func (s *scanReactivatedStore) ReactivateBatchJob(ctx context.Context, record storage.BatchJobRecord) error {
	if err := s.localBatchStore.UpdateBatchJob(ctx, record); err != nil {
		return err
	}
	s.afterReactivate()
	return nil
}

func TestRetryCheckpointIsRecoverableBeforeQueueStarts(t *testing.T) {
	st := &scanReactivatedStore{}
	points := []domain.KnowledgePoint{{ID: "kept", Topic: "保留成功题", Subject: "内科", OutlineCode: "kept"}, {ID: "retry", Topic: "只补失败项", Subject: "内科", OutlineCode: "retry"}}
	var questions []domain.A2Question
	if err := json.Unmarshal([]byte(robustQuestionJSON), &questions); err != nil {
		t.Fatal(err)
	}
	questions[0].ID = "already-generated"
	out := localBatchOutput{Questions: questions, Items: []ImportItem{{Status: "ok", Count: 1, QuestionID: questions[0].ID, OutlineCode: "kept"}, {Status: "failed", Error: "provider timeout", OutlineCode: "retry", Attempts: 3}}}
	pointJSON, _ := json.Marshal(points)
	outputJSON, _ := json.Marshal(out)
	if err := st.SaveBatchJob(t.Context(), storage.BatchJobRecord{ID: "retry-before-start", Backend: localSingleAPIBackend, Status: "completed", TotalCount: 2, Completed: 1, Failed: 1, PointsJSON: string(pointJSON), OutputJSON: string(outputJSON)}); err != nil {
		t.Fatal(err)
	}
	client := &robustLLM{}
	ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), st, "test", LocalConfig{RecoveryInterval: time.Hour})
	defer ex.Close()
	// Force a recovery scan after the durable retry commit and before startQueue.
	st.afterReactivate = ex.resumePending
	if _, err := ex.RetryFailed(t.Context(), "retry-before-start"); err != nil {
		t.Fatal(err)
	}
	job := waitForJobs(t, ex, []string{"retry-before-start"}, time.Second)[0]
	if job.Completed != 2 || job.Failed != 0 || client.calls() != 1 {
		t.Fatalf("retry lost or old success regenerated: %+v calls=%d", job, client.calls())
	}
	record, _ := st.GetBatchJob(t.Context(), job.JobID)
	var saved localBatchOutput
	if err := json.Unmarshal([]byte(record.OutputJSON), &saved); err != nil || len(saved.Questions) != 2 || saved.Questions[0].ID != "already-generated" {
		t.Fatal("old result not preserved", saved, err)
	}
}

func TestPolicyStoppedUnitsCannotBeManuallyReactivated(t *testing.T) {
	st := &localBatchStore{}
	point := domain.KnowledgePoint{ID: "policy-stop", Category: "临床综合", Subject: "药理", Topic: "旧矛盾考点"}
	pointsJSON, _ := json.Marshal([]domain.KnowledgePoint{point})
	outputJSON, _ := json.Marshal(localBatchOutput{Items: []ImportItem{{Status: "failed", Error: "选题范围调整", Attempts: 1}}})
	if err := st.SaveBatchJob(t.Context(), storage.BatchJobRecord{ID: "policy-only", Backend: localSingleAPIBackend, Status: "completed", TotalCount: 1, Failed: 1, PointsJSON: string(pointsJSON), OutputJSON: string(outputJSON)}); err != nil {
		t.Fatal(err)
	}
	ex := NewLocalExecutorWithConfig(generator.NewService(&robustLLM{}), testutil.NewMemoryStore(), st, "test", LocalConfig{RecoveryInterval: time.Hour})
	t.Cleanup(ex.Close)
	if _, err := ex.RetryFailed(t.Context(), "policy-only"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("policy-only job must remain terminal: %v", err)
	}
	got, err := st.GetBatchJob(t.Context(), "policy-only")
	if err != nil || got.Status != "completed" || got.Failed != 1 || got.OutputJSON != string(outputJSON) {
		t.Fatalf("policy job mutated: %+v err=%v", got, err)
	}
}

func TestRetryMixedBatchKeepsSuccessfulAndPolicyStoppedUnits(t *testing.T) {
	st := &scanReactivatedStore{}
	points := []domain.KnowledgePoint{{ID: "kept", OutlineCode: "kept", Topic: "已通过", Subject: "内科"}, {ID: "policy", OutlineCode: "policy", Topic: "旧基础", Category: "基础医学", Subject: "生理"}, {ID: "retry", OutlineCode: "retry", Topic: "瞬时失败", Subject: "内科"}, {ID: "pending-policy", OutlineCode: "pending-policy", Topic: "旧未执行基础项", Category: "基础医学", Subject: "生理"}}
	var questions []domain.A2Question
	if err := json.Unmarshal([]byte(robustQuestionJSON), &questions); err != nil {
		t.Fatal(err)
	}
	questions[0].ID = "kept-question"
	output := localBatchOutput{Questions: questions, Items: []ImportItem{{Status: "ok", QuestionID: "kept-question", Count: 1}, {Status: "failed", Error: "原政策停止证据", Attempts: 3}, {Status: "failed", Error: "provider timeout", Attempts: 3}, {Status: "pending"}}}
	pointsJSON, _ := json.Marshal(points)
	outputJSON, _ := json.Marshal(output)
	if err := st.SaveBatchJob(t.Context(), storage.BatchJobRecord{ID: "mixed-policy", Backend: localSingleAPIBackend, Status: "failed", TotalCount: 4, Completed: 1, Failed: 2, PointsJSON: string(pointsJSON), OutputJSON: string(outputJSON)}); err != nil {
		t.Fatal(err)
	}
	client := &robustLLM{}
	ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), st, "test", LocalConfig{RecoveryInterval: time.Hour})
	t.Cleanup(ex.Close)
	st.afterReactivate = func() {
		record, err := st.GetBatchJob(t.Context(), "mixed-policy")
		if err != nil || record.Failed != 2 {
			t.Fatalf("policy failure must not reserve a new unit: %+v %v", record, err)
		}
		ex.resumePending()
	}
	if _, err := ex.RetryFailed(t.Context(), "mixed-policy"); err != nil {
		t.Fatal(err)
	}
	job := waitForJobs(t, ex, []string{"mixed-policy"}, time.Second)[0]
	if job.Completed != 2 || job.Failed != 2 || job.Items[3].Status != "failed" || client.calls() != 1 || job.Items[1].Error != "原政策停止证据" || job.Items[1].Attempts != 3 || job.Items[0].QuestionID != "kept-question" {
		t.Fatalf("retry changed successful/policy unit: %+v calls=%d", job, client.calls())
	}
}

type transientCheckpointStore struct {
	localBatchStore
	unavailable atomic.Bool
}

func (s *transientCheckpointStore) UpdateBatchJob(ctx context.Context, j storage.BatchJobRecord) error {
	if s.unavailable.Load() && j.Completed > 0 {
		return errors.New("injected temporary write outage")
	}
	return s.localBatchStore.UpdateBatchJob(ctx, j)
}

func TestBatchRecoversCheckpointFailureWithoutRestart(t *testing.T) {
	st := &transientCheckpointStore{}
	st.unavailable.Store(true)
	client := &robustLLM{}
	ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), st, "test", LocalConfig{RecoveryInterval: 20 * time.Millisecond})
	defer ex.Close()
	id, _, err := ex.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "recover", Topic: "恢复", Subject: "内科", OutlineCode: "recover"}}, 1, "auto-recover")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		j, e := ex.GetJobStatus(context.Background(), id)
		if e == nil && j.Recovery != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery state never exposed")
		}
		time.Sleep(time.Millisecond)
	}
	st.unavailable.Store(false)
	job := waitForJobs(t, ex, []string{id}, 3*time.Second)[0]
	if job.Completed != 1 || job.Failed != 0 || job.Recovery != nil || job.Items[0].Attempts != 1 || client.calls() != 1 {
		t.Fatalf("did not recover: %+v", job)
	}
}

// A recovery scan can read the old in-progress record just before the queue
// preserves a successful reply in memory and releases ownership. Its delayed
// start must not overwrite that checkpoint with another paid model attempt.
func TestBatchPendingCheckpointPreventsStaleResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &localBatchStore{}
	client := &robustLLM{}
	point := domain.KnowledgePoint{ID: "checkpoint-resume", Topic: "恢复", Subject: "内科", OutlineCode: "checkpoint-resume"}
	old := localBatchOutput{Items: []ImportItem{{OutlineCode: point.OutlineCode, Status: "running", Attempts: 1, MaxAttempts: 3}}}
	raw, _ := json.Marshal(old)
	if err := st.SaveBatchJob(ctx, storage.BatchJobRecord{ID: "stale-resume", Status: "in_progress", OutputJSON: string(raw)}); err != nil {
		t.Fatal(err)
	}
	ex := &LocalExecutor{generator: generator.NewService(client), questionStore: testutil.NewMemoryStore(), batchJobStore: st, ctx: ctx, cancel: cancel, limiter: storage.NewConcurrencyLimiter(1), MaxAttempts: 3, active: map[string]bool{}, recovery: map[string]RecoveryProgress{}, checkpoints: map[string]batchCheckpoint{}}
	defer ex.Close()
	success := localBatchOutput{Items: []ImportItem{{OutlineCode: point.OutlineCode, Status: "ok", Attempts: 1, Count: 1, QuestionID: "saved-reply"}}}
	ex.rememberCheckpoint("stale-resume", "completed", success)
	ex.startQueue("stale-resume", []domain.KnowledgePoint{point}, []int{0}, old, nil, nil)
	ex.wg.Wait()
	if client.calls() != 0 {
		t.Fatalf("stale recovery repeated a successful model call: %d", client.calls())
	}
	ex.restoreCheckpoints()
	record, err := st.GetBatchJob(ctx, "stale-resume")
	if err != nil || record.Status != "completed" || record.Completed != 1 {
		t.Fatalf("checkpoint not committed: %+v %v", record, err)
	}
}

func TestBatchRestartRetainsFinalAttemptAndNeverCallsFourth(t *testing.T) {
	st := &localBatchStore{}
	points := []domain.KnowledgePoint{{ID: "final", Topic: "最终尝试", Subject: "内科", OutlineCode: "final"}}
	raw, _ := json.Marshal(points)
	out, _ := json.Marshal(localBatchOutput{Items: []ImportItem{{OutlineCode: "final", Topic: "最终尝试", Status: "running", Attempts: 3, MaxAttempts: 3}}})
	_ = st.SaveBatchJob(context.Background(), storage.BatchJobRecord{ID: "final-crash", Backend: localSingleAPIBackend, Status: "in_progress", TotalCount: 1, PointsJSON: string(raw), OutputJSON: string(out)})
	client := &robustLLM{}
	ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), st, "test", LocalConfig{RecoveryInterval: time.Millisecond})
	defer ex.Close()
	job := waitForJobs(t, ex, []string{"final-crash"}, time.Second)[0]
	if job.Failed != 1 || client.calls() != 0 || job.Items[0].Attempts != 3 {
		t.Fatalf("extra call after final crash: %+v calls=%d", job, client.calls())
	}
}

func TestBatchCorruptSnapshotClosesInsteadOfHanging(t *testing.T) {
	st := &localBatchStore{}
	_ = st.SaveBatchJob(context.Background(), storage.BatchJobRecord{ID: "corrupt", Backend: localSingleAPIBackend, Status: "in_progress", TotalCount: 1, PointsJSON: `[{"id":"p","topic":"正常","subject":"内科"}]`, OutputJSON: `{broken`})
	client := &robustLLM{}
	ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), st, "test", LocalConfig{RecoveryInterval: time.Millisecond})
	defer ex.Close()
	job := waitForJobs(t, ex, []string{"corrupt"}, time.Second)[0]
	if job.Status != "failed" || job.Recovery == nil || job.Recovery.State != "failed" || client.calls() != 0 {
		t.Fatalf("corrupt snapshot was regenerated or left hanging: %+v", job)
	}
}

type transientStateStore struct {
	localBatchStore
	unavailable atomic.Bool
	mode        string
}

func (s *transientStateStore) UpdateBatchJob(ctx context.Context, j storage.BatchJobRecord) error {
	var out localBatchOutput
	_ = json.Unmarshal([]byte(j.OutputJSON), &out)
	starting := false
	for _, item := range out.Items {
		starting = starting || item.Status == "running"
	}
	if s.unavailable.Load() && ((s.mode == "starting" && starting) || (s.mode == "terminal" && j.Status == "completed")) {
		return errors.New("injected state checkpoint outage")
	}
	return s.localBatchStore.UpdateBatchJob(ctx, j)
}
func TestBatchCheckpointRecoveryDoesNotBurnModelAttempts(t *testing.T) {
	for _, mode := range []string{"starting", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			st := &transientStateStore{mode: mode}
			st.unavailable.Store(true)
			client := &robustLLM{}
			ex := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), st, "test", LocalConfig{RecoveryInterval: 10 * time.Millisecond})
			defer ex.Close()
			id, _, err := ex.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "checkpoint", Topic: "恢复", Subject: "内科", OutlineCode: "checkpoint"}}, 1, "state-recovery")
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				j, e := ex.GetJobStatus(context.Background(), id)
				if e == nil && j.Recovery != nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no recovery state")
				}
				time.Sleep(time.Millisecond)
			}
			if mode == "starting" && client.calls() != 0 {
				t.Fatal("called model before attempt checkpoint")
			}
			st.unavailable.Store(false)
			j := waitForJobs(t, ex, []string{id}, 3*time.Second)[0]
			if j.Completed != 1 || j.Items[0].Attempts != 1 || client.calls() != 1 {
				t.Fatalf("recovery repeated model call: %+v calls=%d", j, client.calls())
			}
		})
	}
}

func TestBatchRetryStartsNewTimingPhaseWithoutChangingSubmissionTime(t *testing.T) {
	st := &localBatchStore{}
	created := time.Now().Add(-2 * time.Hour)
	points := []domain.KnowledgePoint{{ID: "timed", Topic: "正常", Subject: "内科", OutlineCode: "timed"}}
	raw, _ := json.Marshal(points)
	out, _ := json.Marshal(localBatchOutput{Items: []ImportItem{{OutlineCode: "timed", Topic: "正常", Status: "failed", Attempts: 3, MaxAttempts: 3}}})
	_ = st.SaveBatchJob(context.Background(), storage.BatchJobRecord{ID: "timing", Backend: localSingleAPIBackend, Status: "completed", TotalCount: 1, Failed: 1, PointsJSON: string(raw), OutputJSON: string(out), CreatedAt: created.Format(time.RFC3339Nano), FinishedAt: created.Add(time.Second).Format(time.RFC3339Nano)})
	client := &robustLLM{}
	ex := NewLocalExecutor(generator.NewService(client), testutil.NewMemoryStore(), st, "test")
	defer ex.Close()
	start := time.Now().UnixMilli()
	_, err := ex.RetryFailed(context.Background(), "timing")
	if err != nil {
		t.Fatal(err)
	}
	j := waitForJobs(t, ex, []string{"timing"}, time.Second)[0]
	if j.GenerationStartedAt < start || j.CreatedAt != created.Unix() || j.Items[0].Attempts != 1 || client.calls() != 1 {
		t.Fatalf("retry changed submission or reused old elapsed time: %+v", j)
	}
}
