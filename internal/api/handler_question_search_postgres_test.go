// 题目列表/搜索/题库题目接口的 PostgreSQL 集成测试：
// 验证数据库端过滤分页后的 HTTP 响应结构、过滤语义与题库范围权限。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage/postgres"
)

func seedQuestionSearchFixtures(t *testing.T, store *postgres.Store) {
	t.Helper()
	ctx := context.Background()
	for _, bank := range []domain.QuestionBank{
		{ID: "bank-a", Name: "内科题库", CreatedAt: time.Now()},
		{ID: "bank-b", Name: "外科题库", CreatedAt: time.Now()},
	} {
		if err := store.SaveBank(ctx, bank); err != nil {
			t.Fatal(err)
		}
	}
	seed := []domain.A2Question{
		{ID: "q1", Status: domain.StatusAIDraft, Difficulty: "0.65", Profession: "内科", System: "十二、传染病、性传播疾病", OutlineCode: "110.4.3.1.1", ClinicalStem: "患者胸痛考虑急性心肌梗死", Answer: "A", BankIDs: []string{"bank-a"}},
		{ID: "q2", Status: domain.StatusPublished, Difficulty: "0.85", Profession: "外科", OutlineCode: "110.5.1.2", ClinicalStem: "患者胃溃疡穿孔需要急诊手术", Answer: "B", BankIDs: []string{"bank-a", "bank-b"}},
		{ID: "q3", Status: domain.StatusAIDraft, Difficulty: "0.65", Profession: "内科", OutlineCode: "210.1.1", ClinicalStem: "肺部感染影像学表现", Answer: "C"},
		{ID: "q4", Status: domain.StatusRejected, Difficulty: "0.75", Profession: "儿科", OutlineCode: "110.4.9.9", ClinicalStem: "小儿腹泻脱水的补液方案", Answer: "D", BankIDs: []string{"bank-b"}},
		{ID: "q5", Status: domain.StatusAIDraft, Difficulty: "0.75", Profession: "内科", OutlineCode: "110.4.3.2", ClinicalStem: "生存率100%的患者随访", Answer: "E", BankIDs: []string{"bank-a"}},
	}
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	for i, q := range seed {
		q.Version = 1
		q.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		q.UpdatedAt = q.CreatedAt
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

type questionPagePayload struct {
	Questions []domain.A2Question `json:"questions"`
	Total     int                 `json:"total"`
	Page      int                 `json:"page"`
	PageSize  int                 `json:"page_size"`
	HasMore   bool                `json:"has_more"`
}

func decodeQuestionPage(t *testing.T, body string) questionPagePayload {
	t.Helper()
	var payload questionPagePayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("parse question page response: %v body=%s", err, body)
	}
	return payload
}

func TestQuestionSearchEndpointsPaged(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedQuestionSearchFixtures(t, store)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	// 列表：数据库端分页（暂存草稿 q1/q3/q5 对用户不可见，仅剩 q4、q2）
	list := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?page=1&page_size=2", adminToken, "198.51.100.1", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	page := decodeQuestionPage(t, list.Body.String())
	if page.Total != 2 || page.Page != 1 || page.PageSize != 2 || page.HasMore || len(page.Questions) != 2 {
		t.Fatalf("unexpected list page: %+v", page)
	}
	if page.Questions[0].ID != "q4" || page.Questions[1].ID != "q2" {
		t.Fatalf("list page order wrong: %s, %s", page.Questions[0].ID, page.Questions[1].ID)
	}

	// 搜索：关键词（题干）
	byStem := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions/search?q=%E6%BA%83%E7%96%A1", adminToken, "198.51.100.1", nil).Body.String())
	if byStem.Total != 1 || len(byStem.Questions) != 1 || byStem.Questions[0].ID != "q2" {
		t.Fatalf("keyword search wrong: %+v", byStem)
	}
	// "100%" 必须按字面匹配（LIKE 转义）；唯一命中 q5 是暂存草稿，同样不可见
	byPercent := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions/search?q=100%25", adminToken, "198.51.100.1", nil).Body.String())
	if byPercent.Total != 0 {
		t.Fatalf("percent keyword search should hide staging draft: %+v", byPercent)
	}
	bySystem := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions/search?q=%E4%BC%A0%E6%9F%93%E7%97%85", adminToken, "198.51.100.1", nil).Body.String())
	if bySystem.Total != 0 {
		t.Fatalf("system keyword search should hide staging draft: %+v", bySystem)
	}
	classifiable := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions/search?classifiable=true", adminToken, "198.51.100.1", nil).Body.String())
	if classifiable.Total != 0 {
		t.Fatalf("classifiable search should hide staging drafts (no ai_reviewed seeded): %+v", classifiable)
	}

	// 搜索：显式按暂存状态筛选 → 一律空页（待检题目对用户不可见，含管理员）
	byStatus := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions/search?status=ai_draft&profession=%E5%86%85%E7%A7%91,%E5%A4%96%E7%A7%91", adminToken, "198.51.100.1", nil).Body.String())
	if byStatus.Total != 0 || len(byStatus.Questions) != 0 {
		t.Fatalf("status=ai_draft search should be empty (staging invisible): %+v", byStatus)
	}

	// 搜索：大纲代码前缀（q5/q1 均为暂存草稿，不可见）
	byOutline := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions/search?outline_code=110.4.3", adminToken, "198.51.100.1", nil).Body.String())
	if byOutline.Total != 0 {
		t.Fatalf("outline search should hide staging drafts: %+v", byOutline)
	}

	// 题库题目列表（多对多 + 关键词）：题库列表仅覆盖检查通过的待审核题；
	// 暂存的 q1（心肌梗死）在检查通过前同样不可见；formal 的 q2 密封不可见。
	inBank := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/banks/bank-a/questions?q=%E5%BF%83%E8%82%8C%E6%A2%97%E6%AD%BB", adminToken, "198.51.100.1", nil).Body.String())
	if inBank.Total != 0 {
		t.Fatalf("bank question search wrong (staging draft should be hidden): %+v", inBank)
	}
	formalInBank := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/banks/bank-a/questions?q=%E8%83%83%E6%BA%83%E7%96%A1", adminToken, "198.51.100.1", nil).Body.String())
	if formalInBank.Total != 0 {
		t.Fatalf("formal question leaked into bank listing: %+v", formalInBank)
	}

	// 受限题库范围用户：只看到 bank-b 的题目
	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "scope-user", "password": "scope-password", "display_name": "受限用户",
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registration.Code, registration.Body.String())
	}
	var registered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registration.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		// question:view 现仅覆盖过程题库；跨分层可见需同时授予正式/淘汰题库查看权限
		"display_name": "受限用户", "role": "",
		"permissions": []string{domain.PermQuestionView, domain.PermQuestionViewFormal, domain.PermQuestionViewEliminated},
		"bank_ids":    []string{"bank-b"}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign scope status=%d body=%s", updated.Code, updated.Body.String())
	}
	scopeToken := loginForAuthTest(t, handler, "scope-user", "scope-password", "203.0.113.10")
	scoped := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions", scopeToken, "203.0.113.10", nil).Body.String())
	if scoped.Total != 2 || len(scoped.Questions) != 2 {
		t.Fatalf("scoped list wrong: %+v", scoped)
	}
	for _, q := range scoped.Questions {
		if q.ID != "q2" && q.ID != "q4" {
			t.Fatalf("scoped list leaked question outside bank-b: %s", q.ID)
		}
	}
}
