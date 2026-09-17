package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

type ownerProbeBatchExecutor struct {
	job    batch.BatchJob
	change storage.QuestionChange
}

func (f *ownerProbeBatchExecutor) GenerateAndSubmit(context.Context, []domain.KnowledgePoint, int, string) (string, int, error) {
	return f.job.JobID, 1, nil
}
func (f *ownerProbeBatchExecutor) GetJobStatus(context.Context, string) (*batch.BatchJob, error) {
	job := f.job
	return &job, nil
}
func (f *ownerProbeBatchExecutor) ListJobs(context.Context, string, string, int) ([]batch.BatchJob, error) {
	return []batch.BatchJob{f.job}, nil
}
func (f *ownerProbeBatchExecutor) ImportResults(ctx context.Context, _ string, _ []domain.KnowledgePoint) (*batch.ImportResult, error) {
	f.change = storage.QuestionChangeFromContext(ctx)
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
