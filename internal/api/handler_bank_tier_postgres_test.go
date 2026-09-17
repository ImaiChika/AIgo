// 题库分层（正式/过程/淘汰）的 PostgreSQL 集成测试：
// 验证分层查看权限互相隔离、正式题库删除需专门权限、
// 导出仅限正式题库、分类子题库不触碰正式/淘汰题、撤回后题目回流过程题库。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"

	"aigo/internal/bank"
	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage/postgres"
)

func seedBankTierFixtures(t *testing.T, store *postgres.Store) {
	t.Helper()
	ctx := context.Background()
	for _, bank := range []domain.QuestionBank{
		{ID: "bank-a", Name: "内科分类库", CreatedAt: time.Now()},
		{ID: "bank-b", Name: "外科分类库", CreatedAt: time.Now()},
	} {
		if err := store.SaveBank(ctx, bank); err != nil {
			t.Fatal(err)
		}
	}
	seed := []domain.A2Question{
		{ID: "tier-work", Status: domain.StatusAIReviewed, Difficulty: "0.65", Profession: "内科", ClinicalStem: "过程题库：患者胸痛待查", Answer: "A", BankIDs: []string{"bank-a"}},
		{ID: "tier-formal", Status: domain.StatusPublished, Difficulty: "0.75", Profession: "外科", ClinicalStem: "正式题库：胃溃疡穿孔的处理", Answer: "B", BankIDs: []string{"bank-a"}},
		{ID: "tier-formal-2", Status: domain.StatusPublished, Difficulty: "0.75", Profession: "外科", ClinicalStem: "正式题库：阑尾炎术后补液", Answer: "C", BankIDs: []string{"bank-b"}},
		{ID: "tier-rejected", Status: domain.StatusRejected, Difficulty: "0.85", Profession: "儿科", ClinicalStem: "淘汰题库：小儿补液方案错误", Answer: "D", BankIDs: []string{"bank-b"}},
	}
	base := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	for i, q := range seed {
		q.Version = 1
		q.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		q.UpdatedAt = q.CreatedAt
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

// createTierUser 注册一个最小权限用户并由管理员授予指定权限点，返回其登录 token。
func createTierUser(t *testing.T, handler http.Handler, adminToken, username string, perms []string) string {
	t.Helper()
	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": username, "password": username + "-password", "display_name": username,
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("register %s status=%d body=%s", username, registration.Code, registration.Body.String())
	}
	var registered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registration.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": username, "role": "", "permissions": perms, "bank_ids": []string{}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign perms to %s status=%d body=%s", username, updated.Code, updated.Body.String())
	}
	return loginForAuthTest(t, handler, username, username+"-password", "203.0.113.10")
}

func createScopedTierUser(t *testing.T, handler http.Handler, adminToken, username, roleID string, perms, bankIDs []string) string {
	t.Helper()
	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": username, "password": username + "-password", "display_name": username,
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("register %s status=%d body=%s", username, registration.Code, registration.Body.String())
	}
	var registered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registration.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": username, "role": roleID, "permissions": perms, "bank_ids": bankIDs, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign scoped perms to %s status=%d body=%s", username, updated.Code, updated.Body.String())
	}
	return loginForAuthTest(t, handler, username, username+"-password", "203.0.113.10")
}

func decodeTierPage(t *testing.T, body string) questionPagePayload {
	t.Helper()
	return decodeQuestionPage(t, body)
}

func questionIDs(payload questionPagePayload) []string {
	ids := make([]string, 0, len(payload.Questions))
	for _, q := range payload.Questions {
		ids = append(ids, q.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestBankTierPermissionsAndExport(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedBankTierFixtures(t, store)
	server.bankSvc = bank.NewService(store, store)
	server.reviewSvc = review.NewService(store, store, store, server.authSvc)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	adminUser, err := server.authSvc.GetUserByUsername("admin")
	if err != nil || adminUser == nil {
		t.Fatal("load admin")
	}
	flow := domain.ReviewFlowConfig{ID: "tier-formal-flow", Name: "正式题撤回流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "审核", ExpertIDs: []string{adminUser.ID}, RequiredCount: 1}}, CreatedAt: time.Now()}
	if err := store.SaveFlowConfig(context.Background(), flow); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(context.Background(), domain.ReviewTask{ID: "tier-formal-task", QuestionID: "tier-formal", FlowID: flow.ID, CurrentRound: 1, Status: domain.StatusPublished, AssignedTo: []string{adminUser.ID}, QuestionVersion: 1, RoundResults: []domain.RoundResult{{RoundNumber: 1, Passed: true}}, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// 本测试验证分层查看边界；个人轴隔离（无 view_all 仅本人）是另一维度，
	// 因此这些用户显式授予 question:view_all 以聚焦分层语义。
	workerToken := createTierUser(t, handler, adminToken, "tier-worker", []string{
		domain.PermQuestionView, domain.PermQuestionEdit, domain.PermQuestionDelete,
		domain.PermQuestionGenerate, domain.PermQuestionDownload, domain.PermReviewResults,
		domain.PermQuestionViewAll,
	})
	viewerToken := createTierUser(t, handler, adminToken, "tier-viewer", []string{
		domain.PermQuestionView, domain.PermQuestionViewFormal, domain.PermQuestionDelete,
		domain.PermQuestionViewAll,
	})
	purgerToken := createTierUser(t, handler, adminToken, "tier-purger", []string{
		domain.PermQuestionView, domain.PermQuestionViewFormal, domain.PermQuestionDelete, domain.PermQuestionDeleteFormal,
		domain.PermQuestionViewAll,
	})
	formalOnlyToken := createScopedTierUser(t, handler, adminToken, "formal-only", "", []string{
		domain.PermQuestionViewFormal,
	}, []string{"bank-b"})
	teacherScopedToken := createScopedTierUser(t, handler, adminToken, "teacher-scoped", "teacher", nil, []string{"bank-a"})

	// 正式题库权限可独立使用，并严格受用户级分类子题库范围限制。
	formalOnly := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=formal", formalOnlyToken, "203.0.113.10", nil).Body.String())
	if got := questionIDs(formalOnly); fmt.Sprint(got) != fmt.Sprint([]string{"tier-formal-2"}) {
		t.Fatalf("formal-only scoped list wrong: %v", got)
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/tier-formal-2", formalOnlyToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("formal-only detail should be 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/tier-formal", formalOnlyToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("formal-only user leaked bank-a formal question: status=%d body=%s", resp.Code, resp.Body.String())
	}

	// 角色带来的 question:view 也受用户 bank_ids 限制，不能绕过范围看到其他库。
	teacherScoped := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=working", teacherScopedToken, "203.0.113.10", nil).Body.String())
	if got := questionIDs(teacherScoped); fmt.Sprint(got) != fmt.Sprint([]string{"tier-work"}) {
		t.Fatalf("role permission bypassed user bank scope: %v", got)
	}

	// ===== 管理员：未指定 tier 时看到全部可见分层 =====
	adminAll := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions", adminToken, "198.51.100.1", nil).Body.String())
	if adminAll.Total != 4 {
		t.Fatalf("admin default list should see all tiers, got %+v", questionIDs(adminAll))
	}

	// ===== 管理员：tier 过滤 =====
	for tier, want := range map[string][]string{
		"formal":     {"tier-formal", "tier-formal-2"},
		"working":    {"tier-work"},
		"eliminated": {"tier-rejected"},
	} {
		page := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier="+tier, adminToken, "198.51.100.1", nil).Body.String())
		if got := questionIDs(page); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("admin tier=%s got %v want %v", tier, got, want)
		}
	}

	// ===== 仅过程题库权限用户：默认只见过程题库；正式/淘汰题库不可见 =====
	workerAll := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions", workerToken, "203.0.113.10", nil).Body.String())
	if got := questionIDs(workerAll); fmt.Sprint(got) != fmt.Sprint([]string{"tier-work"}) {
		t.Fatalf("worker default list leaked other tiers: %v", got)
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=formal", workerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("worker tier=formal should be 403, got %d body=%s", resp.Code, resp.Body.String())
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=eliminated", workerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("worker tier=eliminated should be 403, got %d", resp.Code)
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/tier-formal", workerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("worker view formal detail should be 403, got %d", resp.Code)
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/tier-rejected", workerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("worker view eliminated detail should be 403, got %d", resp.Code)
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/tier-work", workerToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("worker view working detail should be 200, got %d", resp.Code)
	}
	// 审核记录汇总不得成为正式/淘汰题库的旁路。
	reviewResults := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results", workerToken, "203.0.113.10", nil)
	if reviewResults.Code != http.StatusOK {
		t.Fatalf("worker review results status=%d body=%s", reviewResults.Code, reviewResults.Body.String())
	}
	var reviewPayload struct {
		Items []struct {
			Question domain.A2Question `json:"question"`
		} `json:"items"`
		Stats struct {
			Total int `json:"total"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(reviewResults.Body.Bytes(), &reviewPayload); err != nil {
		t.Fatal(err)
	}
	if reviewPayload.Stats.Total != 1 || len(reviewPayload.Items) != 1 || reviewPayload.Items[0].Question.ID != "tier-work" {
		t.Fatalf("review results leaked other tiers: %+v", reviewPayload)
	}
	// 编辑/删除正式题库题目同样被分层门禁拦截
	if resp := serveAuthJSON(t, handler, http.MethodPut, "/api/questions/tier-formal", workerToken, "203.0.113.10", map[string]any{"clinical_stem": "越权修改"}); resp.Code != http.StatusForbidden {
		t.Fatalf("worker edit formal should be 403, got %d", resp.Code)
	}
	if resp := serveAuthJSON(t, handler, http.MethodDelete, "/api/questions/tier-formal", workerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("worker delete formal should be 403, got %d", resp.Code)
	}
	// 过程题库题目删除沿用 question:delete
	if resp := serveAuthJSON(t, handler, http.MethodDelete, "/api/questions/tier-work", workerToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("worker delete working should be 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	// 导出需要「查看正式题库」权限：仅有下载权限也会被拒绝
	if resp := serveAuthJSON(t, handler, http.MethodPost, "/api/export/xlsx", workerToken, "203.0.113.10", map[string]any{"export_all": true}); resp.Code != http.StatusForbidden {
		t.Fatalf("worker export should be 403, got %d body=%s", resp.Code, resp.Body.String())
	}

	// ===== 正式题库查看者：可见正式+过程，删除正式题库仍被拒 =====
	// （worker 已删除 tier-work，viewer 可见 formal 两题；rejected 不可见）
	viewerAll := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions", viewerToken, "203.0.113.10", nil).Body.String())
	if got := questionIDs(viewerAll); fmt.Sprint(got) != fmt.Sprint([]string{"tier-formal", "tier-formal-2"}) {
		t.Fatalf("viewer default list wrong: %v", got)
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=eliminated", viewerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("viewer tier=eliminated should be 403, got %d", resp.Code)
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/tier-formal", viewerToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("viewer view formal detail should be 200, got %d", resp.Code)
	}
	if resp := serveAuthJSON(t, handler, http.MethodDelete, "/api/questions/tier-formal", viewerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("viewer delete formal without delete_formal should be 403, got %d", resp.Code)
	}

	// ===== 正式题库删除者：专门权限可删正式题库题目 =====
	if resp := serveAuthJSON(t, handler, http.MethodDelete, "/api/questions/tier-formal-2", purgerToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("purger delete formal should be 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	// ===== 导出：正式题库专用出口，过程题库题目不可导出 =====
	exportResp := serveAuthJSON(t, handler, http.MethodPost, "/api/export/xlsx", adminToken, "198.51.100.1", map[string]any{"question_ids": []string{"tier-formal", "tier-work"}})
	if exportResp.Code != http.StatusOK {
		t.Fatalf("admin export status=%d body=%s", exportResp.Code, exportResp.Body.String())
	}
	var exported struct {
		Count int    `json:"count"`
		File  string `json:"filename"`
	}
	if err := json.Unmarshal(exportResp.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Count != 1 {
		t.Fatalf("export should only contain the formal question, got %d", exported.Count)
	}
	defer os.Remove("output/exports/" + exported.File)

	// ===== 分类子题库：只作用于过程题库 =====
	if _, err := server.bankSvc.CreateBank(context.Background(), "tier-sub", "分层测试子库", "", nil); err != nil {
		t.Fatal(err)
	}
	if resp := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/tier-formal/bank", adminToken, "198.51.100.1", map[string]any{"bank_id": "tier-sub"}); resp.Code != http.StatusBadRequest {
		t.Fatalf("add formal question to sub-bank should be 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if resp := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/tier-rejected/bank", adminToken, "198.51.100.1", map[string]any{"bank_id": "tier-sub"}); resp.Code != http.StatusBadRequest {
		t.Fatalf("add rejected question to sub-bank should be 400, got %d", resp.Code)
	}
	if resp := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/bank-move-batch", adminToken, "198.51.100.1", map[string]any{"ids": []string{"tier-formal"}, "bank_id": "tier-sub"}); resp.Code != http.StatusBadRequest {
		t.Fatalf("batch move formal question should be 400, got %d", resp.Code)
	}

	// ===== 撤回：正式题库回流过程题库 =====
	if resp := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/tier-formal/unpublish", adminToken, "198.51.100.1", map[string]any{"reason": "修订"}); resp.Code != http.StatusOK {
		t.Fatalf("unpublish status=%d body=%s", resp.Code, resp.Body.String())
	}
	adminWorking := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=working", adminToken, "198.51.100.1", nil).Body.String())
	if got := questionIDs(adminWorking); len(got) != 1 || got[0] != "tier-formal" {
		t.Fatalf("unpublished question should reappear in working tier, got %v", got)
	}
	adminFormal := decodeTierPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=formal", adminToken, "198.51.100.1", nil).Body.String())
	if got := questionIDs(adminFormal); len(got) != 0 {
		t.Fatalf("formal tier should be empty after unpublish, got %v", got)
	}
}

func TestAssignedReviewerUsesTaskAccessWithoutQuestionBankView(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := store.SaveBank(ctx, domain.QuestionBank{ID: "bank-review", Name: "审核分类库", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	question := domain.A2Question{
		ID: "task-only-question", ClinicalStem: "患者突发胸痛，最可能的诊断是？",
		Options: []domain.Option{{Label: "A", Text: "心肌梗死"}, {Label: "B", Text: "胃炎"}, {Label: "C", Text: "阑尾炎"}, {Label: "D", Text: "湿疹"}},
		Answer:  "A", Status: domain.StatusAIReviewed, Version: 1, BankIDs: []string{"bank-review"},
	}
	if err := store.SaveQuestion(ctx, question); err != nil {
		t.Fatal(err)
	}
	server.bankSvc = bank.NewService(store, store)
	server.reviewSvc = review.NewService(store, store, store, server.authSvc)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "task-reviewer", "password": "task-reviewer-password", "display_name": "任务审题人",
	})
	var reviewer struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registration.Body.Bytes(), &reviewer); err != nil || reviewer.ID == "" {
		t.Fatalf("register reviewer: err=%v body=%s", err, registration.Body.String())
	}
	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+reviewer.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "任务审题人", "role": "", "permissions": []string{domain.PermReviewDo}, "bank_ids": []string{"bank-review"}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign reviewer status=%d body=%s", updated.Code, updated.Body.String())
	}
	flow := domain.ReviewFlowConfig{
		ID: "task-access-flow", Name: "任务访问流程", BankID: "bank-review",
		Rounds:    []domain.RoundConfig{{RoundNumber: 1, Name: "专家审核", ExpertIDs: []string{reviewer.ID}, RequiredCount: 1}},
		CreatedAt: time.Now(),
	}
	if err := store.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	submitted := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", adminToken, "198.51.100.1", map[string]any{
		"question_id": question.ID, "flow_id": flow.ID, "bank_id": "bank-review",
	})
	if submitted.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", submitted.Code, submitted.Body.String())
	}
	var task domain.ReviewTask
	if err := json.Unmarshal(submitted.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	reviewerToken := loginForAuthTest(t, handler, "task-reviewer", "task-reviewer-password", "203.0.113.10")
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/"+question.ID, reviewerToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("reviewer should not gain bank browsing: status=%d body=%s", resp.Code, resp.Body.String())
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/review/task/"+task.ID, reviewerToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("assigned reviewer should access task detail: status=%d body=%s", resp.Code, resp.Body.String())
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/review/records/"+task.ID, reviewerToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("assigned reviewer should access own task records: status=%d body=%s", resp.Code, resp.Body.String())
	}
}
