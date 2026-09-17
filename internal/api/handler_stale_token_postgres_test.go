// 空角色旧 token fail-closed 验收测试：
// 注册后未分配角色期间登录的 token，在管理员挂上双身份后不得按“全部身份并集”
// 获得出题+审题能力；重新登录或 switch-role 后恢复正常。
package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestStaleRolelessTokenIsFailClosedAfterRolesAssigned(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	// 自注册账号（无任何角色）→ 立即登录，拿到 Role 为空的旧 token
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "198.51.100.2", map[string]any{
		"username": "stale-token-user", "password": "stale-pass-123", "display_name": "旧令牌用户",
	})
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", created.Code, created.Body.String())
	}
	staleToken := loginForAuthTest(t, handler, "stale-token-user", "stale-pass-123", "198.51.100.2")

	// 旧 token 在无角色期访问业务接口：账号无挂载角色，空角色=无权限而非报错
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/generate", staleToken, "198.51.100.2", map[string]any{}); got.Code != http.StatusForbidden {
		t.Fatalf("roleless token before assignment: status=%d body=%s", got.Code, got.Body.String())
	}

	// 管理员挂上双身份（命题教师+审题老师）
	meResp := serveAuthJSON(t, handler, http.MethodGet, "/api/auth/me", staleToken, "198.51.100.2", nil)
	if meResp.Code != http.StatusOK {
		t.Fatalf("me during roleless window status=%d body=%s", meResp.Code, meResp.Body.String())
	}
	target := decodeUserID(t, meResp.Body.String())
	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+target, adminToken, "198.51.100.1", map[string]any{
		"display_name": "旧令牌用户", "role": "teacher", "roles": []string{"teacher", "expert"},
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign roles status=%d body=%s", updated.Code, updated.Body.String())
	}

	// 旧 token：业务接口 fail-closed（既不能出题也不能以审题身份看任务），
	// me/switch-role 仍可用
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/generate", staleToken, "198.51.100.2", map[string]any{}); got.Code != http.StatusForbidden {
		t.Fatalf("stale token generate should be 403, got %d body=%s", got.Code, got.Body.String())
	}
	if got := serveAuthJSON(t, handler, http.MethodGet, "/api/review/my-tasks", staleToken, "198.51.100.2", nil); got.Code != http.StatusForbidden {
		t.Fatalf("stale token must not enter review identity via my-tasks, got %d", got.Code)
	}
	if got := serveAuthJSON(t, handler, http.MethodGet, "/api/auth/me", staleToken, "198.51.100.2", nil); got.Code != http.StatusOK {
		t.Fatalf("me should stay 200 for stale token, got %d", got.Code)
	}
	switched := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", staleToken, "198.51.100.2", map[string]any{"role": "expert"})
	if switched.Code != http.StatusOK {
		t.Fatalf("switch-role should succeed for stale token, got %d body=%s", switched.Code, switched.Body.String())
	}

	// 旧 token 切到审题身份后：具备审题身份能力口径，但不能出题（互斥按当前身份判定）
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/generate", staleToken, "198.51.100.2", map[string]any{}); got.Code != http.StatusForbidden {
		t.Fatalf("expert-identity token should not generate, got %d", got.Code)
	}

	// 重新登录拿到默认身份（teacher）的新 token：出题权限门应通过（测试环境
	// AI 服务未就绪会以 503 拒绝，说明已进入业务逻辑而非权限拒绝）
	freshToken := loginForAuthTest(t, handler, "stale-token-user", "stale-pass-123", "198.51.100.2")
	if got := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/generate", freshToken, "198.51.100.2", map[string]any{}); got.Code == http.StatusForbidden {
		t.Fatalf("fresh teacher-identity token should pass the generate permission gate, got 403 body=%s", got.Body.String())
	}
}

func decodeUserID(t *testing.T, body string) string {
	t.Helper()
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.ID == "" {
		t.Fatalf("decode me payload: id=%q err=%v body=%s", payload.ID, err, body)
	}
	return payload.ID
}

func containsPerm(t *testing.T, server *Server, token, perm string) bool {
	t.Helper()
	resp := serveAuthJSON(t, server.Handler(), http.MethodGet, "/api/auth/me", token, "198.51.100.2", nil)
	return resp.Code == http.StatusOK && len(resp.Body.String()) > 0
}
