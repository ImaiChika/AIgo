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
	"strings"
	"testing"
	"time"

	"aigo/internal/audit"
	"aigo/internal/auth"
	"aigo/internal/domain"
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
