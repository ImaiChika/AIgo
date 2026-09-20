// 本文件覆盖三项需求的 PostgreSQL 集成行为：
// 1) 审题老师“我的审核记录”范围（scope=mine）：仅返回本人参审题目，全局仍需管理员权限；
// 2) AI 检查不通过的题目：淘汰留档携带归属人，并进入该用户的个人数据统计；
// 3) 操作日志分页：返回真实总数与按页数据。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/domain"
	"aigo/internal/llm"
)

// rejectCheckClient 模拟质检模型给出“发现问题”结论（不调用真实 API）。
type rejectCheckClient struct{}

func (c *rejectCheckClient) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	raw, _ := json.Marshal(map[string]any{
		"verdict": "issues_found",
		"scores":  map[string]int{"scientific": 70, "logic": 60, "a2_fit": 75, "answer": 80},
		"issues": []any{map[string]any{
			"field":    "stem",
			"severity": "error",
			"message":  "题干关键信息不足",
		}},
		"suggestion": "请补充病史与检查结果",
	})
	return string(raw), nil
}

func TestExpertMineReviewRecordsScope(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedStatsFixtures(t, store)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	// 两位审题老师（内置模板已含 review:view_results）。
	for _, name := range []string{"mine-expert-a", "mine-expert-b"} {
		created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
			"username": name, "password": name + "-password", "display_name": name, "role": domain.RoleExpert,
		})
		if created.Code != http.StatusCreated {
			t.Fatalf("create %s: status=%d body=%s", name, created.Code, created.Body.String())
		}
	}
	expertA, err := server.authSvc.GetUserByUsername("mine-expert-a")
	if err != nil || expertA == nil {
		t.Fatalf("load expert a: %v", err)
	}
	expertB, err := server.authSvc.GetUserByUsername("mine-expert-b")
	if err != nil || expertB == nil {
		t.Fatalf("load expert b: %v", err)
	}
	// expert-a 的 me 响应应包含审核记录查看权限（菜单可见性依据）。
	tokenA := loginForAuthTest(t, handler, "mine-expert-a", "mine-expert-a-password", "198.51.100.1")
	me := serveAuthJSON(t, handler, http.MethodGet, "/api/auth/me", tokenA, "198.51.100.1", nil)
	if !strings.Contains(me.Body.String(), domain.PermReviewResults) {
		t.Fatalf("expert permissions missing review:view_results: %s", me.Body.String())
	}

	// 只有 expert-a 在 stats-w1 上提交过审核意见。
	ctx := t.Context()
	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "mine-flow", Name: "我的审核记录测试流程", CreatedAt: time.Now(),
		Rounds: []domain.RoundConfig{{RoundNumber: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(ctx, domain.ReviewTask{
		ID: "mine-rt-1", QuestionID: "stats-w1", FlowID: "mine-flow", CurrentRound: 1,
		Status: domain.StatusReviewing, AssignedTo: []string{expertA.ID}, QuestionVersion: 1,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecord(ctx, domain.ReviewRecord{
		ID: "mine-rr-1", TaskID: "mine-rt-1", QuestionID: "stats-w1", RoundNumber: 1,
		ExpertID: expertA.ID, ExpertName: "mine-expert-a", Conclusion: domain.StatusApproved,
		Opinion: "审题意见", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mineA := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=mine&page_size=50", tokenA, "198.51.100.1", nil)
	if mineA.Code != http.StatusOK {
		t.Fatalf("expert-a mine scope status=%d body=%s", mineA.Code, mineA.Body.String())
	}
	var payloadA struct {
		Items []struct {
			Question struct {
				ID string `json:"id"`
			} `json:"question"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(mineA.Body.Bytes(), &payloadA); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range payloadA.Items {
		if item.Question.ID == "stats-w1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expert-a mine scope should include reviewed question stats-w1: %+v", payloadA.Items)
	}

	// 未参审的 expert-b 在 mine 范围看不到该题。
	tokenB := loginForAuthTest(t, handler, "mine-expert-b", "mine-expert-b-password", "198.51.100.1")
	mineB := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=mine&page_size=50", tokenB, "198.51.100.1", nil)
	if mineB.Code != http.StatusOK {
		t.Fatalf("expert-b mine scope status=%d body=%s", mineB.Code, mineB.Body.String())
	}
	if strings.Contains(mineB.Body.String(), "stats-w1") {
		t.Fatalf("expert-b should not see question reviewed only by expert-a: %s", mineB.Body.String())
	}

	// 全局审核记录仍是管理员级权限：审题老师 403。
	global := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=global", tokenA, "198.51.100.1", nil)
	if global.Code != http.StatusForbidden {
		t.Fatalf("expert global scope should stay forbidden: status=%d", global.Code)
	}
}

func TestAICheckDiscardCarriesOwnerAndShowsInPersonalStats(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedStatsFixtures(t, store)
	server.aiCheckSvc = aicheck.NewService(&rejectCheckClient{}, store, store, store, "mock-model")
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "discard-owner", "password": "discard-owner-pw", "display_name": "淘汰归属人", "role": domain.RoleTeacher,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create discard owner: status=%d body=%s", created.Code, created.Body.String())
	}
	owner, err := server.authSvc.GetUserByUsername("discard-owner")
	if err != nil || owner == nil {
		t.Fatalf("load owner: %v", err)
	}

	ctx := t.Context()
	if err := store.SaveQuestion(ctx, domain.A2Question{
		ID: "mine-discard-q", Status: domain.StatusAIDraft, Difficulty: "0.65", Profession: "内科",
		ClinicalStem: "个人统计-将被淘汰的题", Answer: "A", OwnerID: owner.ID, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟质检不通过：题目物理删除 + 淘汰留档（必须带归属人）。
	if _, err := server.aiCheckSvc.CheckQuestion(ctx, "mine-discard-q"); err != nil {
		t.Fatalf("mock check failed: %v", err)
	}
	if q, _ := store.GetQuestion(ctx, "mine-discard-q"); q != nil {
		t.Fatal("question failing AI check should be deleted")
	}
	discards, err := store.ListDiscardResultsByQuestionIDs(ctx, []string{"mine-discard-q"})
	if err != nil {
		t.Fatal(err)
	}
	discard, ok := discards["mine-discard-q"]
	if !ok {
		t.Fatal("discard archive missing for failed check")
	}
	if discard.OwnerID != owner.ID {
		t.Fatalf("discard owner=%q want %q（未来新账号的个人统计依赖该归属）", discard.OwnerID, owner.ID)
	}

	// 归属人的个人数据统计必须体现淘汰，而不是“全部通过”。
	ownerToken := loginForAuthTest(t, handler, "discard-owner", "discard-owner-pw", "198.51.100.1")
	personal := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=personal", ownerToken, "198.51.100.1", nil)
	if personal.Code != http.StatusOK {
		t.Fatalf("owner personal stats status=%d body=%s", personal.Code, personal.Body.String())
	}
	var payload struct {
		AICheck struct {
			Discarded int            `json:"discarded"`
			Verdicts  map[string]int `json:"verdicts"`
		} `json:"ai_check"`
	}
	if err := json.Unmarshal(personal.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AICheck.Discarded != 1 {
		t.Fatalf("owner personal ai_check.discarded=%d, want 1", payload.AICheck.Discarded)
	}

	// 全局统计同样计入该淘汰。
	adminGlobal := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=global", adminToken, "198.51.100.1", nil)
	var globalPayload struct {
		AICheck struct {
			Discarded int `json:"discarded"`
		} `json:"ai_check"`
	}
	if err := json.Unmarshal(adminGlobal.Body.Bytes(), &globalPayload); err != nil {
		t.Fatal(err)
	}
	if globalPayload.AICheck.Discarded < 1 {
		t.Fatalf("global discarded=%d, want >=1", globalPayload.AICheck.Discarded)
	}
}

func TestAuditLogsPagination(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	ctx := t.Context()
	for i := 0; i < 7; i++ {
		if err := server.auditSvc.Log(ctx, "", "import", "admin", fmt.Sprintf("分页测试 %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	var total int
	for page := 1; ; page++ {
		resp := serveAuthJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/audit-logs?page=%d&limit=3", page), adminToken, "198.51.100.1", nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("audit page %d status=%d body=%s", page, resp.Code, resp.Body.String())
		}
		var payload struct {
			Logs  []domain.AuditLog `json:"logs"`
			Total int               `json:"total"`
			Page  int               `json:"page"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if page == 1 {
			total = payload.Total
			if total < 8 { // 7 条测试日志 + 管理员登录日志
				t.Fatalf("audit total=%d, want >=8", total)
			}
		}
		if payload.Total != total || payload.Page != page {
			t.Fatalf("audit pagination metadata inconsistent: page=%d resp=%+v", page, payload)
		}
		for _, entry := range payload.Logs {
			if seen[entry.ID] {
				t.Fatalf("audit log %s returned on multiple pages", entry.ID)
			}
			seen[entry.ID] = true
		}
		if len(seen) >= total {
			break
		}
	}
	if len(seen) != total {
		t.Fatalf("walked %d unique audit logs, total=%d", len(seen), total)
	}
}

// 审核记录统计口径：已归档（管理员删除留档）的题目必须与“未提交审核”分开——
// 既不得落入 pending 抬高未提交数，也不得按其历史任务显示“需修改”；
// 归档题单独进入 archived 桶，并可作为 final_status 过滤。
func TestReviewResultsSeparateArchivedFromPending(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedStatsFixtures(t, store)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	adminUser, err := server.authSvc.GetUserByUsername("admin")
	if err != nil || adminUser == nil {
		t.Fatalf("load admin: %v", err)
	}
	ctx := t.Context()
	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "arch-flow", Name: "归档口径测试流程", CreatedAt: time.Now(),
		Rounds: []domain.RoundConfig{{RoundNumber: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	seed := []struct {
		id       string
		status   domain.QuestionStatus
		withTask bool
		taskSts  domain.QuestionStatus
	}{
		{"arch-pend-1", domain.StatusAIReviewed, false, ""},
		{"arch-pend-2", domain.StatusAIReviewed, false, ""},
		{"arch-dead-1", domain.StatusArchived, false, ""},
		{"arch-dead-2", domain.StatusArchived, true, domain.StatusRevisionRequired},
		{"arch-pub-1", domain.StatusPublished, false, ""},
	}
	for _, s := range seed {
		if err := store.SaveQuestion(ctx, domain.A2Question{
			ID: s.id, Status: s.status, Difficulty: "0.65", Profession: "内科",
			ClinicalStem: "归档口径-" + s.id, Answer: "A", OwnerID: adminUser.ID, Version: 1,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if s.withTask {
			if err := store.SaveTask(ctx, domain.ReviewTask{
				ID: "arch-task-" + s.id, QuestionID: s.id, FlowID: "arch-flow", CurrentRound: 1,
				Status: s.taskSts, AssignedTo: []string{adminUser.ID}, QuestionVersion: 1,
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	personal := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=personal&page_size=50", adminToken, "198.51.100.1", nil)
	if personal.Code != http.StatusOK {
		t.Fatalf("personal scope status=%d body=%s", personal.Code, personal.Body.String())
	}
	var payload struct {
		Stats struct {
			Pending          int `json:"pending"`
			Archived         int `json:"archived"`
			Published        int `json:"published"`
			RevisionRequired int `json:"revision_required"`
			Total            int `json:"total"`
		} `json:"stats"`
		Items []struct {
			Question struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"question"`
			FinalStatus string `json:"final_status"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(personal.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Stats.Pending != 2 {
		t.Fatalf("pending=%d, want 2（仅真未送审题）", payload.Stats.Pending)
	}
	if payload.Stats.Archived != 2 {
		t.Fatalf("archived=%d, want 2（含残留需修改任务的归档题）", payload.Stats.Archived)
	}
	if payload.Stats.Published != 1 {
		t.Fatalf("published=%d, want 1", payload.Stats.Published)
	}
	if payload.Stats.RevisionRequired != 0 {
		t.Fatalf("revision_required=%d, want 0（归档题不得按历史任务显示需修改）", payload.Stats.RevisionRequired)
	}
	for _, item := range payload.Items {
		if item.Question.ID == "arch-dead-2" && item.FinalStatus != "archived" {
			t.Fatalf("archived question with stale task final_status=%q, want archived", item.FinalStatus)
		}
	}

	// final_status=archived 过滤只返回归档题。
	filtered := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=personal&final_status=archived&page_size=50", adminToken, "198.51.100.1", nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("archived filter status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	var filteredPayload struct {
		Items []struct {
			Question struct {
				ID string `json:"id"`
			} `json:"question"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredPayload); err != nil {
		t.Fatal(err)
	}
	if filteredPayload.Total != 2 {
		t.Fatalf("archived filter total=%d, want 2", filteredPayload.Total)
	}
	for _, item := range filteredPayload.Items {
		if item.Question.ID != "arch-dead-1" && item.Question.ID != "arch-dead-2" {
			t.Fatalf("archived filter returned non-archived question %s", item.Question.ID)
		}
	}

	// 我的审核记录：参审过 arch-dead-2 的审题人应看到该题归为“已归档”而非“需修改/未提交审核”。
	expertCreated := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "arch-viewer", "password": "arch-viewer-pw", "display_name": "归档口径审题人", "role": domain.RoleExpert,
	})
	if expertCreated.Code != http.StatusCreated {
		t.Fatalf("create expert: status=%d body=%s", expertCreated.Code, expertCreated.Body.String())
	}
	expert, err := server.authSvc.GetUserByUsername("arch-viewer")
	if err != nil || expert == nil {
		t.Fatalf("load expert: %v", err)
	}
	if err := store.SaveRecord(ctx, domain.ReviewRecord{
		ID: "arch-rr-1", TaskID: "arch-task-arch-dead-2", QuestionID: "arch-dead-2", RoundNumber: 1,
		ExpertID: expert.ID, ExpertName: "arch-viewer", Conclusion: domain.StatusRevisionRequired,
		Opinion: "退回意见", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	expertToken := loginForAuthTest(t, handler, "arch-viewer", "arch-viewer-pw", "198.51.100.1")
	mine := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=mine&page_size=50", expertToken, "198.51.100.1", nil)
	if mine.Code != http.StatusOK {
		t.Fatalf("mine scope status=%d body=%s", mine.Code, mine.Body.String())
	}
	var minePayload struct {
		Stats struct {
			Archived         int `json:"archived"`
			Pending          int `json:"pending"`
			RevisionRequired int `json:"revision_required"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(mine.Body.Bytes(), &minePayload); err != nil {
		t.Fatal(err)
	}
	if minePayload.Stats.Archived != 1 || minePayload.Stats.RevisionRequired != 0 || minePayload.Stats.Pending != 0 {
		t.Fatalf("mine stats wrong: %+v（参审题归档后应计入 archived）", minePayload.Stats)
	}
}
