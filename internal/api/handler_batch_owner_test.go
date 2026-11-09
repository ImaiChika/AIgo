package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

type ownerProbeBatchExecutor struct {
	job    batch.BatchJob
	jobs   []batch.BatchJob
	change storage.QuestionChange
}

func (f *ownerProbeBatchExecutor) SubmitPlan(_ context.Context, submission batch.PlanSubmission) (string, int, error) {
	return f.job.JobID, submission.Plan.RequestedTotal, nil
}

func (f *ownerProbeBatchExecutor) GetBatchJobByRequestKey(context.Context, string, string) (*storage.BatchJobRecord, error) {
	return nil, nil
}

func (f *ownerProbeBatchExecutor) GenerateAndSubmit(context.Context, []domain.KnowledgePoint, int, string) (string, int, error) {
	return f.job.JobID, 1, nil
}
func (f *ownerProbeBatchExecutor) GetJobStatus(_ context.Context, id string) (*batch.BatchJob, error) {
	if f.jobs != nil {
		for _, candidate := range f.jobs {
			if candidate.JobID == id {
				job := candidate
				return &job, nil
			}
		}
		return nil, nil
	}
	job := f.job
	return &job, nil
}
func (f *ownerProbeBatchExecutor) ListJobs(context.Context, string, string, int) ([]batch.BatchJob, error) {
	return []batch.BatchJob{f.job}, nil
}
func (f *ownerProbeBatchExecutor) ListJobsPage(_ context.Context, ownerID, name, status string, limit, offset int) ([]batch.BatchJob, int, error) {
	all := f.jobs
	if all == nil {
		all = []batch.BatchJob{f.job}
	}
	filtered := []batch.BatchJob{}
	for _, job := range all {
		if ownerID != "" && job.OwnerID != ownerID {
			continue
		}
		if status != "" && job.Status != status {
			continue
		}
		if name != "" && !strings.Contains(job.JobName, name) {
			continue
		}
		filtered = append(filtered, job)
	}
	total := len(filtered)
	if offset >= total {
		return []batch.BatchJob{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}
func (f *ownerProbeBatchExecutor) ImportResults(ctx context.Context, _ string, _ []domain.KnowledgePoint) (*batch.ImportResult, error) {
	f.change = storage.QuestionChangeFromContext(ctx)
	if f.job.ImportedAt == "" {
		f.job.ImportedAt = "2026-09-23T00:00:00Z"
	}
	return &batch.ImportResult{}, nil
}
func (f *ownerProbeBatchExecutor) RetryFailed(context.Context, string) (*batch.BatchJob, error) {
	job := f.job
	return &job, nil
}
func (f *ownerProbeBatchExecutor) Capabilities() batch.Capabilities {
	return batch.Capabilities{Available: true, Backend: "local_single_api"}
}

func TestBatchImportIsOwnerOnlyAndKeepsOriginalOwnership(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.100")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.100", map[string]any{
		"username": "batch-owner", "password": "batch-owner-password", "display_name": "批量老师", "role": domain.RoleTeacher,
	})
	var owner auth.User
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &owner) != nil {
		t.Fatalf("create owner: %d %s", created.Code, created.Body)
	}
	probe := &ownerProbeBatchExecutor{job: batch.BatchJob{JobID: "owner-job", OwnerID: owner.ID, Status: "completed", Tracked: true}}
	server.batchSvc = probe
	handler = server.Handler()

	adminImport := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/download/owner-job", adminToken, "198.51.100.100", nil)
	if adminImport.Code != http.StatusForbidden {
		t.Fatalf("admin must not import another user's batch: %d %s", adminImport.Code, adminImport.Body)
	}
	ownerToken := loginForAuthTest(t, handler, owner.Username, "batch-owner-password", "198.51.100.101")
	ownerImport := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/download/owner-job", ownerToken, "198.51.100.101", nil)
	if ownerImport.Code != http.StatusOK {
		t.Fatalf("owner import failed: %d %s", ownerImport.Code, ownerImport.Body)
	}
	if probe.change.OwnerID != owner.ID || probe.change.Actor != owner.Username {
		t.Fatalf("import ownership changed: %+v", probe.change)
	}
}

func TestBatchHistoryReplayDoesNotCreateImportAudit(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.100")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.100", map[string]any{
		"username": "batch-history-owner", "password": "batch-owner-password", "display_name": "批量老师", "role": domain.RoleTeacher,
	})
	var owner auth.User
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &owner) != nil {
		t.Fatalf("create owner: %d %s", created.Code, created.Body)
	}
	probe := &ownerProbeBatchExecutor{job: batch.BatchJob{JobID: "history-job", JobName: "回看测试", OwnerID: owner.ID, Status: "completed", Tracked: true}}
	server.batchSvc = probe
	handler = server.Handler()
	ownerToken := loginForAuthTest(t, handler, owner.Username, "batch-owner-password", "198.51.100.101")

	countImports := func() int {
		t.Helper()
		var count int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='batch_import'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	// 列表和任务切换只是读取，不能生成导入日志。
	for _, path := range []string{"/api/batch/list", "/api/batch/status/history-job"} {
		response := serveAuthJSON(t, handler, http.MethodGet, path, ownerToken, "198.51.100.101", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("read %s: %d %s", path, response.Code, response.Body)
		}
	}
	if count := countImports(); count != 0 {
		t.Fatalf("read-only history created %d import logs", count)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		response := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/download/history-job", ownerToken, "198.51.100.101", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("import/replay %d: %d %s", attempt, response.Code, response.Body)
		}
		if count := countImports(); count != 1 {
			t.Fatalf("after import/replay %d, audit count=%d, want 1", attempt, count)
		}
	}
	// 管理员查看已导入历史结果，同样不能被记成一次导入。
	response := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/download/history-job", adminToken, "198.51.100.100", nil)
	if response.Code != http.StatusOK || countImports() != 1 {
		t.Fatalf("admin history replay: status=%d audit count=%d body=%s", response.Code, countImports(), response.Body)
	}
}

func TestBatchHistoryPagesAfterOwnerFilterAndAdminCanReadAll(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	superToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.130")
	createUser := func(username, role string) auth.User {
		t.Helper()
		response := serveAuthJSON(t, handler, http.MethodPost, "/api/users", superToken, "198.51.100.130", map[string]any{
			"username": username, "password": "history-test-password", "display_name": username, "role": role,
		})
		var user auth.User
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &user) != nil {
			t.Fatalf("create %s: %d %s", username, response.Code, response.Body)
		}
		return user
	}
	teacher := createUser("history-teacher", domain.RoleTeacher)
	manager := createUser("history-manager", domain.RoleAdmin)
	probe := &ownerProbeBatchExecutor{jobs: []batch.BatchJob{
		{JobID: "other-1", JobName: "其他任务", OwnerID: manager.ID, Status: "completed", ImportedAt: "2026-09-23"},
		{JobID: "mine-1", JobName: "我的任务一", OwnerID: teacher.ID, Status: "completed", ImportedAt: "2026-09-23"},
		{JobID: "mine-2", JobName: "我的任务二", OwnerID: teacher.ID, Status: "completed", ImportedAt: "2026-09-23"},
	}}
	server.batchSvc = probe
	handler = server.Handler()
	teacherToken := loginForAuthTest(t, handler, teacher.Username, "history-test-password", "198.51.100.131")
	managerToken := loginForAuthTest(t, handler, manager.Username, "history-test-password", "198.51.100.132")
	read := func(token, path string, wantStatus, wantTotal int) {
		t.Helper()
		response := serveAuthJSON(t, handler, http.MethodGet, path, token, "198.51.100.131", nil)
		if response.Code != wantStatus {
			t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body)
		}
		if wantStatus == http.StatusOK {
			var body struct {
				Total int              `json:"total"`
				Jobs  []batch.BatchJob `json:"jobs"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Total != wantTotal {
				t.Fatalf("%s: body=%+v err=%v", path, body, err)
			}
			if len(body.Jobs) != 1 {
				t.Fatalf("%s: page has %d jobs, want 1", path, len(body.Jobs))
			}
		}
	}
	read(teacherToken, "/api/batch/history?scope=mine&page_size=1&page=1", http.StatusOK, 2)
	read(teacherToken, "/api/batch/history?scope=global&page_size=1", http.StatusForbidden, 0)
	read(managerToken, "/api/batch/history?scope=global&page_size=1", http.StatusOK, 3)
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/batch/status/other-1", teacherToken, "198.51.100.131", nil); response.Code != http.StatusForbidden {
		t.Fatalf("teacher read another task: %d", response.Code)
	}
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/batch/status/mine-1", managerToken, "198.51.100.132", nil); response.Code != http.StatusOK {
		t.Fatalf("manager could not read task: %d %s", response.Code, response.Body)
	}
	if response := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/download/mine-1", managerToken, "198.51.100.132", nil); response.Code != http.StatusOK {
		t.Fatalf("manager could not replay imported result: %d %s", response.Code, response.Body)
	}
	// 管理员默认拥有批量推理后，批量提交应通过权限门（测试环境 AI 检查未就绪时为 503）。
	if response := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/submit", managerToken, "198.51.100.132", nil); response.Code == http.StatusForbidden {
		t.Fatalf("manager batch submit should pass the permission gate: %d %s", response.Code, response.Body)
	}
	// 查看全局历史（question:view_global）仍不能替代批量推理权限。
	viewer := createUser("history-viewer", domain.RoleExpert)
	if response := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+viewer.ID, superToken, "198.51.100.130", map[string]any{
		"display_name": viewer.DisplayName, "role": domain.RoleExpert, "permissions": []string{domain.PermQuestionViewGlobal}, "bank_ids": []string{}, "enabled": true,
	}); response.Code != http.StatusOK {
		t.Fatalf("grant viewer view_global: %d %s", response.Code, response.Body)
	}
	viewerToken := loginForAuthTest(t, handler, viewer.Username, "history-test-password", "198.51.100.133")
	if response := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/submit", viewerToken, "198.51.100.133", nil); response.Code != http.StatusForbidden {
		t.Fatalf("history access granted generation permission: %d", response.Code)
	}

	// 存储层必须先过滤归属再分页，不能先取全局前一页再在 API 层裁剪。
	for _, job := range probe.jobs {
		if err := store.SaveBatchJob(context.Background(), storage.BatchJobRecord{ID: job.JobID, OwnerID: job.OwnerID, JobName: job.JobName, Status: job.Status, PointsJSON: "[]"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := store.ListBatchJobsPage(context.Background(), teacher.ID, "", "", 1, 1)
	if err != nil || total != 2 || len(rows) != 1 || rows[0].OwnerID != teacher.ID {
		t.Fatalf("storage owner paging: rows=%+v total=%d err=%v", rows, total, err)
	}
}

func TestBatchRetryFailedIsOwnerOnly(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.100")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.100", map[string]any{
		"username": "batch-retry-owner", "password": "batch-owner-password", "display_name": "批量老师", "role": domain.RoleTeacher,
	})
	var owner auth.User
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &owner) != nil {
		t.Fatalf("create owner: %d %s", created.Code, created.Body)
	}
	probe := &ownerProbeBatchExecutor{job: batch.BatchJob{JobID: "retry-job", OwnerID: owner.ID, Status: "completed", Tracked: true}}
	server.batchSvc = probe
	handler = server.Handler()

	// 非所有者（即使管理员）不能重跑他人任务
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/retry-failed/retry-job", adminToken, "198.51.100.100", nil); got.Code != http.StatusForbidden {
		t.Fatalf("admin must not retry another user's batch: %d %s", got.Code, got.Body)
	}
	ownerToken := loginForAuthTest(t, handler, owner.Username, "batch-owner-password", "198.51.100.101")
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/batch/retry-failed/retry-job", ownerToken, "198.51.100.101", nil); got.Code != http.StatusOK {
		t.Fatalf("owner retry failed: %d %s", got.Code, got.Body)
	}
}

func TestMissingLocalBatchReturnsNotFoundForAllReadActions(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	executor := batch.NewLocalExecutorWithConfig(nil, store, store, "test", batch.LocalConfig{Concurrency: 2})
	defer executor.Close()
	server.batchSvc = executor
	handler := server.Handler()
	token := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.44")
	for _, tc := range []struct{ name, method, path string }{{"status", "GET", "/api/batch/status/missing"}, {"download", "POST", "/api/batch/download/missing"}, {"retry", "POST", "/api/batch/retry-failed/missing"}} {
		t.Run(tc.name, func(t *testing.T) {
			r := serveAuthJSON(t, handler, tc.method, tc.path, token, "198.51.100.44", nil)
			if r.Code != 404 || !strings.Contains(r.Body.String(), "不存在") {
				t.Fatalf("missing job: %d %s", r.Code, r.Body)
			}
		})
	}
}
