// 管理员重置用户密码验收测试（修复与待办指引.txt 第一批第 3 项）：
// user:manage 管理员可重置并拿到一次性初始口令；目标账号用新口令可登录；
// 审计留痕；无 user:manage 的账号 403；短口令 400；受保护账号拒绝。
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAdminResetUserPassword(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "reset-target", "password": "original-pass-1", "display_name": "重置对象", "role": "teacher",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create user status=%d body=%s", created.Code, created.Body.String())
	}
	var createdUser struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdUser); err != nil || createdUser.ID == "" {
		t.Fatalf("decode created user: %v body=%s", err, created.Body.String())
	}

	// 随机初始口令重置：返回一次性口令，旧口令失效，新口令可登录
	reset := serveAuthJSON(t, handler, http.MethodPost, "/api/users/"+createdUser.ID+"/reset-password", adminToken, "198.51.100.1", map[string]any{})
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	var granted struct {
		Username    string `json:"username"`
		NewPassword string `json:"new_password"`
	}
	if err := json.Unmarshal(reset.Body.Bytes(), &granted); err != nil || len(granted.NewPassword) < 8 {
		t.Fatalf("decode reset response: %+v err=%v", granted, err)
	}
	if resp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "198.51.100.3", map[string]any{"username": "reset-target", "password": "original-pass-1"}); resp.Code != http.StatusUnauthorized {
		t.Fatalf("old password should stop working, got %d", resp.Code)
	}
	loginForAuthTest(t, handler, "reset-target", granted.NewPassword, "198.51.100.3")

	// 审计留痕：user_reset_password
	logs, err := store.ListLogs(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range logs {
		if entry.Action == "user_reset_password" && strings.Contains(entry.Detail, "reset-target") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit log missing user_reset_password entry: %+v", logs)
	}

	// 短口令 400
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/users/"+createdUser.ID+"/reset-password", adminToken, "198.51.100.1", map[string]any{"new_password": "short"}); got.Code != http.StatusBadRequest {
		t.Fatalf("short password should 400, got %d body=%s", got.Code, got.Body.String())
	}

	// 无 user:manage 的账号 403
	teacherToken := loginForAuthTest(t, handler, "reset-target", granted.NewPassword, "198.51.100.3")
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/users/"+createdUser.ID+"/reset-password", teacherToken, "198.51.100.3", map[string]any{}); got.Code != http.StatusForbidden {
		t.Fatalf("non-admin reset should 403, got %d", got.Code)
	}

	// 受保护账号（超级管理员）拒绝
	adminUser, err := server.authSvc.GetUserByUsername("admin")
	if err != nil || adminUser == nil {
		t.Fatalf("load admin: %v", err)
	}
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/users/"+adminUser.ID+"/reset-password", adminToken, "198.51.100.1", map[string]any{}); got.Code != http.StatusForbidden {
		t.Fatalf("protected account reset should 403, got %d body=%s", got.Code, got.Body.String())
	}
}
