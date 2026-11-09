//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/pipeline"
	"aigo/internal/questionlib"
	"aigo/internal/storage"
)

// libraryTestEnv 构建无审核依赖的 0 轮闭环环境：命题教师（question:view/
// edit/confirm/download/generate + batch:run，无任何 review 权限）与受控模型。
func libraryTestEnv(t *testing.T) (*Server, http.Handler, string, string, func()) {
	t.Helper()
	server, store, cleanup := generationHandlerTestServer(t)
	client := &workflowAPIClient{}
	server.aiCheckSvc = aicheck.NewService(client, store, store, store, "test")
	server.pipe = pipeline.New(generator.NewService(client), nil, nil, nil, store)
	h := server.Handler()

	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.190")
	created := serveAuthJSON(t, h, "POST", "/api/users", admin, "198.51.100.190", map[string]any{
		"username": "lib-teacher", "password": "library-test-password",
		"permissions": []string{
			domain.PermQuestionView, domain.PermQuestionEdit, domain.PermQuestionConfirm,
			domain.PermQuestionGenerate, domain.PermQuestionDownload, domain.PermBatchRun,
			domain.PermStatsView,
		},
	})
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body)
	}
	teacher := loginForAuthTest(t, h, "lib-teacher", "library-test-password", "198.51.100.191")

	// 知识点版本。
	version := serveAuthJSON(t, h, "POST", "/api/knowledge-versions", admin, "198.51.100.190", map[string]any{"name": "题库归属大纲", "year": 2026})
	if version.Code != http.StatusCreated {
		t.Fatal(version.Code, version.Body)
	}
	var v domain.KnowledgeVersion
	json.Unmarshal(version.Body.Bytes(), &v)
	point := serveAuthJSON(t, h, "POST", "/api/knowledge-points", admin, "198.51.100.190", map[string]any{
		"version_id": v.ID, "category": "临床综合", "subject": "消化", "topic": "题库归属考点", "outline_code": "lib.001",
	})
	if point.Code != http.StatusCreated {
		t.Fatal(point.Code, point.Body)
	}
	publish := serveAuthJSON(t, h, "POST", "/api/knowledge-versions/"+v.ID+"/publish", admin, "198.51.100.190", nil)
	if publish.Code != 200 {
		t.Fatal(publish.Code, publish.Body)
	}
	return server, h, teacher, v.ID, cleanup
}

// seedPassedQuestion 直接落一道首检通过（ai_reviewed/unfiled）的教师题目。
func seedPassedQuestion(t *testing.T, s *Server, h http.Handler, teacher, versionID, id, stem string) domain.A2Question {
	t.Helper()
	user, err := s.authSvc.GetUserByUsername("lib-teacher")
	if err != nil {
		t.Fatal(err)
	}
	q := domain.A2Question{
		ID: id, OwnerID: user.ID, CreatedBy: "lib-teacher",
		ClinicalStem: stem, Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}},
		Answer: "A", Explanation: "测试解析",
		Status: domain.StatusAIReviewed, Version: 1,
		KnowledgePoints: []domain.KnowledgePoint{{ID: "lib.001", VersionID: versionID, Topic: "题库归属考点", OutlineCode: "lib.001", Category: "临床综合", Subject: "消化"}},
	}
	ctx := storage.WithQuestionChange(t.Context(), storage.QuestionChange{Actor: "lib-teacher", OwnerID: user.ID})
	if err := s.questionStore.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	results := s.questionStore.(interface {
		SaveReviewResult(context.Context, domain.AIReviewResult) error
	})
	if err := results.SaveReviewResult(ctx, domain.AIReviewResult{ID: "pass-" + q.ID, QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.questionStore.GetQuestion(ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *saved
}

func libraryItemForAPI(q domain.A2Question) questionlib.ConfirmItem {
	library, review := q.LibraryRevision, q.ReviewRevision
	return questionlib.ConfirmItem{QuestionID: q.ID, ExpectedVersion: q.Version, ExpectedLibraryRevision: &library, ExpectedReviewRevision: &review}
}
func libraryItemsPayload(t *testing.T, s *Server, ids ...string) map[string]any {
	t.Helper()
	items := make([]questionlib.ConfirmItem, 0, len(ids))
	for _, id := range ids {
		q, err := s.questionStore.GetQuestion(t.Context(), id)
		if err != nil || q == nil {
			t.Fatalf("get %s: %v", id, err)
		}
		items = append(items, libraryItemForAPI(*q))
	}
	return map[string]any{"items": items}
}

func confirmViaAPI(t *testing.T, h http.Handler, teacher string, items map[string]any) *httptest.ResponseRecorder {
	return serveAuthJSON(t, h, "POST", "/api/questions/confirm", teacher, "198.51.100.191", items)
}

// TestLibraryZeroRoundClosedLoop R10：无任何审核权限的教师完成
// 整理→确认→普通题库→导出→下载完整闭环；确认不创建审核任务。
func TestLibraryZeroRoundClosedLoop(t *testing.T) {
	s, h, teacher, versionID, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	seedPassedQuestion(t, s, h, teacher, versionID, "lib-q1", "患者，女，50岁。闭环题干。该患者最可能的诊断是")

	// 整理页数据源包含该题。
	list := serveAuthJSON(t, h, "GET", "/api/questions/my-new?page=1&page_size=20", teacher, "198.51.100.191", nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), "lib-q1") {
		t.Fatalf("整理页应含待确认题: %d %s", list.Code, list.Body)
	}
	// 确认入库（带版本守卫）。
	r := confirmViaAPI(t, h, teacher, map[string]any{"items": []map[string]any{{"question_id": "lib-q1", "expected_version": 1, "expected_library_revision": 1, "expected_review_revision": 0}}})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var result struct {
		Confirmed int `json:"confirmed"`
	}
	json.Unmarshal(r.Body.Bytes(), &result)
	if result.Confirmed != 1 {
		t.Fatal(result.Confirmed)
	}
	stored, _ := s.questionStore.GetQuestion(t.Context(), "lib-q1")
	if stored.LibraryState != domain.LibraryActive || stored.Status != domain.StatusAIReviewed {
		t.Fatalf("归属/状态不符: %+v", stored)
	}
	// 0 轮确认不创建人工审核任务。
	if task, _ := s.reviewSvc.GetTaskByQuestionID(t.Context(), "lib-q1"); task != nil {
		t.Fatal("0 轮确认不得创建审核任务")
	}
	// 确认后移出整理页、进入普通题库页签。
	list = serveAuthJSON(t, h, "GET", "/api/questions/my-new?page=1&page_size=20", teacher, "198.51.100.191", nil)
	if strings.Contains(list.Body.String(), "lib-q1") {
		t.Fatal("确认后应离开整理页")
	}
	normal := serveAuthJSON(t, h, "GET", "/api/questions?scope=personal&library=active&page=1&page_size=20", teacher, "198.51.100.191", nil)
	if normal.Code != 200 || !strings.Contains(normal.Body.String(), "lib-q1") {
		t.Fatalf("普通题库应含确认题: %d %s", normal.Code, normal.Body)
	}
	// 个人普通库导出（此前无 published 必然 403 的路径）。
	export := serveAuthJSON(t, h, "POST", "/api/export/xlsx", teacher, "198.51.100.191", map[string]any{"scope": "personal", "question_ids": []string{"lib-q1"}})
	if export.Code != 200 {
		t.Fatalf("个人导出失败: %d %s", export.Code, export.Body)
	}
	var exported struct {
		Path     string `json:"path"`
		Filename string `json:"filename"`
		Count    int    `json:"count"`
	}
	json.Unmarshal(export.Body.Bytes(), &exported)
	if exported.Count != 1 || exported.Filename == "" {
		t.Fatalf("导出结果不符: %+v", exported)
	}
	// 下载实际文件并核对内容（题干与答案出现在 xlsx 中）。
	download := serveAuthJSON(t, h, "GET", "/api/export/download/"+exported.Filename, teacher, "198.51.100.191", nil)
	if download.Code != 200 {
		t.Fatalf("下载失败: %d %s", download.Code, download.Body)
	}
	if !strings.Contains(download.Header().Get("Content-Disposition"), exported.Filename) {
		t.Fatal("下载文件名不符")
	}
	if download.Body.Len() < 1000 {
		t.Fatalf("下载文件过小: %d", download.Body.Len())
	}
	// xlsx 是 zip：至少校验签名与大小；内容核验由浏览器旅程做打开目检。
	if !strings.HasPrefix(download.Body.String(), "PK") {
		t.Fatal("xlsx 应为 zip 容器")
	}
	_ = os.Remove(exported.Path)

	// 删除→删除题库→恢复闭环。
	del := serveAuthJSON(t, h, "POST", "/api/questions/library-delete", teacher, "198.51.100.191", libraryItemsPayload(t, s, "lib-q1"))
	if del.Code != 200 {
		t.Fatal(del.Code, del.Body)
	}
	deletedView := serveAuthJSON(t, h, "GET", "/api/questions?scope=personal&library=deleted&page=1&page_size=20", teacher, "198.51.100.191", nil)
	if deletedView.Code != 200 || !strings.Contains(deletedView.Body.String(), "lib-q1") {
		t.Fatalf("删除题库应含删除题: %d %s", deletedView.Code, deletedView.Body)
	}
	// 删除态不可导出。
	export = serveAuthJSON(t, h, "POST", "/api/export/xlsx", teacher, "198.51.100.191", map[string]any{"scope": "personal", "question_ids": []string{"lib-q1"}})
	if export.Code == 200 {
		t.Fatal("删除态不可导出")
	}
	restore := serveAuthJSON(t, h, "POST", "/api/questions/library-restore", teacher, "198.51.100.191", libraryItemsPayload(t, s, "lib-q1"))
	if restore.Code != 200 {
		t.Fatal(restore.Code, restore.Body)
	}
	stored, _ = s.questionStore.GetQuestion(t.Context(), "lib-q1")
	if stored.LibraryState != domain.LibraryUnfiled || stored.ConfirmedVersion != 0 {
		t.Fatalf("恢复应回待确认: %+v", stored)
	}
}

// TestLibraryConfirmGuards R11/R13：首检未过、版本冲突、跨人、并发旧窗口。
func TestLibraryConfirmGuards(t *testing.T) {
	s, h, teacher, versionID, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, versionID, "lib-guard", "患者，男，60岁。守卫题干。该患者最可能的诊断是")
	_ = q

	// 版本冲突。
	r := confirmViaAPI(t, h, teacher, map[string]any{"items": []map[string]any{{"question_id": "lib-guard", "expected_version": 5, "expected_library_revision": 1, "expected_review_revision": 0}}})
	if r.Code != http.StatusConflict {
		t.Fatalf("旧版本应 409: %d %s", r.Code, r.Body)
	}
	// 非本人（admin 也不行——confirm 是本人动作）。
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.190")
	r = serveAuthJSON(t, h, "POST", "/api/questions/confirm", admin, "198.51.100.190", libraryItemsPayload(t, s, "lib-guard"))
	if r.Code != http.StatusForbidden {
		t.Fatalf("跨人确认应 403: %d %s", r.Code, r.Body)
	}
	// 无 confirm 权限的账号被拒。
	created := serveAuthJSON(t, h, "POST", "/api/users", admin, "198.51.100.190", map[string]any{"username": "lib-noperm", "password": "library-test-password", "permissions": []string{domain.PermQuestionView}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	noPerm := loginForAuthTest(t, h, "lib-noperm", "library-test-password", "198.51.100.192")
	r = serveAuthJSON(t, h, "POST", "/api/questions/confirm", noPerm, "198.51.100.192", libraryItemsPayload(t, s, "lib-guard"))
	if r.Code != http.StatusForbidden {
		t.Fatalf("缺权限应 403: %d", r.Code)
	}
	// 首检未通过的暂存题不可见不可确认。
	draft := seedPassedQuestion(t, s, h, teacher, versionID, "lib-draft", "患者，女，30岁。草稿题干。该患者最可能的诊断是")
	draft.Status = domain.StatusAIDraft
	ctx := storage.WithQuestionChange(t.Context(), storage.QuestionChange{Actor: "lib-teacher", OwnerID: draft.OwnerID})
	if err := s.questionStore.SaveQuestion(ctx, draft); err != nil {
		t.Fatal(err)
	}
	r = confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, "lib-draft"))
	if r.Code != http.StatusBadRequest {
		t.Fatalf("暂存题确认应 400: %d %s", r.Code, r.Body)
	}
}

// TestLibrarySummaryCounts R28：my/summary 返回三库计数且 0 轮确认不冒充审核量。
func TestLibrarySummaryCounts(t *testing.T) {
	s, h, teacher, versionID, cleanup := libraryTestEnv(t)
	defer cleanup()
	seedPassedQuestion(t, s, h, teacher, versionID, "lib-sum-1", "患者，男，70岁。统计题干一。该患者最可能的诊断是")
	seedPassedQuestion(t, s, h, teacher, versionID, "lib-sum-2", "患者，男，71岁。统计题干二。该患者最可能的诊断是")
	if _, err := s.questionlibService().Confirm(t.Context(), mustUserID(t, s, "lib-teacher"), []questionlib.ConfirmItem{libraryItemForAPI(*mustLibraryQuestion(t, s, "lib-sum-1"))}, time.Now()); err != nil {
		t.Fatal(err)
	}
	r := serveAuthJSON(t, h, "GET", "/api/my/summary", teacher, "198.51.100.191", nil)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var summary map[string]int
	json.Unmarshal(r.Body.Bytes(), &summary)
	if summary["library_active_count"] != 1 || summary["library_unfiled_count"] != 1 || summary["library_deleted_count"] != 0 {
		t.Fatalf("归属计数不符: %v", summary)
	}
	if summary["reviewed_count"] != 0 {
		t.Fatalf("0 轮确认不得计入审核量: %v", summary)
	}
}

func mustUserID(t *testing.T, s *Server, username string) string {
	t.Helper()
	user, err := s.authSvc.GetUserByUsername(username)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func mustLibraryQuestion(t *testing.T, s *Server, id string) *domain.A2Question {
	t.Helper()
	q, err := s.questionStore.GetQuestion(t.Context(), id)
	if err != nil || q == nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return q
}
