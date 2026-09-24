package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
)

// switchRoleTestQuestion 构造一道可送审的题目（归属 reviewerID，AI 检查已通过）。
func switchRoleTestQuestion(id, reviewerID string) domain.A2Question {
	question := domain.A2Question{
		ID:           id,
		ClinicalStem: "男，45岁。反复上腹痛2年，加重1天。该患者最可能的诊断是",
		Options: []domain.Option{
			{Label: "A", Text: "胃溃疡"},
			{Label: "B", Text: "十二指肠溃疡"},
			{Label: "C", Text: "胃癌"},
			{Label: "D", Text: "慢性胃炎"},
			{Label: "E", Text: "功能性消化不良"},
		},
		Answer:      "A",
		Explanation: "正确答案为 A。该患者反复上腹痛，结合疼痛特点考虑胃溃疡。B 项疼痛规律更符合十二指肠溃疡；C 项缺乏消瘦等警示表现；D 项不能完整解释典型节律性疼痛；E 项应在排除器质性疾病后考虑。故选 A。",
		Difficulty:  "0.65",
		OutlineCode: "110.4.3.1.1",
		Profession:  "消化",
		System:      "消化系统",
		Status:      domain.StatusAIReviewed,
		Version:     1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	question.OwnerID = reviewerID
	question.CreatedBy = reviewerID
	return question
}

// TestSwitchRoleAdmissionFollowsPermissionSubset 回归（2026-09-25）：
// 切换身份准入按「权限集合」判定——目标权限覆盖当前权限时自由切换；
// 失去权限但无在途任务时同样放行；有在途任务时 409 并给出具体任务。
// 全程不依赖内置角色模板 ID（第 8 步用自定义角色证明）。
func TestSwitchRoleAdmissionFollowsPermissionSubset(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	ctx := context.Background()

	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "reviewer-dual", "password": "switch-password", "display_name": "双身份审题",
		"role": "teacher", "roles": []string{"teacher", "expert"},
	})
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("create dual user: status=%d body=%s", created.Code, created.Body.String())
	}
	dualID := decodeUserID(t, created.Body.String())

	dualToken := loginForAuthTest(t, handler, "reviewer-dual", "switch-password", "203.0.113.10")

	// 1. 无在途任务时，teacher→expert（失去出题等权限）放行。
	firstSwitch := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "expert"})
	if firstSwitch.Code != http.StatusOK {
		t.Fatalf("downgrade without tasks should pass: status=%d body=%s", firstSwitch.Code, firstSwitch.Body.String())
	}
	dualToken = switchToken(t, firstSwitch)

	_ = ctx
	// 2. 建流程（dual 为第 1 轮审题人）+ 送审一道题，制造在途任务。
	flowCreate := serveAuthJSON(t, handler, http.MethodPost, "/api/review/flows", adminToken, "198.51.100.1", map[string]any{
		"id":   "flow-switch-regression",
		"name": "切换回归流程",
		"rounds": []map[string]any{{
			"round_number": 1, "name": "初审", "expert_ids": []string{dualID}, "required_count": 1,
		}},
	})
	if flowCreate.Code != http.StatusCreated {
		t.Fatalf("create flow: status=%d body=%s", flowCreate.Code, flowCreate.Body.String())
	}
	if err := store.SaveQuestion(ctx, switchRoleTestQuestion("q-switch-regression", dualID)); err != nil {
		t.Fatal(err)
	}
	dualToken = loginForAuthTest(t, handler, "reviewer-dual", "switch-password", "203.0.113.10")

	submit := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", dualToken, "203.0.113.10", map[string]any{
		"question_id": "q-switch-regression", "flow_id": "flow-switch-regression",
	})
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review: status=%d body=%s", submit.Code, submit.Body.String())
	}

	// 3. teacher→expert（获得审题权限）自由切换。
	upgrade := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "expert"})
	if upgrade.Code != http.StatusOK {
		t.Fatalf("upgrade with pending task should pass: status=%d body=%s", upgrade.Code, upgrade.Body.String())
	}
	dualToken = switchToken(t, upgrade)

	// 4. expert→teacher（失去审题权限）被在途任务拦截。
	blocked := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "teacher"})
	if blocked.Code != http.StatusConflict {
		t.Fatalf("downgrade with pending review task should 409: status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	if !strings.Contains(blocked.Body.String(), "进行中的审核任务") {
		t.Fatalf("conflict should name the pending task: %s", blocked.Body.String())
	}

	// 5. 自定义角色证明：删除内置模板也不影响判定。
	customRole := serveAuthJSON(t, handler, http.MethodPost, "/api/roles", adminToken, "198.51.100.1", map[string]any{
		"id": "desk-reviewer", "name": "桌面审题", "permissions": []string{"question:view", "review:do"},
	})
	if customRole.Code != http.StatusOK && customRole.Code != http.StatusCreated {
		t.Fatalf("create custom role: status=%d body=%s", customRole.Code, customRole.Body.String())
	}
	// 新增自定义身份、保留 teacher（不失去任何权限，编辑守卫不应拦截）。
	added := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+dualID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "双身份审题", "role": "desk-reviewer", "roles": []string{"desk-reviewer", "teacher"},
	})
	if added.Code != http.StatusOK {
		t.Fatalf("add custom role keeping teacher: status=%d body=%s", added.Code, added.Body.String())
	}
	// 旧令牌身份（expert）已被移除：视为空集，允许切换到自定义身份。
	stale := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "desk-reviewer"})
	if stale.Code != http.StatusOK {
		t.Fatalf("switch with stale current identity should pass: status=%d body=%s", stale.Code, stale.Body.String())
	}
	dualToken = switchToken(t, stale)
	blockedCustom := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "teacher"})
	if blockedCustom.Code != http.StatusConflict || !strings.Contains(blockedCustom.Body.String(), "进行中的审核任务") {
		t.Fatalf("custom role losing review:do should be blocked by task: status=%d body=%s",
			blockedCustom.Code, blockedCustom.Body.String())
	}
}

// TestEditGuardBlockersPartitionByLostPermission 回归（2026-09-25）：
// 用户编辑守卫的阻断项必须按「实际失去的权限」分组——只收编辑权限时，
// 审题相关引用不得拦截；只收审题权限时才拦截流程审人引用。
func TestEditGuardBlockersPartitionByLostPermission(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()

	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	mkRole := func(id, name string, perms []string) {
		resp := serveAuthJSON(t, handler, http.MethodPost, "/api/roles", adminToken, "198.51.100.1", map[string]any{
			"id": id, "name": name, "permissions": perms,
		})
		if resp.Code != http.StatusOK && resp.Code != http.StatusCreated {
			t.Fatalf("create role %s: status=%d body=%s", id, resp.Code, resp.Body.String())
		}
	}
	mkRole("edit-reviewer", "编辑兼审题", []string{"question:view", "question:edit", "review:do"})
	mkRole("view-reviewer", "仅审题", []string{"question:view", "review:do"})
	mkRole("edit-only", "仅编辑", []string{"question:view", "question:edit"})

	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "partition-user", "password": "partition-password", "display_name": "分组回归",
		"role": "edit-reviewer", "roles": []string{"edit-reviewer"},
	})
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("create user: status=%d body=%s", created.Code, created.Body.String())
	}
	userID := decodeUserID(t, created.Body.String())

	flowCreate := serveAuthJSON(t, handler, http.MethodPost, "/api/review/flows", adminToken, "198.51.100.1", map[string]any{
		"id":   "flow-partition-regression",
		"name": "分组回归流程",
		"rounds": []map[string]any{{
			"round_number": 1, "name": "初审", "expert_ids": []string{userID}, "required_count": 1,
		}},
	})
	if flowCreate.Code != http.StatusCreated {
		t.Fatalf("create flow: status=%d body=%s", flowCreate.Code, flowCreate.Body.String())
	}
	// 只收 question:edit（保留 review:do）：流程审人引用不应拦截。
	keepReview := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+userID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "分组回归", "role": "view-reviewer", "roles": []string{"view-reviewer"},
	})
	if keepReview.Code != http.StatusOK {
		t.Fatalf("dropping only edit permission should pass: status=%d body=%s", keepReview.Code, keepReview.Body.String())
	}

	// 收 review:do（保留 question:edit）：流程审人引用必须拦截。
	dropReview := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+userID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "分组回归", "role": "edit-only", "roles": []string{"edit-only"},
	})
	if dropReview.Code != http.StatusConflict || !strings.Contains(dropReview.Body.String(), "审核人") {
		t.Fatalf("dropping review:do with flow reference should 409: status=%d body=%s",
			dropReview.Code, dropReview.Body.String())
	}
}

// switchToken 从切换身份响应中取新令牌。
func switchToken(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Token == "" {
		t.Fatalf("switch response missing token: %s", response.Body.String())
	}
	return payload.Token
}
