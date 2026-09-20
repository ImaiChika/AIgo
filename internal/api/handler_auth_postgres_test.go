package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"aigo/internal/audit"
	"aigo/internal/auth"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage/postgres"

	"github.com/lib/pq"
)

func TestRegistrationVisibilityPermissionAssignmentAndLoginThrottling(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()

	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "new-low-user", "password": "new-user-password", "display_name": "新注册低权限用户",
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("registration status=%d body=%s", registration.Code, registration.Body.String())
	}
	var registered auth.User
	if err := json.Unmarshal(registration.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	if registered.Role != "" || len(registered.Permissions) != 0 || len(registered.DirectPermissions) != 0 || len(registered.BankIDs) != 0 || !registered.Enabled {
		t.Fatalf("registered user is not minimum privilege: %+v", registered)
	}

	adminList := serveAuthJSON(t, handler, http.MethodGet, "/api/users", adminToken, "198.51.100.1", nil)
	if adminList.Code != http.StatusOK || !strings.Contains(adminList.Body.String(), `"username":"new-low-user"`) {
		t.Fatalf("admin cannot see registered user: status=%d body=%s", adminList.Code, adminList.Body.String())
	}

	userToken := loginForAuthTest(t, handler, "new-low-user", "new-user-password", "203.0.113.10")
	forbidden := serveAuthJSON(t, handler, http.MethodGet, "/api/users", userToken, "203.0.113.10", nil)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("minimum privilege user accessed user management: status=%d", forbidden.Code)
	}

	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "新注册低权限用户", "role": "", "permissions": []string{domain.PermUserManage}, "bank_ids": []string{}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("admin permission assignment failed: status=%d body=%s", updated.Code, updated.Body.String())
	}
	allowed := serveAuthJSON(t, handler, http.MethodGet, "/api/users", userToken, "203.0.113.10", nil)
	if allowed.Code != http.StatusOK {
		t.Fatalf("permission change did not take effect for existing token: status=%d body=%s", allowed.Code, allowed.Body.String())
	}

	for attempt := 1; attempt <= 8; attempt++ {
		response := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "192.0.2.88", map[string]any{
			"username": "new-low-user", "password": "wrong-password",
		})
		if attempt < 8 {
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "用户名或密码错误") {
				t.Fatalf("attempt %d status=%d body=%s", attempt, response.Code, response.Body.String())
			}
		} else if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
			t.Fatalf("threshold attempt status=%d retry=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
		}
	}
	blocked := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "192.0.2.88", map[string]any{
		"username": "new-low-user", "password": "wrong-password",
	})
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("pre-blocked login status=%d", blocked.Code)
	}

	var failedCount, limitedCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='auth_login_failed'`).Scan(&failedCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='auth_rate_limited'`).Scan(&limitedCount); err != nil {
		t.Fatal(err)
	}
	if failedCount != 7 || limitedCount != 1 {
		t.Fatalf("unexpected auth audit counts: failed=%d limited=%d", failedCount, limitedCount)
	}
	var detail string
	if err := store.DB().QueryRow(`SELECT detail FROM audit_logs WHERE action='auth_login_failed' LIMIT 1`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, "new-low-user") || strings.Contains(detail, "192.0.2.88") || !strings.Contains(detail, "account=") || !strings.Contains(detail, "source=") {
		t.Fatalf("authentication audit is not pseudonymized: %q", detail)
	}
}

func TestDelegatedAdminCannotManageRolesOrProtectedAccounts(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	superToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.2")

	createdAdmin := serveAuthJSON(t, handler, http.MethodPost, "/api/users", superToken, "198.51.100.2", map[string]any{
		"username": "delegated-http-admin", "password": "delegated-password", "display_name": "业务管理员", "role": domain.RoleAdmin,
	})
	if createdAdmin.Code != http.StatusCreated {
		t.Fatalf("super admin should be able to assign admin role: status=%d body=%s", createdAdmin.Code, createdAdmin.Body.String())
	}
	var delegated auth.User
	if err := json.Unmarshal(createdAdmin.Body.Bytes(), &delegated); err != nil {
		t.Fatal(err)
	}
	delegatedToken := loginForAuthTest(t, handler, delegated.Username, "delegated-password", "198.51.100.3")

	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/roles", delegatedToken, "198.51.100.3", nil); response.Code != http.StatusOK {
		t.Fatalf("delegated admin should read roles for user assignment: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodPost, "/api/roles", delegatedToken, "198.51.100.3", map[string]any{
		"id": "http-forbidden-role", "name": "越权角色", "permissions": []string{domain.PermQuestionView},
	}); response.Code != http.StatusForbidden {
		t.Fatalf("delegated admin should not create roles: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodPost, "/api/users", delegatedToken, "198.51.100.3", map[string]any{
		"username": "http-forbidden-super", "password": "forbidden-password", "display_name": "越权", "role": domain.RoleSuperAdmin,
	}); response.Code != http.StatusForbidden {
		t.Fatalf("delegated admin should not create a second super admin: status=%d body=%s", response.Code, response.Body.String())
	}

	createdExpert := serveAuthJSON(t, handler, http.MethodPost, "/api/users", delegatedToken, "198.51.100.3", map[string]any{
		"username": "http-deletable-expert", "password": "expert-password", "display_name": "审核老师", "role": domain.RoleExpert,
	})
	if createdExpert.Code != http.StatusCreated {
		t.Fatalf("delegated admin should create ordinary expert: status=%d body=%s", createdExpert.Code, createdExpert.Body.String())
	}
	var expert auth.User
	if err := json.Unmarshal(createdExpert.Body.Bytes(), &expert); err != nil {
		t.Fatal(err)
	}
	if response := serveAuthJSON(t, handler, http.MethodDelete, "/api/users/"+expert.ID, delegatedToken, "198.51.100.3", nil); response.Code != http.StatusOK {
		t.Fatalf("delegated admin should delete ordinary users: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSingleAndBatchGenerationPermissionsAreIndependent(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.batchSvc = batch.NewUnavailableExecutor("disabled", "test batch executor")
	handler := server.Handler()
	superToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.30")

	createdAdmin := serveAuthJSON(t, handler, http.MethodPost, "/api/users", superToken, "198.51.100.30", map[string]any{
		"username": "separate-permission-admin", "password": "separate-admin-password", "display_name": "普通管理员", "role": domain.RoleAdmin,
	})
	if createdAdmin.Code != http.StatusCreated {
		t.Fatalf("create delegated admin: status=%d body=%s", createdAdmin.Code, createdAdmin.Body.String())
	}
	delegatedAdminToken := loginForAuthTest(t, handler, "separate-permission-admin", "separate-admin-password", "198.51.100.31")
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/batch/capabilities", delegatedAdminToken, "198.51.100.31", nil); response.Code != http.StatusForbidden {
		t.Fatalf("delegated admin should not have default batch permission: status=%d body=%s", response.Code, response.Body.String())
	}

	createdExpert := serveAuthJSON(t, handler, http.MethodPost, "/api/users", superToken, "198.51.100.30", map[string]any{
		"username": "separate-permission-expert", "password": "separate-expert-password", "display_name": "出题专家", "role": domain.RoleExpert,
	})
	if createdExpert.Code != http.StatusCreated {
		t.Fatalf("create expert: status=%d body=%s", createdExpert.Code, createdExpert.Body.String())
	}
	var expert auth.User
	if err := json.Unmarshal(createdExpert.Body.Bytes(), &expert); err != nil {
		t.Fatal(err)
	}
	expertToken := loginForAuthTest(t, handler, expert.Username, "separate-expert-password", "198.51.100.32")
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/batch/capabilities", expertToken, "198.51.100.32", nil); response.Code != http.StatusForbidden {
		t.Fatalf("expert should not have default batch permission: status=%d body=%s", response.Code, response.Body.String())
	}

	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+expert.ID, superToken, "198.51.100.30", map[string]any{
		"display_name": expert.DisplayName, "role": domain.RoleExpert, "permissions": []string{domain.PermBatchRun}, "bank_ids": []string{}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("grant direct batch permission: status=%d body=%s", updated.Code, updated.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/batch/capabilities", expertToken, "198.51.100.32", nil); response.Code != http.StatusOK {
		t.Fatalf("explicit direct batch permission should take effect without relogin: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTeacherAndReviewerIdentitySwitch(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.60")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.60", map[string]any{
		"username": "dual-identity-user", "password": "dual-identity-password", "display_name": "双身份老师",
		"role": domain.RoleTeacher, "roles": []string{domain.RoleTeacher, domain.RoleExpert},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create dual identity user: %d %s", created.Code, created.Body)
	}
	var user auth.User
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil || len(user.Roles) != 2 {
		t.Fatalf("dual identity roles not returned: err=%v user=%+v", err, user)
	}

	login := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "198.51.100.61", map[string]any{
		"username": "dual-identity-user", "password": "dual-identity-password",
	})
	var loginPayload struct {
		Token string    `json:"token"`
		User  auth.User `json:"user"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &loginPayload) != nil {
		t.Fatalf("dual identity login failed: %d %s", login.Code, login.Body)
	}
	if loginPayload.User.Role != domain.RoleTeacher || !slices.Contains(loginPayload.User.Permissions, domain.PermQuestionGenerate) || slices.Contains(loginPayload.User.Permissions, domain.PermReviewDo) {
		t.Fatalf("default teacher identity permissions incorrect: %+v", loginPayload.User)
	}

	switched := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", loginPayload.Token, "198.51.100.61", map[string]any{"role": domain.RoleExpert})
	if switched.Code != http.StatusOK {
		t.Fatalf("switch to reviewer identity failed: %d %s", switched.Code, switched.Body)
	}
	if err := json.Unmarshal(switched.Body.Bytes(), &loginPayload); err != nil || loginPayload.User.Role != domain.RoleExpert || !slices.Contains(loginPayload.User.Permissions, domain.PermReviewDo) || slices.Contains(loginPayload.User.Permissions, domain.PermQuestionGenerate) {
		t.Fatalf("reviewer identity permissions incorrect: err=%v user=%+v", err, loginPayload.User)
	}
	if response := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/generate", loginPayload.Token, "198.51.100.61", map[string]any{"topic": "不应出题"}); response.Code != http.StatusForbidden {
		t.Fatalf("reviewer identity retained authoring access: %d %s", response.Code, response.Body)
	}
}

func TestMySummaryPendingCountMatchesNewQuestionWorkspace(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.70")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.70", map[string]any{
		"username": "summary-teacher", "password": "summary-teacher-password", "display_name": "统计命题老师", "role": domain.RoleTeacher,
	})
	var teacher auth.User
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &teacher) != nil {
		t.Fatalf("create summary teacher: %d %s", created.Code, created.Body)
	}
	now := time.Now()
	for _, id := range []string{"summary-new", "summary-revision"} {
		if err := store.SaveQuestion(t.Context(), domain.A2Question{
			ID: id, OwnerID: teacher.ID, CreatedBy: teacher.Username, ClinicalStem: "患者出现症状，最可能的诊断是？",
			Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}},
			Answer:  "A", Status: domain.StatusAIReviewed, Version: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	flow := domain.ReviewFlowConfig{ID: "summary-flow", Name: "统计流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "审核", ExpertIDs: []string{teacher.ID}, RequiredCount: 1}}, CreatedAt: now}
	if err := store.SaveFlowConfig(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(t.Context(), domain.ReviewTask{
		ID: "summary-task", QuestionID: "summary-revision", FlowID: flow.ID, Status: domain.StatusRevisionRequired,
		CurrentRound: 1, AssignedTo: []string{teacher.ID}, QuestionVersion: 1, RoundResults: []domain.RoundResult{{RoundNumber: 1}}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	teacherToken := loginForAuthTest(t, handler, teacher.Username, "summary-teacher-password", "198.51.100.71")
	summary := serveAuthJSON(t, handler, http.MethodGet, "/api/my/summary", teacherToken, "198.51.100.71", nil)
	var payload struct {
		Generated    int `json:"generated_count"`
		NewQuestions int `json:"new_questions_count"`
	}
	if summary.Code != http.StatusOK || json.Unmarshal(summary.Body.Bytes(), &payload) != nil || payload.Generated != 2 || payload.NewQuestions != 1 {
		t.Fatalf("summary count mismatch: %d %s", summary.Code, summary.Body)
	}
	workspace := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/my-new?page=1&page_size=20", teacherToken, "198.51.100.71", nil)
	var page struct {
		Total int `json:"total"`
	}
	if workspace.Code != http.StatusOK || json.Unmarshal(workspace.Body.Bytes(), &page) != nil || page.Total != payload.NewQuestions {
		t.Fatalf("summary and workspace diverged: summary=%d workspace=%d body=%s", payload.NewQuestions, page.Total, workspace.Body)
	}
}

func TestReferencedReviewerCannotBeDisabledOrDeleted(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.80")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.80", map[string]any{
		"username": "referenced-reviewer", "password": "referenced-reviewer-password", "display_name": "被引用审题老师", "role": domain.RoleExpert,
	})
	var reviewer auth.User
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &reviewer) != nil {
		t.Fatalf("create reviewer: %d %s", created.Code, created.Body)
	}
	if err := store.SaveFlowConfig(t.Context(), domain.ReviewFlowConfig{
		ID: "referenced-reviewer-flow", Name: "引用审题人流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "审核", ExpertIDs: []string{reviewer.ID}, RequiredCount: 1}}, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	disable := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+reviewer.ID, adminToken, "198.51.100.80", map[string]any{
		"display_name": reviewer.DisplayName, "role": reviewer.Role, "roles": reviewer.Roles,
		"permissions": reviewer.DirectPermissions, "bank_ids": reviewer.BankIDs, "enabled": false,
	})
	if disable.Code != http.StatusConflict || !strings.Contains(disable.Body.String(), "仍被审核流程") {
		t.Fatalf("referenced reviewer disable should be blocked: %d %s", disable.Code, disable.Body)
	}
	deleted := serveAuthJSON(t, handler, http.MethodDelete, "/api/users/"+reviewer.ID, adminToken, "198.51.100.80", nil)
	if deleted.Code != http.StatusConflict || !strings.Contains(deleted.Body.String(), "仍被审核流程") {
		t.Fatalf("referenced reviewer deletion should be blocked: %d %s", deleted.Code, deleted.Body)
	}
}

func TestUserWithOwnedQuestionsCannotBeDeleted(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.90")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.90", map[string]any{
		"username": "owned-work-teacher", "password": "owned-work-teacher-password", "display_name": "有遗留题老师", "role": domain.RoleTeacher,
	})
	var teacher auth.User
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &teacher) != nil {
		t.Fatalf("create teacher: %d %s", created.Code, created.Body)
	}
	if err := store.SaveQuestion(t.Context(), domain.A2Question{
		ID: "owned-work-question", OwnerID: teacher.ID, CreatedBy: teacher.Username,
		ClinicalStem: "男，50岁。有遗留题目，最可能的诊断是？",
		Options:      []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}},
		Answer:       "A", Status: domain.StatusAIReviewed, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	deleted := serveAuthJSON(t, handler, http.MethodDelete, "/api/users/"+teacher.ID, adminToken, "198.51.100.90", nil)
	if deleted.Code != http.StatusConflict || !strings.Contains(deleted.Body.String(), "个人题目 1") {
		t.Fatalf("user with owned work should not be deleted: %d %s", deleted.Code, deleted.Body)
	}
}

func authHandlerTestServer(t *testing.T) (*Server, *postgres.Store, func()) {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run auth handler integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := fmt.Sprintf("aigo_auth_handler_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	store, err := postgres.New(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	schemaSQL, err := os.ReadFile("../storage/postgres/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitSchema(string(schemaSQL)); err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(store.DB(), "auth-handler-test-secret", time.Hour)
	if err := authService.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if created, _, err := authService.InitAdmin("admin", "admin-password", "测试管理员"); err != nil || !created {
		t.Fatalf("create admin: created=%v err=%v", created, err)
	}
	server := &Server{
		authSvc: authService, auditSvc: audit.NewService(store), questionStore: store, shareStore: store,
		reviewSvc:       review.NewService(store, store, store, authService),
		registerEnabled: true, trustProxyHeaders: true, readinessTimeout: 2 * time.Second,
	}
	cleanup := func() {
		_ = store.Close()
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)
		_ = adminDB.Close()
	}
	return server, store, cleanup
}

func loginForAuthTest(t *testing.T, handler http.Handler, username, password, clientIP string) string {
	t.Helper()
	response := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", clientIP, map[string]any{"username": username, "password": password})
	if response.Code != http.StatusOK {
		t.Fatalf("login %s status=%d body=%s", username, response.Code, response.Body.String())
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Token == "" {
		t.Fatalf("parse login response: token=%q err=%v", payload.Token, err)
	}
	return payload.Token
}

func serveAuthJSON(t *testing.T, handler http.Handler, method, path, token, clientIP string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.RemoteAddr = "10.0.0.2:12345"
	request.Header.Set("X-AIgo-Client-IP", clientIP)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// 登录成功必须在审计日志留痕（含管理员自身），且登录/me 响应要携带
// role_names，否则挂自定义角色的账号在界面只能看到 role-<时间戳> 原始 ID。
func TestLoginAuditLogAndCustomRoleNames(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()

	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	createdRole := serveAuthJSON(t, handler, http.MethodPost, "/api/roles", adminToken, "198.51.100.1", map[string]any{
		"name": "访客测试角色", "description": "登录日志与角色名集成测试", "permissions": []string{domain.PermQuestionView},
	})
	if createdRole.Code != http.StatusCreated {
		t.Fatalf("create role status=%d body=%s", createdRole.Code, createdRole.Body.String())
	}
	var role domain.Role
	if err := json.Unmarshal(createdRole.Body.Bytes(), &role); err != nil || role.ID == "" {
		t.Fatalf("parse role: %+v err=%v", role, err)
	}
	if !strings.HasPrefix(role.ID, "role-") {
		t.Fatalf("auto role id should look like role-<timestamp>, got %q", role.ID)
	}

	createdUser := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "guest-login-test", "password": "guest-password-1", "display_name": "访客登录测试", "role": role.ID,
	})
	if createdUser.Code != http.StatusCreated {
		t.Fatalf("create user status=%d body=%s", createdUser.Code, createdUser.Body.String())
	}

	login := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "203.0.113.20", map[string]any{"username": "guest-login-test", "password": "guest-password-1"})
	if login.Code != http.StatusOK {
		t.Fatalf("guest login status=%d body=%s", login.Code, login.Body.String())
	}
	var loginPayload struct {
		Token string    `json:"token"`
		User  auth.User `json:"user"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginPayload); err != nil {
		t.Fatal(err)
	}
	if loginPayload.User.Role != role.ID {
		t.Fatalf("guest role=%q want %q", loginPayload.User.Role, role.ID)
	}
	if name := loginPayload.User.RoleNames[role.ID]; name != "访客测试角色" {
		t.Fatalf("login role_names[%s]=%q want 访客测试角色", role.ID, name)
	}

	// 管理员登录响应同样要带内置角色的显示名。
	adminLogin := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "198.51.100.1", map[string]any{"username": "admin", "password": "admin-password"})
	if adminLogin.Code != http.StatusOK {
		t.Fatalf("admin login status=%d body=%s", adminLogin.Code, adminLogin.Body.String())
	}
	var adminPayload struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(adminLogin.Body.Bytes(), &adminPayload); err != nil {
		t.Fatal(err)
	}
	if name := adminPayload.User.RoleNames[domain.RoleSuperAdmin]; name != "超级管理员" {
		t.Fatalf("admin role_names[%s]=%q want 超级管理员", domain.RoleSuperAdmin, name)
	}

	me := serveAuthJSON(t, handler, http.MethodGet, "/api/auth/me", loginPayload.Token, "203.0.113.20", nil)
	if me.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}
	var meUser auth.User
	if err := json.Unmarshal(me.Body.Bytes(), &meUser); err != nil {
		t.Fatal(err)
	}
	if meUser.RoleNames[role.ID] != "访客测试角色" {
		t.Fatalf("me role_names[%s]=%q want 访客测试角色", role.ID, meUser.RoleNames[role.ID])
	}

	logs := serveAuthJSON(t, handler, http.MethodGet, "/api/audit-logs?limit=50", adminToken, "198.51.100.1", nil)
	if logs.Code != http.StatusOK {
		t.Fatalf("audit logs status=%d body=%s", logs.Code, logs.Body.String())
	}
	var logPayload struct {
		Logs []domain.AuditLog `json:"logs"`
	}
	if err := json.Unmarshal(logs.Body.Bytes(), &logPayload); err != nil {
		t.Fatal(err)
	}
	sawAdmin, sawGuest := false, false
	for _, entry := range logPayload.Logs {
		if entry.Action != "auth_login" {
			continue
		}
		if entry.Actor == "admin" {
			sawAdmin = true
		}
		if entry.Actor == "guest-login-test" && strings.Contains(entry.Detail, "访客测试角色") && strings.Contains(entry.Detail, "source=") {
			sawGuest = true
		}
	}
	if !sawAdmin {
		t.Fatal("admin's own successful login is missing from audit logs")
	}
	if !sawGuest {
		t.Fatal("guest's successful login (with role name and hashed source) is missing from audit logs")
	}
}
