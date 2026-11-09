package api

// 跨身份待办提醒与账号级权限视图回归（PG 集成）：
//  1. 登录 / me / 切换身份响应携带 account_permissions（全身份权限并集）与
//     role_permissions（每个已挂身份的有效权限）；当前身份 permissions 保持隔离。
//     前端跨身份提醒完全基于权限点判定，不依赖内置角色 ID（模板可删建）。
//  2. 剥离规则不因并集视图失效：role:manage 不出现在任何非超管视图；
//     generation_quota:manage 仅超级管理员可用，普通管理员多身份并集也不可携带。
//  3. /api/my/summary 的账号级待办计数（待审核 / 待决断 / 待本人修改）与当前
//     工作身份无关：审题身份下也能看到名下退修题等，供仪表盘跨身份提醒引导切换。

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/review"
)

type crossIdentityUserView struct {
	ID                 string              `json:"id"`
	Username           string              `json:"username"`
	Role               string              `json:"role"`
	Roles              []string            `json:"roles"`
	Permissions        []string            `json:"permissions"`
	AccountPermissions []string            `json:"account_permissions"`
	RolePermissions    map[string][]string `json:"role_permissions"`
}

type crossIdentityUserPayload struct {
	Token string                `json:"token"`
	User  crossIdentityUserView `json:"user"`
}

func containsStr(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func TestCrossIdentityPermissionViewOnLoginAndSwitch(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "cross-duo", "password": "cross-duo-pass", "display_name": "跨身份视图",
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registration.Code, registration.Body.String())
	}
	var registered struct {
		ID string `json:"id"`
	}
	json.Unmarshal(registration.Body.Bytes(), &registered)
	assigned := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "跨身份视图", "role": "expert", "roles": []string{"expert", "teacher"},
		"permissions": []string{}, "bank_ids": []string{}, "enabled": true,
	})
	if assigned.Code != http.StatusOK {
		t.Fatalf("assign roles status=%d body=%s", assigned.Code, assigned.Body.String())
	}

	login := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "203.0.113.10", map[string]any{
		"username": "cross-duo", "password": "cross-duo-pass",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d", login.Code)
	}
	var logged crossIdentityUserPayload
	if err := json.Unmarshal(login.Body.Bytes(), &logged); err != nil {
		t.Fatal(err)
	}
	if logged.User.Role != "teacher" || !containsStr(logged.User.Permissions, "question:generate") || containsStr(logged.User.Permissions, "review:do") {
		t.Fatalf("fresh dual login should enter isolated teacher identity: %+v", logged.User)
	}
	// The preferred login identity does not remove the review identity. Enter
	// expert explicitly before exercising the original expert permission branch.
	expertResp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", logged.Token, "203.0.113.10", map[string]any{"role": "expert"})
	if expertResp.Code != http.StatusOK {
		t.Fatalf("switch to expert status=%d body=%s", expertResp.Code, expertResp.Body)
	}
	if err := json.Unmarshal(expertResp.Body.Bytes(), &logged); err != nil || logged.User.Role != "expert" {
		t.Fatalf("explicit expert switch failed: role=%s error=%v", logged.User.Role, err)
	}
	u := logged.User

	// 当前身份（expert）隔离：有审题无出题。
	if !containsStr(u.Permissions, "review:do") || containsStr(u.Permissions, "question:generate") {
		t.Fatalf("expert identity permissions should isolate: %v", u.Permissions)
	}
	// 账号并集：两个身份的权限都在。
	for _, perm := range []string{"review:do", "question:generate", "review:submit", "review:view_results"} {
		if !containsStr(u.AccountPermissions, perm) {
			t.Fatalf("account_permissions missing %s: %v", perm, u.AccountPermissions)
		}
	}
	// 各身份有效权限（前端"哪个身份能做什么"的判定依据）。
	if !containsStr(u.RolePermissions["expert"], "review:do") || containsStr(u.RolePermissions["expert"], "question:generate") {
		t.Fatalf("role_permissions[expert] wrong: %v", u.RolePermissions["expert"])
	}
	if !containsStr(u.RolePermissions["teacher"], "question:generate") || containsStr(u.RolePermissions["teacher"], "review:do") {
		t.Fatalf("role_permissions[teacher] wrong: %v", u.RolePermissions["teacher"])
	}

	// 切换身份后同样携带完整视图，且当前身份随切换变化。
	switchResp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", logged.Token, "203.0.113.10", map[string]any{
		"role": "teacher",
	})
	if switchResp.Code != http.StatusOK {
		t.Fatalf("switch status=%d body=%s", switchResp.Code, switchResp.Body.String())
	}
	var switched crossIdentityUserPayload
	if err := json.Unmarshal(switchResp.Body.Bytes(), &switched); err != nil {
		t.Fatal(err)
	}
	if switched.User.Role != "teacher" || !containsStr(switched.User.Permissions, "question:generate") || containsStr(switched.User.Permissions, "review:do") {
		t.Fatalf("teacher identity not isolated after switch: role=%s perms=%v", switched.User.Role, switched.User.Permissions)
	}
	if !containsStr(switched.User.AccountPermissions, "review:do") || !containsStr(switched.User.RolePermissions["expert"], "review:do") {
		t.Fatalf("switch response lost account view: %v / %v", switched.User.AccountPermissions, switched.User.RolePermissions)
	}

	// me 响应（GetUserByIDForRole 路径，裸 user 对象）同样携带账号级视图。
	me := serveAuthJSON(t, handler, http.MethodGet, "/api/auth/me", switched.Token, "203.0.113.10", nil)
	if me.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}
	var meUser crossIdentityUserView
	if err := json.Unmarshal(me.Body.Bytes(), &meUser); err != nil {
		t.Fatal(err)
	}
	if !containsStr(meUser.AccountPermissions, "review:do") || !containsStr(meUser.RolePermissions["teacher"], "question:generate") {
		t.Fatalf("me response lost account view: %v / %v", meUser.AccountPermissions, meUser.RolePermissions)
	}
}

func TestCrossIdentityPermissionStrippingRules(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "cross-admin-duo", "password": "cross-admin-pass", "display_name": "管理员双身份",
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("register status=%d", registration.Code)
	}
	var registered struct {
		ID string `json:"id"`
	}
	json.Unmarshal(registration.Body.Bytes(), &registered)
	assigned := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "管理员双身份", "role": "admin", "roles": []string{"admin", "expert"},
		"permissions": []string{}, "bank_ids": []string{}, "enabled": true,
	})
	if assigned.Code != http.StatusOK {
		t.Fatalf("assign roles status=%d body=%s", assigned.Code, assigned.Body.String())
	}
	login := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "203.0.113.10", map[string]any{
		"username": "cross-admin-duo", "password": "cross-admin-pass",
	})
	var logged crossIdentityUserPayload
	json.Unmarshal(login.Body.Bytes(), &logged)
	u := logged.User

	// role:manage 任何非超管视图都不可见（并集也不行）。
	if containsStr(u.AccountPermissions, "role:manage") || containsStr(u.RolePermissions["admin"], "role:manage") {
		t.Fatalf("role:manage must never appear for non-super-admin: account=%v admin=%v", u.AccountPermissions, u.RolePermissions["admin"])
	}
	// 普通管理员与审题身份、账号并集均不可携带配额管理权限。
	for label, permissions := range map[string][]string{"admin": u.RolePermissions["admin"], "expert": u.RolePermissions["expert"], "account": u.AccountPermissions} {
		if containsStr(permissions, "generation_quota:manage") {
			t.Fatalf("%s must not inherit super-admin quota permission: %v", label, permissions)
		}
	}
}

func TestMySummaryTodoCountsAreIdentityIndependent(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	server.reviewSvc = review.NewService(store, store, store, server.authSvc)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	ctx := context.Background()
	now := time.Now()

	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "cross-todo", "password": "cross-todo-pass", "display_name": "跨身份待办",
	})
	var registered struct {
		ID string `json:"id"`
	}
	json.Unmarshal(registration.Body.Bytes(), &registered)
	assigned := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "跨身份待办", "role": "expert", "roles": []string{"expert", "teacher"},
		// 内置 expert/teacher 模板都不含 review:final；把关人账号需账号级持有
		// 决断权限（与 handleMyDecisions 的 hasFinalRight 门控同口径）。
		"permissions": []string{"review:final"}, "bank_ids": []string{}, "enabled": true,
	})
	if assigned.Code != http.StatusOK {
		t.Fatalf("assign roles status=%d body=%s", assigned.Code, assigned.Body.String())
	}
	duo, err := server.authSvc.GetUserByUsername("cross-todo")
	if err != nil || duo == nil {
		t.Fatalf("load duo user: %v", err)
	}

	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "cross-flow", Name: "跨身份流程", CreatedAt: now,
		Rounds:           []domain.RoundConfig{{RoundNumber: 1, Name: "一审", ExpertIDs: []string{duo.ID}, RequiredCount: 1}},
		FinalReviewerIDs: []string{duo.ID},
	}); err != nil {
		t.Fatal(err)
	}
	saveQuestion := func(id string, status domain.QuestionStatus) {
		t.Helper()
		if err := store.SaveQuestion(ctx, domain.A2Question{
			ID: id, Status: status, OwnerID: duo.ID, CreatedBy: "cross-todo", Version: 1,
			ClinicalStem: "跨身份待办-" + id, Answer: "A", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	saveQuestion("cross-q-revision", domain.StatusAIReviewed)
	saveQuestion("cross-q-reviewing", domain.StatusAIReviewed)
	saveQuestion("cross-q-conflict", domain.StatusAIReviewed)
	// 退修：任务 revision_required（属主为 duo）。
	if err := store.SaveTask(ctx, domain.ReviewTask{
		ID: "cross-task-revision", QuestionID: "cross-q-revision", FlowID: "cross-flow",
		Status: domain.StatusRevisionRequired, CurrentRound: 1, QuestionVersion: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// 审核中：分配给 duo、未投票。
	if err := store.SaveTask(ctx, domain.ReviewTask{
		ID: "cross-task-review", QuestionID: "cross-q-reviewing", FlowID: "cross-flow",
		Status: domain.StatusReviewing, CurrentRound: 1, AssignedTo: []string{duo.ID}, QuestionVersion: 1,
		RoundResults: []domain.RoundResult{{RoundNumber: 1}}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// 待决断：全部轮通过、duo 在把关人名单。
	if err := store.SaveTask(ctx, domain.ReviewTask{
		ID: "cross-task-decision", QuestionID: "cross-q-conflict", FlowID: "cross-flow",
		Status: domain.StatusConflict, CurrentRound: 1, FinalReviewerIDs: []string{duo.ID}, QuestionVersion: 1,
		RoundResults: []domain.RoundResult{{RoundNumber: 1, Passed: true}}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	fetchSummary := func(token string) map[string]int {
		t.Helper()
		resp := serveAuthJSON(t, handler, http.MethodGet, "/api/my/summary", token, "203.0.113.10", nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("my/summary status=%d body=%s", resp.Code, resp.Body.String())
		}
		var payload map[string]int
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	login := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "203.0.113.10", map[string]any{
		"username": "cross-todo", "password": "cross-todo-pass",
	})
	var logged crossIdentityUserPayload
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &logged) != nil {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body)
	}
	if logged.User.Role != "teacher" || !containsStr(logged.User.Permissions, "question:generate") || containsStr(logged.User.Permissions, "review:do") {
		t.Fatalf("fresh dual login should enter teacher: %+v", logged.User)
	}
	expertResp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", logged.Token, "203.0.113.10", map[string]any{"role": "expert"})
	if expertResp.Code != http.StatusOK || json.Unmarshal(expertResp.Body.Bytes(), &logged) != nil || logged.User.Role != "expert" {
		t.Fatalf("explicit expert identity status=%d body=%s", expertResp.Code, expertResp.Body)
	}

	expertSummary := fetchSummary(logged.Token)
	for key, want := range map[string]int{
		"pending_review_count":   1,
		"pending_decision_count": 1,
		"pending_revision_count": 1,
	} {
		if expertSummary[key] != want {
			t.Fatalf("expert identity summary[%s]=%d, want %d: %v", key, expertSummary[key], want, expertSummary)
		}
	}

	switchResp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", logged.Token, "203.0.113.10", map[string]any{"role": "teacher"})
	var switched crossIdentityUserPayload
	json.Unmarshal(switchResp.Body.Bytes(), &switched)
	teacherSummary := fetchSummary(switched.Token)
	for key, want := range map[string]int{
		"pending_review_count":   1,
		"pending_decision_count": 1,
		"pending_revision_count": 1,
	} {
		if teacherSummary[key] != want {
			t.Fatalf("teacher identity summary[%s]=%d, want %d（账号级待办不得随身份变化）: %v", key, teacherSummary[key], want, teacherSummary)
		}
		if teacherSummary[key] != expertSummary[key] {
			t.Fatalf("summary[%s] differs by identity: expert=%d teacher=%d", key, expertSummary[key], teacherSummary[key])
		}
	}
}
