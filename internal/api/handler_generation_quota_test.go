package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
)

func quotaHTTPTeacher(t *testing.T, handler http.Handler, adminToken, name string) (string, string) {
	t.Helper()
	response := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.31", map[string]any{
		"username": name, "password": "quota-test-password", "display_name": "配额测试教师", "role": domain.RoleTeacher,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("create teacher: %d %s", response.Code, response.Body)
	}
	var user struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	return user.ID, loginForAuthTest(t, handler, name, "quota-test-password", "198.51.100.32")
}

func TestGenerationQuotaManagementPermissionAndTeacherRejection(t *testing.T) {
	server, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	server.quotaStore = store
	handler := server.Handler() // 测试不启动生成 worker，也不调用真实模型。
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.30")
	teacherID, teacherToken := quotaHTTPTeacher(t, handler, adminToken, "quota-teacher")
	if got := serveGenerateJSON(t, handler, http.MethodGet, "/api/system/generation-quota", teacherToken, nil); got.Code != http.StatusForbidden {
		t.Fatalf("teacher read quota: %d", got.Code)
	}
	if got := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", teacherToken, storage.DefaultGenerationQuota()); got.Code != http.StatusForbidden {
		t.Fatalf("teacher edit quota: %d", got.Code)
	}
	// 即使误给教师账号直接权限，也不允许其进入仅供管理员的设置页。
	if _, err := store.DB().Exec(`UPDATE users SET permissions=array_append(permissions,$1) WHERE id=$2`, domain.PermGenerationQuotaManage, teacherID); err != nil {
		t.Fatal(err)
	}
	if got := serveGenerateJSON(t, handler, http.MethodGet, "/api/system/generation-quota", teacherToken, nil); got.Code != http.StatusForbidden {
		t.Fatalf("teacher with direct permission must still be blocked: %d", got.Code)
	}

	adminUser := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.30", map[string]any{
		"username": "quota-admin", "password": "quota-admin-password", "display_name": "配额管理员", "role": domain.RoleAdmin,
	})
	if adminUser.Code != http.StatusCreated {
		t.Fatalf("create delegated admin: %d %s", adminUser.Code, adminUser.Body)
	}
	delegatedToken := loginForAuthTest(t, handler, "quota-admin", "quota-admin-password", "198.51.100.33")
	if got := serveGenerateJSON(t, handler, http.MethodGet, "/api/system/generation-quota", delegatedToken, nil); got.Code != http.StatusOK {
		t.Fatalf("admin read quota: %d %s", got.Code, got.Body)
	}
	quota := storage.DefaultGenerationQuota()
	quota.SingleMaxQuestions = 1
	quota.BatchMaxQuestions = 2
	quota.SingleActivePerUser = 1
	quota.GlobalPendingQuestions = 20
	if got := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", delegatedToken, quota); got.Code != http.StatusOK {
		t.Fatalf("admin save quota: %d %s", got.Code, got.Body)
	}

	tooMany := serveGenerateJSON(t, handler, http.MethodPost, "/api/questions/generate", teacherToken, map[string]any{"run_id": "quota-http-too-many", "count": 2})
	if tooMany.Code != http.StatusTooManyRequests || !strings.Contains(tooMany.Body.String(), "最多生成 1 道题") {
		t.Fatalf("single count rejection: %d %s", tooMany.Code, tooMany.Body)
	}
	first := serveGenerateJSON(t, handler, http.MethodPost, "/api/questions/generate", teacherToken, map[string]any{"run_id": "quota-http-first", "count": 1})
	if first.Code != http.StatusOK {
		t.Fatalf("first single: %d %s", first.Code, first.Body)
	}
	current := serveGenerateJSON(t, handler, http.MethodGet, "/api/system/generation-quota", delegatedToken, nil)
	var snapshot struct {
		Usage storage.GenerationQuotaUsage `json:"usage"`
	}
	if current.Code != http.StatusOK || json.Unmarshal(current.Body.Bytes(), &snapshot) != nil ||
		snapshot.Usage.ActiveSingleRuns != 1 || snapshot.Usage.SinglePendingQuestions != 1 || snapshot.Usage.PendingQuestionUnits != 1 {
		t.Fatalf("quota API breakdown after accepted run: %d %s", current.Code, current.Body)
	}
	active := serveGenerateJSON(t, handler, http.MethodPost, "/api/questions/generate", teacherToken, map[string]any{"run_id": "quota-http-second", "count": 1})
	if active.Code != http.StatusTooManyRequests || !strings.Contains(active.Body.String(), "未完成的单题任务") {
		t.Fatalf("per-user single rejection: %d %s", active.Code, active.Body)
	}
	if active.Header().Get("Retry-After") == "" {
		t.Fatal("quota rejection must provide Retry-After")
	}

	// 批量超额在任务写入前拒绝；假执行器不会调用千问。
	version, err := server.kpSvc.CreateVersion(context.Background(), "配额测试大纲", 2026, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i, topic := range []string{"要点甲", "要点乙", "要点丙"} {
		point, err := server.kpSvc.CreatePoint(context.Background(), domain.KnowledgePoint{VersionID: version.ID, Category: "临床综合", Subject: "内科", Topic: topic, OutlineCode: "quota." + string(rune('1'+i))})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, point.ID)
	}
	if err := server.kpSvc.PublishVersion(context.Background(), version.ID); err != nil {
		t.Fatal(err)
	}
	server.batchSvc = &ownerProbeBatchExecutor{job: batch.BatchJob{JobID: "fake-quota", OwnerID: teacherID}}
	handler = server.Handler()
	batchRejected := serveGenerateJSON(t, handler, http.MethodPost, "/api/batch/submit", teacherToken, map[string]any{"version_id": version.ID, "knowledge_point_ids": ids, "count": 1})
	if batchRejected.Code != http.StatusTooManyRequests || !strings.Contains(batchRejected.Body.String(), "最多生成 2 道题") {
		t.Fatalf("batch total rejection: %d %s", batchRejected.Code, batchRejected.Body)
	}
}

func TestQuotaPermissionRestrictedToBuiltinAdmins(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.quotaStore = store
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.51")
	meta := serveGenerateJSON(t, handler, http.MethodGet, "/api/permissions", adminToken, nil)
	var catalog struct {
		Permissions []struct {
			Code      string `json:"code"`
			AdminOnly bool   `json:"admin_only"`
		} `json:"permissions"`
	}
	if meta.Code != http.StatusOK || json.Unmarshal(meta.Body.Bytes(), &catalog) != nil {
		t.Fatalf("permission catalog: %d %s", meta.Code, meta.Body)
	}
	found := false
	for _, p := range catalog.Permissions {
		if p.Code == domain.PermGenerationQuotaManage {
			found = p.AdminOnly
		}
	}
	if !found {
		t.Fatal("quota permission must be marked as builtin-admin-only")
	}
	role := serveGenerateJSON(t, handler, http.MethodPost, "/api/roles", adminToken, map[string]any{
		"id": "role-quota-custom", "name": "自定义配额管理员", "permissions": []string{domain.PermGenerationQuotaManage},
	})
	if role.Code != http.StatusBadRequest || !strings.Contains(role.Body.String(), "仅属于内置") {
		t.Fatalf("custom role grant must be rejected: %d %s", role.Code, role.Body)
	}
	created := serveGenerateJSON(t, handler, http.MethodPost, "/api/users", adminToken, map[string]any{
		"username": "direct-quota-user", "password": "direct-quota-password", "display_name": "普通用户", "role": domain.RoleTeacher,
		"permissions": []string{domain.PermGenerationQuotaManage},
	})
	if created.Code != http.StatusBadRequest || !strings.Contains(created.Body.String(), "不能直接分配") {
		t.Fatalf("direct grant must be rejected: %d %s", created.Code, created.Body)
	}
}

func TestBatchImportQuotaRejectionAndRecoveryWithoutModelCall(t *testing.T) {
	server, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	server.quotaStore = store
	executor := batch.NewLocalExecutor(generator.NewService(&fakeAPILLM{}), store, store, "offline-fake")
	executor.SetDraftChecker(server.aiCheckSvc)
	server.batchSvc = executor
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.61")
	teacherID, teacherToken := quotaHTTPTeacher(t, handler, adminToken, "quota-import-teacher")
	quota := storage.DefaultGenerationQuota()
	quota.BatchMaxQuestions, quota.GlobalPendingQuestions = 20, 20
	if err := store.SaveGenerationQuota(context.Background(), quota); err != nil {
		t.Fatal(err)
	}
	active := storage.BatchJobRecord{ID: "quota-import-active", OwnerID: "other", Backend: "local_single_api", Status: "in_progress", TotalCount: 20, PointsJSON: "[]", OutputJSON: `{"questions":[],"items":[]}`}
	if err := store.SaveBatchJob(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	question := domain.A2Question{ID: "quota-import-question", ClinicalStem: "男，45岁。腹痛后就诊，最可能的诊断是什么？", Options: []domain.Option{
		{Label: "A", Text: "胃溃疡"}, {Label: "B", Text: "十二指肠溃疡"}, {Label: "C", Text: "胃炎"}, {Label: "D", Text: "肠炎"}, {Label: "E", Text: "胰腺炎"}}, Answer: "A", Difficulty: domain.Difficulty("0.65"), Status: domain.StatusAIDraft, Version: 1}
	output, _ := json.Marshal(map[string]any{"questions": []domain.A2Question{question}, "items": []batch.ImportItem{{Status: "ok", Count: 1}}})
	finished := storage.BatchJobRecord{ID: "quota-import-finished", OwnerID: teacherID, Backend: "local_single_api", Status: "completed", TotalCount: 1, Completed: 1, PointsJSON: "[]", OutputJSON: string(output)}
	if err := store.SaveBatchJob(context.Background(), finished); err != nil {
		t.Fatal(err)
	}
	rejected := serveGenerateJSON(t, handler, http.MethodPost, "/api/batch/download/quota-import-finished", teacherToken, nil)
	if rejected.Code != http.StatusTooManyRequests || rejected.Header().Get("Retry-After") == "" {
		t.Fatalf("full queue must reject import with retry hint: %d %s", rejected.Code, rejected.Body)
	}
	if q, err := store.GetQuestion(context.Background(), question.ID); err != nil || q != nil {
		t.Fatalf("rejected import must not save question: %+v %v", q, err)
	}
	active.Status, active.Completed = "completed", 20
	if err := store.UpdateBatchJob(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	accepted := serveGenerateJSON(t, handler, http.MethodPost, "/api/batch/download/quota-import-finished", teacherToken, nil)
	if accepted.Code != http.StatusOK {
		t.Fatalf("import after capacity frees: %d %s", accepted.Code, accepted.Body)
	}
	_, usage, err := store.GetGenerationQuota(context.Background())
	if err != nil || usage.PendingQuestionUnits != 1 || usage.ActiveAICheckTasks != 1 || usage.BatchImportReservedQuestions != 0 {
		t.Fatalf("import should hand off reservation to one check task: %+v %v", usage, err)
	}
}
