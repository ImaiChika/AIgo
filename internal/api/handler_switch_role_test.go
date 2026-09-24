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

// switchRoleTestQuestion 构造一道归属 reviewerID 的题目。
func switchRoleTestQuestion(id, reviewerID string, status domain.QuestionStatus) domain.A2Question {
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
		Status:      status,
		Version:     1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	question.OwnerID = reviewerID
	question.CreatedBy = reviewerID
	return question
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

// TestSwitchRoleStaysFreeWithInflightWork 回归（2026-09-25）：已挂载身份之间的
// 切换永远自由，即使名下有在途审核任务或待本人修改的退修题——任务归属账号，
// 切换只改变可见页面，用户随时切回处理。此前错误加入的准入拦截必须保持移除。
func TestSwitchRoleStaysFreeWithInflightWork(t *testing.T) {
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

	// 制造两类在途占用：
	// 1) 名下送审的题目（进行中的审核任务，需要 review:do 处理）；
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
	if err := store.SaveQuestion(ctx, switchRoleTestQuestion("q-inflight-review", dualID, domain.StatusAIReviewed)); err != nil {
		t.Fatal(err)
	}
	submit := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", dualToken, "203.0.113.10", map[string]any{
		"question_id": "q-inflight-review", "flow_id": "flow-switch-regression",
	})
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review: status=%d body=%s", submit.Code, submit.Body.String())
	}

	// 2) 名下退回修改中的题目（待本人修改，需要 question:edit 处理）。
	if err := store.SaveQuestion(ctx, switchRoleTestQuestion("q-own-revision", dualID, domain.StatusAIReviewed)); err != nil {
		t.Fatal(err)
	}
	submit2 := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", dualToken, "203.0.113.10", map[string]any{
		"question_id": "q-own-revision", "flow_id": "flow-switch-regression",
	})
	if submit2.Code != http.StatusOK {
		t.Fatalf("submit second review: status=%d body=%s", submit2.Code, submit2.Body.String())
	}
	// 先切到 expert（拿到 review:do），再把第二题审成「需修改」。
	toExpert := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "expert"})
	if toExpert.Code != http.StatusOK {
		t.Fatalf("switch to expert: status=%d body=%s", toExpert.Code, toExpert.Body.String())
	}
	dualToken = switchToken(t, toExpert)
	myTasks := serveAuthJSON(t, handler, http.MethodGet, "/api/review/my-tasks", dualToken, "203.0.113.10", nil)
	if myTasks.Code != http.StatusOK {
		t.Fatalf("my-tasks: status=%d body=%s", myTasks.Code, myTasks.Body.String())
	}
	var taskPage struct {
		Tasks []struct {
			Task struct {
				ID         string `json:"id"`
				QuestionID string `json:"question_id"`
			} `json:"task"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(myTasks.Body.Bytes(), &taskPage); err != nil {
		t.Fatal(err)
	}
	var ownTaskID string
	for _, item := range taskPage.Tasks {
		if item.Task.QuestionID == "q-own-revision" {
			ownTaskID = item.Task.ID
		}
	}
	if ownTaskID == "" {
		t.Fatalf("own review task not found: %s", myTasks.Body.String())
	}
	revision := serveAuthJSON(t, handler, http.MethodPost, "/api/review/action", dualToken, "203.0.113.10", map[string]any{
		"task_id": ownTaskID, "action": "revision_required", "opinion": "回归：退回修改",
	})
	if revision.Code != http.StatusOK {
		t.Fatalf("revision action: status=%d body=%s", revision.Code, revision.Body.String())
	}

	// 两类占用都在：expert ↔ teacher 双向切换都必须自由。
	back := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "teacher"})
	if back.Code != http.StatusOK {
		t.Fatalf("switch with inflight review task and own revision must stay free: status=%d body=%s",
			back.Code, back.Body.String())
	}
	dualToken = switchToken(t, back)
	again := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", dualToken, "203.0.113.10", map[string]any{"role": "expert"})
	if again.Code != http.StatusOK {
		t.Fatalf("switch back must stay free: status=%d body=%s", again.Code, again.Body.String())
	}
}

// TestEditGuardBlockersPartitionByLostPermission 回归（2026-09-25）：
// admin 权限分配的收权守卫按「实际失去的权限」分组阻断——只收 question:edit
// 时，审题相关引用不得拦截；收 review:do 时才拦截流程审人引用。
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
