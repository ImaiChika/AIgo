package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 审计日志筛选与行为统计接口：
// 1. 行为/操作人/题目 组合筛选 + 分页；
// 2. 行为统计端点返回库里实际出现的行为；
// 3. 无全局查看权限的用户 actor 被强制为本人；
// 4. 新增的操作行为留痕（专家库增删改、修改密码、更新资料）。
func TestAuditLogFilterAndActions(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	admin := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.9")

	// 触发几类行为：专家增改删 + 修改资料 + 修改密码
	created := serveAuthJSON(t, handler, "POST", "/api/experts", admin, "198.51.100.9",
		map[string]any{"name": "审计测试专家", "department": "内科", "title": "主任医师"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create expert: %d %s", created.Code, created.Body.String())
	}
	var expert struct {
		ID string `json:"id"`
	}
	json.Unmarshal(created.Body.Bytes(), &expert)
	updated := serveAuthJSON(t, handler, "PUT", "/api/experts/"+expert.ID, admin, "198.51.100.9",
		map[string]any{"title": "副主任医师"})
	if updated.Code != http.StatusOK {
		t.Fatalf("update expert: %d %s", updated.Code, updated.Body.String())
	}
	profile := serveAuthJSON(t, handler, "PUT", "/api/auth/profile", admin, "198.51.100.9",
		map[string]any{"display_name": "审计管理员"})
	if profile.Code != http.StatusOK {
		t.Fatalf("update profile: %d %s", profile.Code, profile.Body.String())
	}
	password := serveAuthJSON(t, handler, "POST", "/api/auth/change-password", admin, "198.51.100.9",
		map[string]any{"old_password": "admin-password", "new_password": "admin-password-2"})
	if password.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", password.Code, password.Body.String())
	}
	defer func() {
		// 还原密码供其他用例/排查使用
		restore := serveAuthJSON(t, handler, "POST", "/api/auth/change-password", loginForAuthTest(t, handler, "admin", "admin-password-2", "198.51.100.9"), "198.51.100.9",
			map[string]any{"old_password": "admin-password-2", "new_password": "admin-password"})
		if restore.Code != http.StatusOK {
			t.Logf("restore admin password: %d %s", restore.Code, restore.Body.String())
		}
	}()

	// 行为筛选：只看专家更新
	filtered := serveAuthJSON(t, handler, "GET", "/api/audit-logs?action=expert_update&limit=10", admin, "198.51.100.9", nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("filter logs: %d %s", filtered.Code, filtered.Body.String())
	}
	var page struct {
		Logs  []map[string]any `json:"logs"`
		Total int              `json:"total"`
	}
	json.Unmarshal(filtered.Body.Bytes(), &page)
	if page.Total != 1 || len(page.Logs) != 1 || page.Logs[0]["action"] != "expert_update" {
		t.Fatalf("action filter result: total=%d logs=%v", page.Total, page.Logs)
	}

	// 组合筛选：行为 + 操作人（admin）仍应命中，题目维度为空不生效
	combo := serveAuthJSON(t, handler, "GET", "/api/audit-logs?action=expert_update&actor=admin", admin, "198.51.100.9", nil)
	json.Unmarshal(combo.Body.Bytes(), &page)
	if page.Total != 1 {
		t.Fatalf("combo filter result: total=%d", page.Total)
	}
	combo = serveAuthJSON(t, handler, "GET", "/api/audit-logs?action=expert_update&actor=nobody", admin, "198.51.100.9", nil)
	json.Unmarshal(combo.Body.Bytes(), &page)
	if page.Total != 0 {
		t.Fatalf("combo filter should be empty: total=%d", page.Total)
	}

	// 行为统计端点：包含新行为，且按次数降序
	actionsResp := serveAuthJSON(t, handler, "GET", "/api/audit-logs/actions", admin, "198.51.100.9", nil)
	if actionsResp.Code != http.StatusOK {
		t.Fatalf("list actions: %d %s", actionsResp.Code, actionsResp.Body.String())
	}
	var actions struct {
		Actions []struct {
			Action string `json:"action"`
			Count  int    `json:"count"`
		} `json:"actions"`
	}
	json.Unmarshal(actionsResp.Body.Bytes(), &actions)
	found := map[string]int{}
	for _, a := range actions.Actions {
		found[a.Action] = a.Count
	}
	for _, want := range []string{"auth_login", "expert_create", "expert_update", "auth_change_password", "auth_update_profile"} {
		if found[want] == 0 {
			t.Fatalf("action %s missing from stats: %v", want, found)
		}
	}

	// 无全局查看权限的用户：只能看到自己的日志，传 actor=admin 也不越权
	userResp := serveAuthJSON(t, handler, "POST", "/api/users", admin, "198.51.100.9",
		map[string]any{"username": "auditreader", "password": "auditreader-password", "display_name": "日志读者", "role": "teacher", "permissions": []string{"audit:view"}})
	if userResp.Code != http.StatusCreated {
		t.Fatalf("create audit reader: %d %s", userResp.Code, userResp.Body.String())
	}
	reader := loginForAuthTest(t, handler, "auditreader", "auditreader-password", "198.51.100.10")
	own := serveAuthJSON(t, handler, "GET", "/api/audit-logs?actor=admin", reader, "198.51.100.10", nil)
	if own.Code != http.StatusOK {
		t.Fatalf("reader logs: %d %s", own.Code, own.Body.String())
	}
	json.Unmarshal(own.Body.Bytes(), &page)
	if page.Total == 0 {
		t.Fatal("reader should see own login log")
	}
	for _, log := range page.Logs {
		if log["actor"] != "auditreader" {
			t.Fatalf("reader saw foreign log: %v", log)
		}
	}
	readerActions := serveAuthJSON(t, handler, "GET", "/api/audit-logs/actions", reader, "198.51.100.10", nil)
	json.Unmarshal(readerActions.Body.Bytes(), &actions)
	for _, a := range actions.Actions {
		if a.Action == "expert_update" {
			t.Fatal("reader action stats leaked expert_update (admin-only action)")
		}
	}

	// 专家删除留痕
	deleted := serveAuthJSON(t, handler, "DELETE", "/api/experts/"+expert.ID, admin, "198.51.100.9", nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete expert: %d %s", deleted.Code, deleted.Body.String())
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'expert_delete'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expert_delete audit: count=%d err=%v", count, err)
	}
	var createDetail string
	if err := store.DB().QueryRow(`SELECT detail FROM audit_logs WHERE action = 'expert_create'`).Scan(&createDetail); err != nil || !strings.Contains(createDetail, "审计测试专家") {
		t.Fatalf("expert_create detail missing name: %q err=%v", createDetail, err)
	}
}
