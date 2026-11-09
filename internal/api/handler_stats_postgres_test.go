// /api/stats 数据统计接口集成测试：
// 验证存储端聚合的正确性、题库分层权限对统计的隔离、以及新旧字段的语义。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/bank"
	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/postgres"
	"aigo/internal/storage/testutil"
)

func seedStatsFixtures(t *testing.T, store *postgres.Store) {
	t.Helper()
	ctx := t.Context()
	if err := store.SaveBank(ctx, domain.QuestionBank{ID: "stats-bank-a", Name: "统计内科库", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	seed := []domain.A2Question{
		// 待审核层：3 题（2 题在 bank-a，1 题未分类）
		{ID: "stats-w1", Status: domain.StatusAIReviewed, Difficulty: "0.65", Profession: "内科", ClinicalStem: "统计-过程题1", Answer: "A", BankIDs: []string{"stats-bank-a"}},
		{ID: "stats-w2", Status: domain.StatusReviewing, Difficulty: "0.70", Profession: "内科", ClinicalStem: "统计-过程题2", Answer: "B", BankIDs: []string{"stats-bank-a"}},
		{ID: "stats-w3", Status: domain.StatusAIDraft, Difficulty: "0.65", Profession: "外科", ClinicalStem: "统计-过程题3-未分类", Answer: "C"},
		// 正式层
		{ID: "stats-f1", Status: domain.StatusPublished, Difficulty: "0.75", Profession: "外科", ClinicalStem: "统计-正式题1", Answer: "A", BankIDs: []string{"stats-bank-a"}},
		{ID: "stats-f2", Status: domain.StatusPublished, Difficulty: "0.75", Profession: "内科", ClinicalStem: "统计-正式题2", Answer: "B"},
		// 淘汰层
		{ID: "stats-e1", Status: domain.StatusRejected, Difficulty: "0.85", Profession: "儿科", ClinicalStem: "统计-淘汰题1", Answer: "D"},
	}
	base := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	for i, q := range seed {
		q.Version = 1
		if q.Status == domain.StatusPublished {
			q.LibraryState = domain.LibraryActive
			q.ConfirmedVersion = q.Version
			q.ConfirmationSource = domain.ConfirmSourceLegacy
		}
		q.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		q.UpdatedAt = q.CreatedAt
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
		shareStatus := domain.QuestionSharePending
		switch q.Status {
		case domain.StatusPublished:
			shareStatus = domain.QuestionShareApproved
		case domain.StatusRejected:
			shareStatus = domain.QuestionShareRejected
		case domain.StatusAIDraft:
			continue
		}
		// Historical ownerless shares already migrated to C4 carry immutable
		// submission evidence. A raw share row without it must fail closed.
		snapshot, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().ExecContext(ctx, `
			INSERT INTO question_share_requests(id,question_id,owner_id,status,reviewed_by,created_at,question_version,library_revision,question_snapshot,snapshot_search_text)
			VALUES($1,$2,'',$3,'migration',$4,1,1,$5,$6)`, "share-"+q.ID, q.ID, shareStatus, q.CreatedAt, snapshot, storage.BuildQuestionSearchText(q)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatsHandlerAggregates(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedStatsFixtures(t, store)
	// AI 检查统计：2 条通过 + 1 条发现问题 + 1 条淘汰留档
	aiReviewStore := testutil.NewMemoryAIReviewStore()
	ctx := t.Context()
	for i, verdict := range []string{"pass", "pass", "issues_found"} {
		if err := aiReviewStore.SaveReviewResult(ctx, domain.AIReviewResult{
			ID: fmt.Sprintf("air-%d", i), QuestionID: "stats-w1", Verdict: verdict,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := aiReviewStore.SaveDiscardResult(ctx, domain.AICheckDiscard{
		ID: "air-gone-1", QuestionID: "stats-gone", Verdict: "issues_found", StemSummary: "已淘汰题目摘要",
	}); err != nil {
		t.Fatal(err)
	}
	server.aiCheckSvc = aicheck.NewService(nil, store, aiReviewStore, nil, "test-model")
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	resp := serveAuthJSON(t, handler, http.MethodGet, "/api/stats", adminToken, "203.0.113.10", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/stats status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		QuestionCount          int            `json:"question_count"`
		StatusDistribution     map[string]int `json:"status_distribution"`
		DifficultyDistribution map[string]int `json:"difficulty_distribution"`
		BankDistribution       map[string]int `json:"bank_distribution"`
		TierCounts             map[string]int `json:"tier_counts"`
		ProfessionDistribution map[string]int `json:"profession_distribution"`
		CreationTrend          map[string]int `json:"creation_trend"`
		KPCovered              int            `json:"kp_covered"`
		AICheck                struct {
			Discarded int            `json:"discarded"`
			Verdicts  map[string]int `json:"verdicts"`
			Tasks     map[string]int `json:"tasks"`
		} `json:"ai_check"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode stats: %v body=%s", err, resp.Body.String())
	}

	// 管理员全范围：待审核层 = 检查通过后的过程题（2 题）；暂存草稿不可见也不计数
	if payload.QuestionCount != 2 {
		t.Fatalf("question_count=%d, want 2", payload.QuestionCount)
	}
	// 状态分布同样按可见口径统计：暂存 ai_draft 不出现
	// Pending sharing is a global collection tier. It does not disclose the
	// personal source's optional-review task state.
	if payload.StatusDistribution["ai_reviewed"] != 2 || payload.StatusDistribution["reviewing"] != 0 {
		t.Fatalf("status_distribution wrong: %v", payload.StatusDistribution)
	}
	if payload.StatusDistribution["ai_draft"] != 0 {
		t.Fatalf("暂存草稿不应出现在状态分布: %v", payload.StatusDistribution)
	}
	if payload.TierCounts["working"] != 2 || payload.TierCounts["formal"] != 2 || payload.TierCounts["eliminated"] != 1 {
		t.Fatalf("tier_counts wrong: %v", payload.TierCounts)
	}
	if payload.BankDistribution["stats-bank-a"] != 2 {
		t.Fatalf("bank_distribution wrong: %v", payload.BankDistribution)
	}
	// 暂存草稿（未分类）不可见，不应产生“待归类”桶
	if payload.BankDistribution["待归类"] != 0 {
		t.Fatalf("staging draft should not appear as 待归类: %v", payload.BankDistribution)
	}
	// 专业分布：内科 = w1、w2、f2；外科暂存草稿 w3 不可见
	if payload.ProfessionDistribution["内科"] != 2 || payload.ProfessionDistribution["外科"] != 0 {
		t.Fatalf("profession_distribution wrong: %v", payload.ProfessionDistribution)
	}
	if len(payload.CreationTrend) == 0 {
		t.Fatal("creation_trend should not be empty for seeded questions")
	}
	if payload.AICheck.Discarded != 1 {
		t.Fatalf("ai_check.discarded=%d, want 1", payload.AICheck.Discarded)
	}
	if payload.AICheck.Verdicts["pass"] != 2 || payload.AICheck.Verdicts["issues_found"] != 1 {
		t.Fatalf("ai_check.verdicts wrong: %v", payload.AICheck.Verdicts)
	}
}

func TestStatsHandlerScopedAndPermissionGated(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedStatsFixtures(t, store)
	server.bankSvc = bank.NewService(store, store)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	adminUser, err := server.authSvc.GetUserByUsername("admin")
	if err != nil || adminUser == nil {
		t.Fatalf("load admin for personal stats fixture: user=%v err=%v", adminUser, err)
	}
	if err := store.SaveQuestion(t.Context(), domain.A2Question{
		ID: "stats-personal-admin", Status: domain.StatusAIReviewed, Difficulty: "0.65",
		Profession: "管理员个人题", ClinicalStem: "管理员个人统计题", Answer: "A", OwnerID: adminUser.ID, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	globalResp := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=global", adminToken, "198.51.100.1", nil)
	if globalResp.Code != http.StatusOK {
		t.Fatalf("global stats status=%d body=%s", globalResp.Code, globalResp.Body.String())
	}
	var globalPayload struct {
		QuestionCount int            `json:"question_count"`
		TierCounts    map[string]int `json:"tier_counts"`
	}
	if err := json.Unmarshal(globalResp.Body.Bytes(), &globalPayload); err != nil {
		t.Fatal(err)
	}
	// 全局统计只计分享状态：管理员未分享的个人题不进入全局待审核库；
	// 暂存草稿（stats-w3）也不进入任何可见分层。
	if globalPayload.QuestionCount != 2 || globalPayload.TierCounts["working"] != 2 || globalPayload.TierCounts["formal"] != 2 || globalPayload.TierCounts["eliminated"] != 1 {
		t.Fatalf("global stats should match shared bank tiers: %+v", globalPayload)
	}
	personalResp := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=personal", adminToken, "198.51.100.1", nil)
	if personalResp.Code != http.StatusOK {
		t.Fatalf("personal stats status=%d body=%s", personalResp.Code, personalResp.Body.String())
	}
	var personalPayload struct {
		QuestionCount int `json:"question_count"`
	}
	if err := json.Unmarshal(personalResp.Body.Bytes(), &personalPayload); err != nil {
		t.Fatal(err)
	}
	if personalPayload.QuestionCount != 1 {
		t.Fatalf("personal stats did not isolate admin question: %+v", personalPayload)
	}

	// 仅有统计+过程题库查看权限、限定 bank-a 的用户（未传 scope 的旧客户端口径）：
	// 只统计 bank-a 范围内的 2 题（本人+历史无归属），且看不到正式/淘汰层。
	scopedToken := createScopedTierUser(t, handler, adminToken, "stats-scoped", "", []string{
		domain.PermStatsView, domain.PermQuestionView,
	}, []string{"stats-bank-a"})
	resp := serveAuthJSON(t, handler, http.MethodGet, "/api/stats", scopedToken, "203.0.113.10", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("scoped GET /api/stats status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		QuestionCount      int            `json:"question_count"`
		TierCounts         map[string]int `json:"tier_counts"`
		BankDistribution   map[string]int `json:"bank_distribution"`
		StatusDistribution map[string]int `json:"status_distribution"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.QuestionCount != 2 {
		t.Fatalf("scoped question_count=%d, want 2", payload.QuestionCount)
	}
	if _, ok := payload.TierCounts["formal"]; ok {
		t.Fatalf("scoped user should not see formal tier count: %v", payload.TierCounts)
	}
	if _, ok := payload.TierCounts["eliminated"]; ok {
		t.Fatalf("scoped user should not see eliminated tier count: %v", payload.TierCounts)
	}
	if payload.BankDistribution["待归类"] != 0 {
		t.Fatalf("scoped user should not see unclassified questions: %v", payload.BankDistribution)
	}

	// 只有统计权限、没有过程题库查看权限的用户：题目统计应为空而非报错
	statsOnlyToken := createScopedTierUser(t, handler, adminToken, "stats-only", "", []string{
		domain.PermStatsView,
	}, nil)
	resp2 := serveAuthJSON(t, handler, http.MethodGet, "/api/stats", statsOnlyToken, "203.0.113.10", nil)
	if resp2.Code != http.StatusOK {
		t.Fatalf("stats-only GET /api/stats status=%d body=%s", resp2.Code, resp2.Body.String())
	}
	var payload2 struct {
		QuestionCount int `json:"question_count"`
	}
	if err := json.Unmarshal(resp2.Body.Bytes(), &payload2); err != nil {
		t.Fatal(err)
	}
	if payload2.QuestionCount != 0 {
		t.Fatalf("stats-only user question_count=%d, want 0", payload2.QuestionCount)
	}
}

// 数据统计入口与题库同口径 + 个人范围 AI 检查统计 + 用户数门控：
// 1) 仅持有 question:view 的老师（审题身份）也能进入数据统计；
// 2) “我的数据”下 AI 质量检查按题目归属人过滤，个人题库有题就有检查数据；
// 3) 用户数仅“全局数据 + 用户管理权限”返回，个人范围与普通老师不返回该字段。
func TestStatsTierEntryPersonalAICheckAndUserCountGating(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	seedStatsFixtures(t, store)
	server.aiCheckSvc = aicheck.NewService(nil, store, store, store, "test-model")
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	adminUser, err := server.authSvc.GetUserByUsername("admin")
	if err != nil || adminUser == nil {
		t.Fatalf("load admin: user=%v err=%v", adminUser, err)
	}

	// 审题身份：只有 question:view（没有 stats:view），此前连接口都会 403。
	viewerToken := createScopedTierUser(t, handler, adminToken, "stats-viewer", "", []string{
		domain.PermQuestionView,
	}, nil)
	resp := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=personal", viewerToken, "203.0.113.10", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("question:view-only user GET /api/stats status=%d body=%s", resp.Code, resp.Body.String())
	}

	viewerUser, err := server.authSvc.GetUserByUsername("stats-viewer")
	if err != nil || viewerUser == nil {
		t.Fatalf("load viewer: user=%v err=%v", viewerUser, err)
	}
	// 造数据：viewer 名下 1 题（有检查结论 + 淘汰留档），admin 名下 1 题。
	ctx := t.Context()
	if err := store.SaveQuestion(ctx, domain.A2Question{
		ID: "stats-own-q1", Status: domain.StatusAIReviewed, Difficulty: "0.65",
		Profession: "内科", ClinicalStem: "个人统计-归属题", Answer: "A", OwnerID: viewerUser.ID, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveQuestion(ctx, domain.A2Question{
		ID: "stats-own-q2", Status: domain.StatusAIReviewed, Difficulty: "0.65",
		Profession: "内科", ClinicalStem: "个人统计-管理员题", Answer: "A", OwnerID: adminUser.ID, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(ctx, domain.AIReviewResult{ID: "own-air-1", QuestionID: "stats-own-q1", Verdict: "pass", QuestionVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(ctx, domain.AIReviewResult{ID: "own-air-2", QuestionID: "stats-own-q2", Verdict: "reject", QuestionVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDiscardResult(ctx, domain.AICheckDiscard{
		ID: "own-disc-1", QuestionID: "stats-gone-viewer", Verdict: "reject", OwnerID: viewerUser.ID, StemSummary: "个人淘汰留档",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDiscardResult(ctx, domain.AICheckDiscard{
		ID: "own-disc-2", QuestionID: "stats-gone-admin", Verdict: "reject", OwnerID: adminUser.ID, StemSummary: "管理员淘汰留档",
	}); err != nil {
		t.Fatal(err)
	}

	personal := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=personal", viewerToken, "203.0.113.10", nil)
	if personal.Code != http.StatusOK {
		t.Fatalf("viewer personal stats status=%d body=%s", personal.Code, personal.Body.String())
	}
	var personalPayload struct {
		QuestionCount int `json:"question_count"`
		AICheck       struct {
			Discarded int            `json:"discarded"`
			Verdicts  map[string]int `json:"verdicts"`
		} `json:"ai_check"`
		UserCount *int `json:"user_count"`
	}
	if err := json.Unmarshal(personal.Body.Bytes(), &personalPayload); err != nil {
		t.Fatal(err)
	}
	if personalPayload.QuestionCount != 1 {
		t.Fatalf("viewer personal question_count=%d, want 1（仅本人归属题）", personalPayload.QuestionCount)
	}
	if personalPayload.AICheck.Verdicts["pass"] != 1 || personalPayload.AICheck.Verdicts["reject"] != 0 {
		t.Fatalf("viewer personal ai_check.verdicts=%v, want 仅本人检查结论 pass=1", personalPayload.AICheck.Verdicts)
	}
	if personalPayload.AICheck.Discarded != 1 {
		t.Fatalf("viewer personal ai_check.discarded=%d, want 1（仅本人淘汰留档）", personalPayload.AICheck.Discarded)
	}
	if personalPayload.UserCount != nil {
		t.Fatalf("普通老师个人范围不应返回 user_count: %v", *personalPayload.UserCount)
	}

	// 管理员：全局范围返回 user_count；个人范围不返回（系统级指标不属于“我的数据”）。
	global := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=global", adminToken, "198.51.100.1", nil)
	if global.Code != http.StatusOK {
		t.Fatalf("admin global stats status=%d", global.Code)
	}
	var globalPayload struct {
		UserCount *int `json:"user_count"`
	}
	if err := json.Unmarshal(global.Body.Bytes(), &globalPayload); err != nil {
		t.Fatal(err)
	}
	if globalPayload.UserCount == nil || *globalPayload.UserCount < 2 {
		t.Fatalf("admin global should include user_count>=2: %v", globalPayload.UserCount)
	}
	adminPersonal := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=personal", adminToken, "198.51.100.1", nil)
	if adminPersonal.Code != http.StatusOK {
		t.Fatalf("admin personal stats status=%d", adminPersonal.Code)
	}
	var adminPersonalPayload struct {
		UserCount *int `json:"user_count"`
	}
	if err := json.Unmarshal(adminPersonal.Body.Bytes(), &adminPersonalPayload); err != nil {
		t.Fatal(err)
	}
	if adminPersonalPayload.UserCount != nil {
		t.Fatalf("个人范围不应返回系统级 user_count: %v", *adminPersonalPayload.UserCount)
	}
}

// 双身份账号个人统计身份无关性回归：
// 审题老师+命题教师双身份账号名下三层各有题目时，无论当前身份是 expert
// 还是 teacher，/api/stats?scope=personal 的分层计数必须完全一致——
// 个人数据属于账号个体，切换身份只是工作视角切换。
// 回归背景：个人范围正式/淘汰层此前按当前身份的 question:view_formal /
// question:view_eliminated 门控，审题身份（无这两个权限）下正式题库计数
// 缺失，前端显示"-"，与个人题库页（两身份都可见）自相矛盾。
func TestPersonalStatsIdentityIndependent(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.aiCheckSvc = aicheck.NewService(nil, store, store, store, "test-model")
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	// 注册并挂载 审题老师+命题教师 双身份（无直接权限，纯模板权限）。
	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.10", map[string]any{
		"username": "stats-duo", "password": "stats-duo-password", "display_name": "双身份统计",
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("register stats-duo status=%d body=%s", registration.Code, registration.Body.String())
	}
	var registered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registration.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	assigned := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+registered.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "双身份统计", "role": "expert", "roles": []string{"expert", "teacher"},
		"permissions": []string{}, "bank_ids": []string{}, "enabled": true,
	})
	if assigned.Code != http.StatusOK {
		t.Fatalf("assign dual roles status=%d body=%s", assigned.Code, assigned.Body.String())
	}
	duoUser, err := server.authSvc.GetUserByUsername("stats-duo")
	if err != nil || duoUser == nil {
		t.Fatalf("load stats-duo: user=%v err=%v", duoUser, err)
	}

	// 名下三层各放一题：正式（published）、待审（ai_reviewed）、淘汰（rejected）。
	ctx := t.Context()
	for i, tc := range []struct {
		id     string
		status domain.QuestionStatus
	}{
		{"stats-duo-formal", domain.StatusPublished},
		{"stats-duo-working", domain.StatusAIReviewed},
		{"stats-duo-eliminated", domain.StatusRejected},
	} {
		if err := store.SaveQuestion(ctx, domain.A2Question{
			ID: tc.id, Status: tc.status, OwnerID: duoUser.ID, Version: 1,
			ClinicalStem: "双身份统计-" + tc.id, Answer: "A", CreatedAt: time.Now().Add(time.Duration(i) * time.Second), UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	loginResp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/login", "", "203.0.113.10", map[string]any{
		"username": "stats-duo", "password": "stats-duo-password",
	})
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login stats-duo status=%d", loginResp.Code)
	}
	var loginPayload struct {
		Token string `json:"token"`
		User  struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(loginResp.Body.Bytes(), &loginPayload); err != nil {
		t.Fatal(err)
	}
	if loginPayload.User.Role != "teacher" {
		t.Fatalf("fresh dual login should default to teacher, got %q", loginPayload.User.Role)
	}
	// Keep testing the real expert branch rather than measuring teacher twice.
	expertResp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", loginPayload.Token, "203.0.113.10", map[string]any{"role": "expert"})
	if expertResp.Code != http.StatusOK || json.Unmarshal(expertResp.Body.Bytes(), &loginPayload) != nil || loginPayload.User.Role != "expert" {
		t.Fatalf("explicit expert switch status=%d body=%s", expertResp.Code, expertResp.Body)
	}

	fetchPersonal := func(token string) map[string]int {
		t.Helper()
		resp := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=personal", token, "203.0.113.10", nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("personal stats status=%d body=%s", resp.Code, resp.Body.String())
		}
		var payload struct {
			QuestionCount int            `json:"question_count"`
			TierCounts    map[string]int `json:"tier_counts"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		payload.TierCounts["question_count"] = payload.QuestionCount
		return payload.TierCounts
	}

	expertCounts := fetchPersonal(loginPayload.Token)
	want := map[string]int{"working": 1, "formal": 1, "eliminated": 1, "question_count": 1}
	for key, n := range want {
		if expertCounts[key] != n {
			t.Fatalf("expert identity personal stats[%s]=%d, want %d（审题身份也必须看到本人正式题计数）: %v", key, expertCounts[key], n, expertCounts)
		}
	}

	switchResp := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/switch-role", loginPayload.Token, "203.0.113.10", map[string]any{
		"role": "teacher",
	})
	if switchResp.Code != http.StatusOK {
		t.Fatalf("switch to teacher status=%d body=%s", switchResp.Code, switchResp.Body.String())
	}
	var switchPayload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(switchResp.Body.Bytes(), &switchPayload); err != nil {
		t.Fatal(err)
	}
	teacherCounts := fetchPersonal(switchPayload.Token)
	for key, n := range want {
		if teacherCounts[key] != n {
			t.Fatalf("teacher identity personal stats[%s]=%d, want %d: %v", key, teacherCounts[key], n, teacherCounts)
		}
		if teacherCounts[key] != expertCounts[key] {
			t.Fatalf("personal stats[%s] differs by identity: expert=%d teacher=%d（个人统计不得随身份变化）", key, expertCounts[key], teacherCounts[key])
		}
	}
}
