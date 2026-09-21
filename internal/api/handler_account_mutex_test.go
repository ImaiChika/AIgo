package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/review"
)

// 账号互斥占用守卫验收：
// 1. 属主侧审核流程占用（审核中/待决断/退修中）拦截停用与收回编辑权限，提示含具体题目与状态；
// 2. 停用时拦截未导入批量任务与运行中命题任务；
// 3. 多身份模板权限并集：移除其中一个模板但另一模板仍覆盖审题权限时不误拦，全部移除才拦；
// 4. 专家库镜像随"失去审题权限/停用/删号"同步清理，恢复时自动补回；
// 5. 反向守卫：属主账号停用时管理员撤回 published 题被拦截。
func TestAccountOperationMutualExclusion(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.reviewSvc = review.NewService(store, store, store, server.authSvc)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	ctx := context.Background()
	now := time.Now()

	createUser := func(body map[string]any) string {
		t.Helper()
		resp := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", body)
		if resp.Code != http.StatusCreated {
			t.Fatalf("create user %v: %d %s", body["username"], resp.Code, resp.Body.String())
		}
		var u struct {
			ID string `json:"id"`
		}
		json.Unmarshal(resp.Body.Bytes(), &u)
		if u.ID == "" {
			t.Fatalf("create user %v: missing id", body["username"])
		}
		return u.ID
	}
	updateUser := func(userID string, body map[string]any) (int, string) {
		t.Helper()
		resp := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+userID, adminToken, "198.51.100.1", body)
		return resp.Code, resp.Body.String()
	}
	saveQuestion := func(id, ownerID, createdBy string, status domain.QuestionStatus) {
		t.Helper()
		q := domain.A2Question{
			ID: id, OwnerID: ownerID, CreatedBy: createdBy,
			ClinicalStem: "男，50岁。突发胸痛2小时。该患者最可能的诊断是",
			Options:      []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}},
			Answer:      "A", Status: status, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatalf("save question %s: %v", id, err)
		}
	}

	// ===== 1. 属主侧审核占用拦截停用/收权 =====
	teacherID := createUser(map[string]any{
		"username": "mutex-teacher", "password": "mutex-pass-1", "display_name": "占用教师", "role": "teacher",
	})
	reviewerID := createUser(map[string]any{
		"username": "mutex-reviewer", "password": "mutex-pass-1", "display_name": "占用审题", "role": "expert",
	})
	flow := domain.ReviewFlowConfig{
		ID: "mutex-flow", Name: "互斥流程", CreatedAt: now,
		Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "一审", ExpertIDs: []string{reviewerID}, RequiredCount: 1}},
	}
	if err := store.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	saveQuestion("mutex-q-reviewing", teacherID, "mutex-teacher", domain.StatusAIReviewed)
	teacherToken := loginForAuthTest(t, handler, "mutex-teacher", "mutex-pass-1", "198.51.100.2")
	submitted := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", teacherToken, "198.51.100.2",
		map[string]any{"question_id": "mutex-q-reviewing", "flow_id": flow.ID})
	if submitted.Code != http.StatusOK {
		t.Fatalf("submit review: %d %s", submitted.Code, submitted.Body.String())
	}

	// 停用被拦，提示包含题目 ID 与"审核中"
	code, body := updateUser(teacherID, map[string]any{"display_name": "占用教师", "role": "teacher", "enabled": false})
	if code != http.StatusConflict || !strings.Contains(body, "mutex-q-reviewing") || !strings.Contains(body, "审核中") {
		t.Fatalf("disable teacher with reviewing question: %d %s", code, body)
	}
	// 收回编辑权限同样被拦（换成 expert 身份后不再有 question:edit）
	code, body = updateUser(teacherID, map[string]any{"display_name": "占用教师", "role": "expert"})
	if code != http.StatusConflict || !strings.Contains(body, "mutex-q-reviewing") {
		t.Fatalf("revoke edit with reviewing question: %d %s", code, body)
	}
	// 不变的更新（改显示名）不受影响
	code, body = updateUser(teacherID, map[string]any{"display_name": "占用教师2", "role": "teacher"})
	if code != http.StatusOK {
		t.Fatalf("rename should pass: %d %s", code, body)
	}

	// ===== 2. 多身份模板并集 =====
	roleA := serveAuthJSON(t, handler, http.MethodPost, "/api/roles", adminToken, "198.51.100.1",
		map[string]any{"id": "mutex-role-a", "name": "审题A", "permissions": []string{domain.PermReviewDo}})
	if roleA.Code != http.StatusCreated {
		t.Fatalf("create roleA: %d %s", roleA.Code, roleA.Body.String())
	}
	roleB := serveAuthJSON(t, handler, http.MethodPost, "/api/roles", adminToken, "198.51.100.1",
		map[string]any{"id": "mutex-role-b", "name": "审题B", "permissions": []string{domain.PermReviewDo}})
	if roleB.Code != http.StatusCreated {
		t.Fatalf("create roleB: %d %s", roleB.Code, roleB.Body.String())
	}
	dualID := createUser(map[string]any{
		"username": "mutex-dual", "password": "mutex-pass-1", "display_name": "双模板", "role": "mutex-role-a",
		"roles": []string{"mutex-role-a", "mutex-role-b"},
	})
	flowDual := domain.ReviewFlowConfig{
		ID: "mutex-flow-dual", Name: "双模板流程", CreatedAt: now,
		Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "一审", ExpertIDs: []string{dualID}, RequiredCount: 1}},
	}
	if err := store.SaveFlowConfig(ctx, flowDual); err != nil {
		t.Fatal(err)
	}
	// 移除模板 A 但保留模板 B：审题权限仍由 B 覆盖，不触发守卫
	code, body = updateUser(dualID, map[string]any{"display_name": "双模板", "role": "mutex-role-b", "roles": []string{"mutex-role-b"}})
	if code != http.StatusOK {
		t.Fatalf("removing one of two review templates should pass: %d %s", code, body)
	}
	// 两个模板全部移除（换成 teacher）：失去审题权限且被流程引用 → 拦截
	code, body = updateUser(dualID, map[string]any{"display_name": "双模板", "role": "teacher"})
	if code != http.StatusConflict || !strings.Contains(body, "双模板流程") {
		t.Fatalf("removing all review templates must be blocked: %d %s", code, body)
	}

	// ===== 3. 停用时拦截自主任务占用（批量未导入/命题运行中）=====
	autonomousID := createUser(map[string]any{
		"username": "mutex-auto", "password": "mutex-pass-1", "display_name": "自主任务", "role": "teacher",
	})
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO batch_jobs (id, backend, job_name, status, owner_id) VALUES ($1,'local_single_api',$2,'completed',$3)`,
		"mutex-batch-1", "互斥批量任务", autonomousID); err != nil {
		t.Fatal(err)
	}
	code, body = updateUser(autonomousID, map[string]any{"display_name": "自主任务", "role": "teacher", "enabled": false})
	if code != http.StatusConflict || !strings.Contains(body, "互斥批量任务") || !strings.Contains(body, "未导入") {
		t.Fatalf("disable with unimported batch: %d %s", code, body)
	}
	// 导入后放行
	if _, err := store.DB().ExecContext(ctx, `UPDATE batch_jobs SET imported_at=NOW() WHERE id=$1`, "mutex-batch-1"); err != nil {
		t.Fatal(err)
	}
	// 再造一个运行中的命题任务 → 仍然拦截
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO generation_runs (id, owner_id, status, requested_count, started_at, updated_at) VALUES ($1,$2,'pending',1,NOW(),NOW())`,
		"mutex-gen-run-1", autonomousID); err != nil {
		t.Fatal(err)
	}
	code, body = updateUser(autonomousID, map[string]any{"display_name": "自主任务", "role": "teacher", "enabled": false})
	if code != http.StatusConflict || !strings.Contains(body, "mutex-gen-run-1") || !strings.Contains(body, "正在执行") {
		t.Fatalf("disable with running generation: %d %s", code, body)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE generation_runs SET status='failed' WHERE id=$1`, "mutex-gen-run-1"); err != nil {
		t.Fatal(err)
	}
	code, body = updateUser(autonomousID, map[string]any{"display_name": "自主任务", "role": "teacher", "enabled": false})
	if code != http.StatusOK {
		t.Fatalf("disable after clearing blockers should pass: %d %s", code, body)
	}
	// 恢复启用供后续用例
	code, body = updateUser(autonomousID, map[string]any{"display_name": "自主任务", "role": "teacher", "enabled": true})
	if code != http.StatusOK {
		t.Fatalf("re-enable: %d %s", code, body)
	}

	// ===== 4. 专家库镜像清理与补回 =====
	mirrorID := createUser(map[string]any{
		"username": "mutex-mirror", "password": "mutex-pass-1", "display_name": "镜像专家", "role": "expert",
	})
	expertExists := func(id string) bool {
		var count int
		if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM experts WHERE id=$1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count > 0
	}
	if !expertExists(mirrorID) {
		t.Fatal("expert entry should be created for review-permission user")
	}
	// 停用 → 专家条目清理
	code, body = updateUser(mirrorID, map[string]any{"display_name": "镜像专家", "role": "expert", "enabled": false})
	if code != http.StatusOK || expertExists(mirrorID) {
		t.Fatalf("disable should remove expert mirror: %d %s", code, body)
	}
	// 重新启用 → 自动补回
	code, body = updateUser(mirrorID, map[string]any{"display_name": "镜像专家", "role": "expert", "enabled": true})
	if code != http.StatusOK || !expertExists(mirrorID) {
		t.Fatalf("re-enable should restore expert mirror: %d %s", code, body)
	}
	// 删号 → 专家条目清理（无任何占用可删）
	deleted := serveAuthJSON(t, handler, http.MethodDelete, "/api/users/"+mirrorID, adminToken, "198.51.100.1", nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete mirror user: %d %s", deleted.Code, deleted.Body.String())
	}
	if expertExists(mirrorID) {
		t.Fatal("delete user should remove expert mirror entry")
	}

	// ===== 5. 反向守卫：属主停用时撤回 published 被拦 =====
	publishTeacherID := createUser(map[string]any{
		"username": "mutex-publish", "password": "mutex-pass-1", "display_name": "定稿教师", "role": "teacher",
	})
	saveQuestion("mutex-q-published", publishTeacherID, "mutex-publish", domain.StatusAIReviewed)
	publishToken := loginForAuthTest(t, handler, "mutex-publish", "mutex-pass-1", "198.51.100.3")
	pubSubmit := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", publishToken, "198.51.100.3",
		map[string]any{"question_id": "mutex-q-published", "flow_id": flow.ID})
	if pubSubmit.Code != http.StatusOK {
		t.Fatalf("submit publish flow: %d %s", pubSubmit.Code, pubSubmit.Body.String())
	}
	var taskID string
	if err := store.DB().QueryRowContext(ctx, `SELECT id FROM review_tasks WHERE question_id=$1`, "mutex-q-published").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	reviewerToken := loginForAuthTest(t, handler, "mutex-reviewer", "mutex-pass-1", "198.51.100.4")
	action := serveAuthJSON(t, handler, http.MethodPost, "/api/review/action", reviewerToken, "198.51.100.4",
		map[string]any{"task_id": taskID, "action": "approved", "opinion": "同意"})
	if action.Code != http.StatusOK {
		t.Fatalf("review action: %d %s", action.Code, action.Body.String())
	}
	finalize := serveAuthJSON(t, handler, http.MethodPost, "/api/review/finalize", adminToken, "198.51.100.1",
		map[string]any{"task_id": taskID, "action": "approved", "opinion": "准予定稿"})
	if finalize.Code != http.StatusOK {
		t.Fatalf("finalize: %d %s", finalize.Code, finalize.Body.String())
	}
	// 停用定稿教师（published 不属于流程占用，允许停用——这是拥有题目的设计出口）
	code, body = updateUser(publishTeacherID, map[string]any{"display_name": "定稿教师", "role": "teacher", "enabled": false})
	if code != http.StatusOK {
		t.Fatalf("disable owner of published question should pass: %d %s", code, body)
	}
	unpublish := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/mutex-q-published/unpublish", adminToken, "198.51.100.1",
		map[string]any{"reason": "测试撤回"})
	if unpublish.Code != http.StatusConflict || !strings.Contains(unpublish.Body.String(), "已停用") {
		t.Fatalf("unpublish with disabled owner must be blocked: %d %s", unpublish.Code, unpublish.Body.String())
	}
	// 恢复账号后撤回成功
	code, body = updateUser(publishTeacherID, map[string]any{"display_name": "定稿教师", "role": "teacher", "enabled": true})
	if code != http.StatusOK {
		t.Fatalf("re-enable owner: %d %s", code, body)
	}
	unpublish2 := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/mutex-q-published/unpublish", adminToken, "198.51.100.1",
		map[string]any{"reason": "测试撤回"})
	if unpublish2.Code != http.StatusOK {
		t.Fatalf("unpublish after re-enable should pass: %d %s", unpublish2.Code, unpublish2.Body.String())
	}
}
