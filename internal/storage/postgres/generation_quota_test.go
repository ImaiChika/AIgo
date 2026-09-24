package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func quotaTestRun(id, owner string, count int) domain.GenerationRun {
	return domain.GenerationRun{ID: id, OwnerID: owner, Status: domain.GenerationRunPending, RequestedCount: count}
}

func quotaTestBatch(id, owner, status string, total, completed, failed int) storage.BatchJobRecord {
	return storage.BatchJobRecord{ID: id, OwnerID: owner, Backend: "local_single_api", Status: status,
		TotalCount: total, Completed: completed, Failed: failed, PointsJSON: "[]", OutputJSON: `{"questions":[],"items":[]}`}
}

func quotaMustReject(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, storage.ErrGenerationQuotaExceeded) {
		t.Fatalf("expected quota rejection, got %v", err)
	}
}

func TestGenerationQuotaAdmissionAndImmediateUpdate(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t) // 独立 PostgreSQL schema；不启动 worker、没有模型调用。
	quota, usage, err := store.GetGenerationQuota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if quota != storage.DefaultGenerationQuota() || usage.PendingQuestionUnits != 0 {
		t.Fatalf("unexpected defaults: %+v %+v", quota, usage)
	}

	quota.SingleMaxQuestions = 2
	quota.BatchMaxQuestions = 20
	quota.SingleActivePerUser = 2
	quota.BatchActivePerUser = 1
	quota.GlobalPendingQuestions = 20
	if err := store.SaveGenerationQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGenerationRun(ctx, quotaTestRun("too-many-single", "teacher-1", 3)); err == nil {
		t.Fatal("single count should reject")
	} else {
		quotaMustReject(t, err)
	}
	if err := store.SaveBatchJob(ctx, quotaTestBatch("too-many-batch", "teacher-1", "in_progress", 21, 0, 0)); err == nil {
		t.Fatal("batch total should reject")
	} else {
		quotaMustReject(t, err)
	}

	for i := 1; i <= 2; i++ {
		created, err := store.CreateGenerationRun(ctx, quotaTestRun(fmt.Sprintf("single-%d", i), "teacher-1", 1))
		if err != nil || !created {
			t.Fatalf("single %d: %v", i, err)
		}
	}
	// 幂等重放不能因为当前已达上限而变成错误。
	if created, err := store.CreateGenerationRun(ctx, quotaTestRun("single-1", "teacher-1", 1)); err != nil || created {
		t.Fatalf("idempotent replay: %v %v", created, err)
	}
	if _, err := store.CreateGenerationRun(ctx, quotaTestRun("single-3", "teacher-1", 1)); err == nil {
		t.Fatal("per-user single active should reject")
	} else {
		quotaMustReject(t, err)
	}
	if err := store.SaveBatchJob(ctx, quotaTestBatch("batch-1", "teacher-1", "in_progress", 17, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBatchJob(ctx, quotaTestBatch("batch-2", "teacher-1", "in_progress", 1, 0, 0)); err == nil {
		t.Fatal("per-user batch active should reject")
	} else {
		quotaMustReject(t, err)
	}
	if _, err := store.CreateGenerationRun(ctx, quotaTestRun("global-reject", "teacher-2", 2)); err == nil {
		t.Fatal("shared global backlog should reject")
	} else {
		quotaMustReject(t, err)
	}

	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveSingleRuns != 2 || usage.ActiveBatchJobs != 1 || usage.PendingQuestionUnits != 19 {
		t.Fatalf("wrong usage: %+v %v", usage, err)
	}
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("batch-1", "teacher-1", "in_progress", 17, 10, 0)); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 9 {
		t.Fatalf("completed items should free capacity: %+v %v", usage, err)
	}
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("batch-1", "teacher-1", "completed", 17, 17, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBatchJob(ctx, quotaTestBatch("batch-2", "teacher-1", "in_progress", 1, 0, 0)); err != nil {
		t.Fatalf("finished batch should free per-user slot: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE generation_runs SET status='running' WHERE id='single-1'`); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteGenerationRun(ctx, "single-1", []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGenerationRun(ctx, quotaTestRun("single-3", "teacher-1", 1)); err != nil {
		t.Fatalf("finished single should free per-user slot: %v", err)
	}
}

func TestGenerationQuotaConcurrentGlobalAdmission(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	quota := storage.DefaultGenerationQuota()
	quota.BatchMaxQuestions = 15
	quota.GlobalPendingQuestions = 20
	if err := store.SaveGenerationQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- store.SaveBatchJob(context.Background(), quotaTestBatch(fmt.Sprintf("parallel-%d", i), fmt.Sprintf("teacher-%d", i), "in_progress", 15, 0, 0))
		}(i)
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, storage.ErrGenerationQuotaExceeded) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("atomic admission: accepted=%d rejected=%d", accepted, rejected)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 15 {
		t.Fatalf("backlog exceeded cap: %+v %v", usage, err)
	}
}

func TestGenerationQuotaRetryAdmission(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	quota := storage.DefaultGenerationQuota()
	quota.BatchActivePerUser = 1
	quota.BatchMaxQuestions = 20
	quota.GlobalPendingQuestions = 20
	if err := store.SaveGenerationQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	// 旧任务已经结束，其中 3 项失败；重跑需重新占 3 个名额。
	if err := store.SaveBatchJob(ctx, quotaTestBatch("retry-old", "teacher", "completed", 3, 0, 3)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBatchJob(ctx, quotaTestBatch("active", "teacher", "in_progress", 1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	retry := quotaTestBatch("retry-old", "teacher", "in_progress", 3, 0, 0)
	quotaMustReject(t, store.ReactivateBatchJob(ctx, retry))
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("active", "teacher", "completed", 1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReactivateBatchJob(ctx, retry); err != nil {
		t.Fatalf("retry after slot is freed: %v", err)
	}
	if err := store.ReactivateBatchJob(ctx, retry); !errors.Is(err, storage.ErrBatchJobAlreadyActive) {
		t.Fatalf("concurrent duplicate retry should reject: %v", err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 3 || usage.ActiveBatchJobs != 1 {
		t.Fatalf("retry occupancy: %+v %v", usage, err)
	}
}

func TestGenerationQuotaIncludesAICheckBacklog(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	quota := storage.DefaultGenerationQuota()
	quota.BatchMaxQuestions = 20
	quota.GlobalPendingQuestions = 20
	if err := store.SaveGenerationQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBatchJob(ctx, quotaTestBatch("check-batch", "teacher-1", "in_progress", 19, 0, 0)); err != nil {
		t.Fatal(err)
	}
	q := aiCheckTestQuestion(t, store, ctx, "check-pending-question")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "check-pending-task", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveAICheckTasks != 1 || usage.PendingQuestionUnits != 20 {
		t.Fatalf("AI check must occupy global backlog: %+v %v", usage, err)
	}
	if _, err := store.CreateGenerationRun(ctx, quotaTestRun("after-check", "teacher-2", 1)); err == nil {
		t.Fatal("new generation should wait for AI check capacity")
	} else {
		quotaMustReject(t, err)
	}
}

func TestGenerationQuotaQuestionLifecycleDoesNotDoubleCount(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t) // 仅操作隔离数据库，不启动模型 worker。
	if created, err := store.CreateGenerationRun(ctx, quotaTestRun("lifecycle-single", "teacher", 2)); err != nil || !created {
		t.Fatalf("create single run: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE generation_runs SET status='running' WHERE id='lifecycle-single'`); err != nil {
		t.Fatal(err)
	}
	q := aiCheckTestQuestion(t, store, ctx, "lifecycle-question")
	if err := store.RecordGenerationRunQuestionIDs(ctx, "lifecycle-single", []string{q.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "lifecycle-check", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveSingleRuns != 1 || usage.ActiveAICheckTasks != 1 || usage.SinglePendingQuestions != 2 || usage.OverlapQuestions != 1 || usage.PendingQuestionUnits != 2 {
		t.Fatalf("single-to-check transition should count each question once: %+v %v", usage, err)
	}
	if err := store.CompleteGenerationRun(ctx, "lifecycle-single", []string{q.ID}); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveSingleRuns != 0 || usage.OverlapQuestions != 0 || usage.PendingQuestionUnits != 1 {
		t.Fatalf("only check remains: %+v %v", usage, err)
	}
	if err := store.CompleteCheckTask(ctx, "lifecycle-check"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.QuestionStatus{domain.StatusAIReviewed, domain.StatusReviewing, domain.StatusPublished, domain.StatusRejected, domain.StatusArchived} {
		if _, err := store.db.ExecContext(ctx, `UPDATE questions SET status=$1 WHERE id=$2`, status, q.ID); err != nil {
			t.Fatal(err)
		}
		_, usage, err = store.GetGenerationQuota(ctx)
		if err != nil || usage.PendingQuestionUnits != 0 || usage.ActiveAICheckTasks != 0 {
			t.Fatalf("downstream status %s must not occupy generation quota: %+v %v", status, usage, err)
		}
	}
	// AI 质量淘汰物理删除题目时，外键级联移除首检任务，额度不残留。
	q2 := aiCheckTestQuestion(t, store, ctx, "discarded-question")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "discarded-check", QuestionID: q2.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM questions WHERE id=$1`, q2.ID); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 0 {
		t.Fatalf("deleted draft should free quota: %+v %v", usage, err)
	}
}

func TestGenerationQuotaBatchStageCounts(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	if err := store.SaveBatchJob(ctx, quotaTestBatch("stage-batch", "teacher", "in_progress", 3, 0, 0)); err != nil {
		t.Fatal(err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveBatchJobs != 1 || usage.BatchPendingQuestions != 3 || usage.PendingQuestionUnits != 3 {
		t.Fatalf("queued batch units: %+v %v", usage, err)
	}
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("stage-batch", "teacher", "in_progress", 3, 1, 1)); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.BatchPendingQuestions != 1 || usage.PendingQuestionUnits != 1 {
		t.Fatalf("completed and failed units must leave queue: %+v %v", usage, err)
	}
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("stage-batch", "teacher", "completed", 3, 2, 1)); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveBatchJobs != 0 || usage.PendingQuestionUnits != 0 {
		t.Fatalf("completed batch awaiting manual import is not running work: %+v %v", usage, err)
	}
	q := aiCheckTestQuestion(t, store, ctx, "batch-imported-question")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "batch-imported-check", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveAICheckTasks != 1 || usage.PendingQuestionUnits != 1 {
		t.Fatalf("imported question enters first check: %+v %v", usage, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE ai_check_tasks SET status='exhausted' WHERE id='batch-imported-check'`); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 0 {
		t.Fatalf("terminal check failure blocks review but no longer occupies running quota: %+v %v", usage, err)
	}
}

func TestQuotaObservesBriefBatchTerminalGap(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	if err := store.SaveBatchJob(ctx, quotaTestBatch("terminal-gap", "teacher", "in_progress", 1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("terminal-gap", "teacher", "in_progress", 1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveBatchJobs != 1 || usage.BatchPendingQuestions != 0 {
		t.Fatalf("last-item-to-terminal observation: %+v %v", usage, err)
	}
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("terminal-gap", "teacher", "completed", 1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.ActiveBatchJobs != 0 || usage.BatchPendingQuestions != 0 {
		t.Fatalf("terminal update should resolve observation: %+v %v", usage, err)
	}
}

func TestQuotaRecordIDsFailureFallbackIsConservative(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	if created, err := store.CreateGenerationRun(ctx, quotaTestRun("record-gap", "teacher", 2)); err != nil || !created {
		t.Fatalf("create run: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE generation_runs SET status='running' WHERE id='record-gap'`); err != nil {
		t.Fatal(err)
	}
	q := aiCheckTestQuestion(t, store, ctx, "record-gap-question")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "record-gap-check", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 3 || usage.OverlapQuestions != 0 {
		t.Fatalf("missing ID registration should conservatively double count: %+v %v", usage, err)
	}
	if err := store.RecordGenerationRunQuestionIDs(ctx, "record-gap", []string{q.ID}); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 2 || usage.OverlapQuestions != 1 {
		t.Fatalf("registration restores exact count: %+v %v", usage, err)
	}
}

func TestGenerationQuotaValidation(t *testing.T) {
	q := storage.DefaultGenerationQuota()
	q.BatchMaxQuestions = 501
	if q.Validate() == nil {
		t.Fatal("unsafe batch limit should reject")
	}
	q = storage.DefaultGenerationQuota()
	q.GlobalPendingQuestions = 50
	if q.Validate() == nil {
		t.Fatal("global cap below batch size should reject")
	}
}

func batchOutputIDs(ids []string) string {
	questions := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		questions = append(questions, map[string]string{"id": id})
	}
	payload, _ := json.Marshal(map[string]any{"questions": questions, "items": []any{}})
	return string(payload)
}

func TestCompletedBatchImportReservesFirstCheckQuota(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	quota := storage.DefaultGenerationQuota()
	quota.BatchMaxQuestions = 20
	quota.GlobalPendingQuestions = 20
	if err := store.SaveGenerationQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBatchJob(ctx, quotaTestBatch("full-running", "teacher-a", "in_progress", 20, 0, 0)); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = fmt.Sprintf("import-bypass-%d", i)
	}
	finished := quotaTestBatch("finished-waiting-import", "teacher-b", "completed", 12, 12, 0)
	finished.OutputJSON = batchOutputIDs(ids)
	if err := store.SaveBatchJob(ctx, finished); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimBatchJobImport(ctx, "finished-waiting-import")
	if claimed || !errors.Is(err, storage.ErrGenerationQuotaExceeded) {
		t.Fatalf("full quota must reject import before any question is saved: %v %v", claimed, err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 20 || usage.BatchImportReservedQuestions != 0 {
		t.Fatalf("rejected import changed quota: %+v %v", usage, err)
	}
	if err := store.UpdateBatchJob(ctx, quotaTestBatch("full-running", "teacher-a", "completed", 20, 20, 0)); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimBatchJobImport(ctx, "finished-waiting-import")
	if err != nil || !claimed {
		t.Fatalf("import should proceed after capacity is freed: %v %v", claimed, err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 12 || usage.BatchImportReservedQuestions != 12 || usage.ActiveBatchImports != 1 {
		t.Fatalf("claim must reserve 12 slots: %+v %v", usage, err)
	}
	for i, id := range ids {
		q := aiCheckTestQuestion(t, store, ctx, id)
		if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: fmt.Sprintf("import-bypass-task-%d", i), QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
			t.Fatal(err)
		}
		_, usage, err = store.GetGenerationQuota(ctx)
		if err != nil || usage.PendingQuestionUnits != 12 || usage.BatchImportReservedQuestions != 11-i {
			t.Fatalf("reservation must hand off to check task without double count: %+v %v", usage, err)
		}
	}
	resultJSON, _ := json.Marshal(map[string]any{"saved": 12, "failed": 0, "question_ids": ids})
	if err := store.SaveBatchJobImportResult(ctx, "finished-waiting-import", string(resultJSON)); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 12 || usage.BatchImportReservedQuestions != 0 || usage.ActiveBatchImports != 0 {
		t.Fatalf("finalized import must leave only check tasks: %+v %v", usage, err)
	}
	for i := range ids {
		if err := store.CompleteCheckTask(ctx, fmt.Sprintf("import-bypass-task-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 0 {
		t.Fatalf("completed checks must release all slots: %+v %v", usage, err)
	}
}

func TestConcurrentBatchImportClaimsShareQuota(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	quota := storage.DefaultGenerationQuota()
	quota.BatchMaxQuestions, quota.GlobalPendingQuestions = 20, 20
	if err := store.SaveGenerationQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ids := make([]string, 12)
		for j := range ids {
			ids[j] = fmt.Sprintf("parallel-import-%d-%d", i, j)
		}
		job := quotaTestBatch(fmt.Sprintf("parallel-import-job-%d", i), fmt.Sprintf("owner-%d", i), "completed", 12, 12, 0)
		job.OutputJSON = batchOutputIDs(ids)
		if err := store.SaveBatchJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := store.ClaimBatchJobImport(context.Background(), fmt.Sprintf("parallel-import-job-%d", i))
			if err == nil && !claimed {
				err = fmt.Errorf("claim returned false without quota rejection")
			}
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, storage.ErrGenerationQuotaExceeded) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("parallel imports: accepted=%d rejected=%d", accepted, rejected)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.PendingQuestionUnits != 12 || usage.BatchImportReservedQuestions != 12 {
		t.Fatalf("parallel import quota: %+v %v", usage, err)
	}
}

func TestBatchImportReservationKeepsOnlySavedUnqueuedQuestions(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	job := quotaTestBatch("partial-import", "teacher", "completed", 2, 2, 0)
	job.OutputJSON = batchOutputIDs([]string{"saved-q", "failed-save-q"})
	if err := store.SaveBatchJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimBatchJobImport(ctx, job.ID); err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	_, usage, err := store.GetGenerationQuota(ctx)
	if err != nil || usage.BatchImportReservedQuestions != 2 {
		t.Fatalf("initial reservation: %+v %v", usage, err)
	}
	q := aiCheckTestQuestion(t, store, ctx, "saved-q")
	resultJSON, _ := json.Marshal(map[string]any{"saved": 1, "failed": 1, "question_ids": []string{q.ID}})
	if err := store.SaveBatchJobImportResult(ctx, job.ID, string(resultJSON)); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.BatchImportReservedQuestions != 1 || usage.PendingQuestionUnits != 1 {
		t.Fatalf("failed save must release its reservation: %+v %v", usage, err)
	}
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "partial-check", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	_, usage, err = store.GetGenerationQuota(ctx)
	if err != nil || usage.BatchImportReservedQuestions != 0 || usage.ActiveAICheckTasks != 1 || usage.PendingQuestionUnits != 1 {
		t.Fatalf("late check enqueue must replace reservation: %+v %v", usage, err)
	}
}

func TestCompletedLegacyBatchLargerThanCurrentGlobalCapExplainsRemedy(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	quota := storage.DefaultGenerationQuota()
	quota.BatchMaxQuestions, quota.GlobalPendingQuestions = 20, 20
	if err := store.SaveGenerationQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = fmt.Sprintf("legacy-larger-%d", i)
	}
	job := quotaTestBatch("legacy-larger-job", "teacher", "completed", 21, 21, 0)
	job.OutputJSON = batchOutputIDs(ids)
	if err := store.SaveBatchJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimBatchJobImport(ctx, job.ID)
	if claimed || !errors.Is(err, storage.ErrGenerationQuotaExceeded) || !strings.Contains(err.Error(), "调整上限") {
		t.Fatalf("legacy job should explain admin remedy: %v %v", claimed, err)
	}
}

func TestQuotaPermissionMigrationRemovesLegacyGrants(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	// 独立测试 schema 回到 v33 台账，再人为造出旧版允许的“授权却不可用”数据。
	if _, err := store.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version IN (34,35)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO roles (id,name,permissions,is_builtin) VALUES ('legacy-quota-role','旧自定义角色',ARRAY['generation_quota:manage'],false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO users (id,username,password_hash,display_name,role,roles,permissions) VALUES ('legacy-quota-user','legacy-quota-user','unused','旧账号','teacher',ARRAY['teacher'],ARRAY['generation_quota:manage'])`); err != nil {
		t.Fatal(err)
	}
	applied, err := store.Migrate(ctx)
	if err != nil || len(applied) != 2 {
		t.Fatalf("apply migrations 34/35: %v %+v", err, applied)
	}
	var roleHas, userHas bool
	if err := store.db.QueryRowContext(ctx, `SELECT 'generation_quota:manage'=ANY(permissions) FROM roles WHERE id='legacy-quota-role'`).Scan(&roleHas); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT 'generation_quota:manage'=ANY(permissions) FROM users WHERE id='legacy-quota-user'`).Scan(&userHas); err != nil {
		t.Fatal(err)
	}
	if roleHas || userHas {
		t.Fatalf("old invalid grants survived migration: role=%v user=%v", roleHas, userHas)
	}
}
