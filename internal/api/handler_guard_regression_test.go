package api

// 修复回归（PG 集成）：
//  1. 流程删除（归档）后，其配置级引用不再锁定审核人收权；
//     只有模板引用也不再锁定，实际在途任务仍拦截。
//  2. 资格预检先于占用守卫：越权/受保护操作即使目标有审核占用也先得 403，
//     不再把占用细节（409）泄露给无权操作者；有权者仍见 409。

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage/postgres"
)

func guardSetup(t *testing.T) (http.Handler, *postgres.Store, func()) {
	t.Helper()
	server, store, cleanup := authHandlerTestServer(t)
	server.reviewSvc = review.NewService(store, store, store, server.authSvc)
	return server.Handler(), store, cleanup
}

func guardCreateUser(t *testing.T, h http.Handler, adminToken string, body map[string]any) string {
	t.Helper()
	resp := serveAuthJSON(t, h, http.MethodPost, "/api/users", adminToken, "198.51.100.1", body)
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

func guardUpdateUser(t *testing.T, h http.Handler, token, userID string, body map[string]any) (int, string) {
	t.Helper()
	resp := serveAuthJSON(t, h, http.MethodPut, "/api/users/"+userID, token, "198.51.100.1", body)
	return resp.Code, resp.Body.String()
}

func TestArchivedFlowNoLongerLocksReviewer(t *testing.T) {
	h, store, cleanup := guardSetup(t)
	defer cleanup()
	adminToken := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.1")
	ctx := context.Background()
	now := time.Now()

	reviewerID := guardCreateUser(t, h, adminToken, map[string]any{
		"username": "guard-arch-rev", "password": "guard-pass-1", "display_name": "归档流程审题", "role": "expert"})
	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "guard-arch-flow", Name: "归档守卫流程", CreatedAt: now,
		Rounds:           []domain.RoundConfig{{RoundNumber: 1, Name: "一审", ExpertIDs: []string{reviewerID}, RequiredCount: 1}},
		FinalReviewerIDs: []string{reviewerID},
	}); err != nil {
		t.Fatal(err)
	}

	revoke := map[string]any{
		"display_name": "归档流程审题", "role": "teacher", "roles": []string{"teacher"},
		"permissions": []string{}, "bank_ids": []string{}}

	// 没有实际任务时，未归档模板也不能锁定收权。
	code, body := guardUpdateUser(t, h, adminToken, reviewerID, revoke)
	if code != http.StatusOK {
		t.Fatalf("只有模板引用时收权应 200，实得 %d %s", code, body)
	}

	// 删除流程（归档）。
	resp := serveAuthJSON(t, h, http.MethodDelete, "/api/review/flows/guard-arch-flow", adminToken, "198.51.100.1", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("删除流程应 200，实得 %d %s", resp.Code, resp.Body.String())
	}

	// 归档后：同样的收权放行。
	code, body = guardUpdateUser(t, h, adminToken, reviewerID, revoke)
	if code != http.StatusOK {
		t.Fatalf("流程归档后收权应 200，实得 %d %s", code, body)
	}
}

func TestPermissionPreflightPrecedesOccupancyGuard(t *testing.T) {
	h, store, cleanup := guardSetup(t)
	defer cleanup()
	adminToken := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.1")
	ctx := context.Background()
	now := time.Now()

	// 下放管理员 + 被流程引用占用中的第二管理员。
	delegatedID := guardCreateUser(t, h, adminToken, map[string]any{
		"username": "guard-delegated", "password": "guard-pass-1", "display_name": "下放", "role": "admin"})
	delegatedToken := loginForAuthTest(t, h, "guard-delegated", "guard-pass-1", "198.51.100.2")
	targetID := guardCreateUser(t, h, adminToken, map[string]any{
		"username": "guard-target-admin", "password": "guard-pass-1", "display_name": "占用管理员", "role": "admin"})

	var superID string
	resp := serveAuthJSON(t, h, http.MethodGet, "/api/users", adminToken, "198.51.100.1", nil)
	var usersBody struct {
		Users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"users"`
	}
	json.Unmarshal(resp.Body.Bytes(), &usersBody)
	for _, u := range usersBody.Users {
		if u.Username == "admin" {
			superID = u.ID
		}
	}
	if superID == "" {
		t.Fatal("超级管理员未找到")
	}

	// 目标管理员与超管都有"把关人"占用（活跃流程引用）。
	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "guard-order-flow", Name: "顺序守卫流程", CreatedAt: now,
		Rounds:           []domain.RoundConfig{{RoundNumber: 1, Name: "一审", ExpertIDs: []string{delegatedID}, RequiredCount: 1}},
		FinalReviewerIDs: []string{targetID, superID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveQuestion(ctx, domain.A2Question{ID: "guard-order-question", OwnerID: superID, Status: domain.StatusReviewing, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(ctx, domain.ReviewTask{ID: "guard-order-task", QuestionID: "guard-order-question", FlowID: "guard-order-flow", Status: domain.StatusReviewing, CurrentRound: 1, AssignedTo: []string{delegatedID}, FinalReviewerIDs: []string{targetID, superID}, QuestionVersion: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// 越权修改另一管理员：先得 403（资格），不得返回 409 占用细节。
	code, body := guardUpdateUser(t, h, delegatedToken, targetID, map[string]any{
		"display_name": "占用管理员", "role": "teacher", "roles": []string{"teacher"},
		"permissions": []string{}, "bank_ids": []string{}})
	if code != http.StatusForbidden {
		t.Fatalf("越权修改另一管理员应 403，实得 %d %s", code, body)
	}

	// 越权修改/删除超管：403（受保护），同样不得泄露占用信息。
	code, body = guardUpdateUser(t, h, delegatedToken, superID, map[string]any{
		"display_name": "x", "role": "teacher", "roles": []string{"teacher"},
		"permissions": []string{}, "bank_ids": []string{}})
	if code != http.StatusForbidden {
		t.Fatalf("越权修改超管应 403，实得 %d %s", code, body)
	}
	resp = serveAuthJSON(t, h, http.MethodDelete, "/api/users/"+superID, delegatedToken, "198.51.100.2", nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("越权删除超管应 403，实得 %d %s", resp.Code, resp.Body.String())
	}

	// 有权的超级管理员对同一占用目标：仍是 409 占用拦截（守卫未回退）。
	code, body = guardUpdateUser(t, h, adminToken, targetID, map[string]any{
		"display_name": "占用管理员", "role": "teacher", "roles": []string{"teacher"},
		"permissions": []string{}, "bank_ids": []string{}})
	if code != http.StatusConflict {
		t.Fatalf("超级管理员收占用中管理员权限应 409，实得 %d %s", code, body)
	}
}
