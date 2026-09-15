package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aigo/internal/bank"
	"aigo/internal/domain"
)

// TestQuestionVisibilityPersonalAxis 验证个人轴题目可见性三档语义：
//   - 默认（无 view_all、未分配题库范围）：仅本人题目，他人题目与历史无归属题不可见；
//   - 授予 question:view_all：可见全部题目（含历史无归属题）与完整子题库目录；
//   - 直接分配题库范围（bank_ids）：范围内题库的题目可见（含他人）。
//
// 同时验证子题库目录不再向普通用户泄露全部子题库（如管理员自建测试题库）。
func TestQuestionVisibilityPersonalAxis(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.bankSvc = bank.NewService(store, store)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	ctx := context.Background()
	now := time.Now()

	// 准备：管理员自建的测试题库 + 专家题目关联的归纳库
	if err := store.SaveBank(ctx, domain.QuestionBank{ID: "bank-admin-test", Name: "管理员第一次测试", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBank(ctx, domain.QuestionBank{ID: "bank-expert-own", Name: "专家归纳库", Professions: []string{"消化"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// 历史无归属题（owner_id 空，挂在管理员测试题库下）
	legacy := domain.A2Question{
		ID: "vis-legacy", ClinicalStem: "历史无归属题干", Options: []domain.Option{{Label: "A", Text: "选项"}},
		Answer: "A", Status: domain.StatusAIReviewed, Version: 1, BankIDs: []string{"bank-admin-test"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	// 注册专家并授予 expert 角色（question:view 等来自角色；无 view_all、无 bank_ids）
	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "vis-expert", "password": "vis-expert-password", "display_name": "可见性专家",
	})
	var expert struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registration.Body.Bytes(), &expert); err != nil || expert.ID == "" {
		t.Fatalf("register expert: err=%v", err)
	}
	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+expert.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "可见性专家", "role": domain.RoleExpert, "permissions": []string{}, "bank_ids": []string{}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign expert role: status=%d body=%s", updated.Code, updated.Body.String())
	}
	expertToken := loginForAuthTest(t, handler, "vis-expert", "vis-expert-password", "203.0.113.10")

	// 专家本人的一道题（归本人，按专业归纳进 bank-expert-own）
	own := domain.A2Question{
		ID: "vis-expert-own", ClinicalStem: "专家本人题干", Options: []domain.Option{{Label: "A", Text: "选项"}},
		Answer: "A", Status: domain.StatusAIReviewed, Version: 1, OwnerID: expert.ID, CreatedBy: "vis-expert",
		BankIDs: []string{"bank-expert-own"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(ctx, own); err != nil {
		t.Fatal(err)
	}

	// ===== 默认：专家只看到本人的题；他人/历史题不可见 =====
	list := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=personal", expertToken, "203.0.113.10", nil).Body.String())
	if list.Total != 1 || len(list.Questions) != 1 || list.Questions[0].ID != "vis-expert-own" {
		t.Fatalf("expert default list should contain only own question, got total=%d ids=%v", list.Total, questionIDs(list))
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/vis-legacy", expertToken, "203.0.113.10", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("expert should not view legacy question: status=%d body=%s", resp.Code, resp.Body.String())
	}

	// ===== 子题库目录：专家只看到本人题目涉及的子题库，不泄露管理员的测试题库 =====
	banksResp := serveAuthJSON(t, handler, http.MethodGet, "/api/banks", expertToken, "203.0.113.10", nil)
	if banksResp.Code != http.StatusOK {
		t.Fatalf("banks list status=%d", banksResp.Code)
	}
	var banksPayload struct {
		Banks []struct {
			ID string `json:"id"`
		} `json:"banks"`
	}
	if err := json.Unmarshal(banksResp.Body.Bytes(), &banksPayload); err != nil {
		t.Fatal(err)
	}
	for _, b := range banksPayload.Banks {
		if b.ID == "bank-admin-test" {
			t.Fatal("expert should not see admin's test bank in catalog")
		}
	}
	if len(banksPayload.Banks) != 1 || banksPayload.Banks[0].ID != "bank-expert-own" {
		t.Fatalf("expert catalog should contain only own-relevant bank, got %+v", banksPayload.Banks)
	}

	// ===== 授予 question:view_all：可见全部题目（含历史无归属题）与完整子题库目录 =====
	updated = serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+expert.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "可见性专家", "role": domain.RoleExpert, "permissions": []string{domain.PermQuestionViewAll}, "bank_ids": []string{}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("grant view_all: status=%d body=%s", updated.Code, updated.Body.String())
	}
	list = decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=personal", expertToken, "203.0.113.10", nil).Body.String())
	if list.Total != 2 {
		t.Fatalf("expert with view_all should see all personal-axis questions, got total=%d ids=%v", list.Total, questionIDs(list))
	}
	if resp := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/vis-legacy", expertToken, "203.0.113.10", nil); resp.Code != http.StatusOK {
		t.Fatalf("expert with view_all should view legacy question: status=%d", resp.Code)
	}
	banksResp = serveAuthJSON(t, handler, http.MethodGet, "/api/banks", expertToken, "203.0.113.10", nil)
	if err := json.Unmarshal(banksResp.Body.Bytes(), &banksPayload); err != nil {
		t.Fatal(err)
	}
	sawAdminBank := false
	for _, b := range banksPayload.Banks {
		if b.ID == "bank-admin-test" {
			sawAdminBank = true
		}
	}
	if !sawAdminBank {
		t.Fatal("expert with view_all should see full bank catalog")
	}

	// ===== bank_ids 范围授权：范围内题库的题目可见（含他人/历史题） =====
	updated = serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+expert.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "可见性专家", "role": "", "permissions": []string{domain.PermQuestionView}, "bank_ids": []string{"bank-admin-test"}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign scoped: status=%d", updated.Code)
	}
	// 管理员已撤销原 expert 身份，旧 JWT 的身份绑定应失效；重新登录后
	// 直接授权题库范围继续生效。
	expertToken = loginForAuthTest(t, handler, "vis-expert", "vis-expert-password", "203.0.113.10")
	list = decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=personal", expertToken, "203.0.113.10", nil).Body.String())
	sawScoped := false
	for _, q := range list.Questions {
		if q.ID == "vis-legacy" {
			sawScoped = true
		}
	}
	if !sawScoped {
		t.Fatalf("scoped user should see questions within granted banks: ids=%v", questionIDs(list))
	}
}
