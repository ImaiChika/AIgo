package api

import (
	"aigo/internal/domain"
	"aigo/internal/knowledge"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"aigo/internal/storage/postgres"
)

func TestKnowledgeVersionAPIAndPermissions(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.kpSvc = knowledge.NewService(store)
	handler := server.Handler()
	admin := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.9")
	post := serveAuthJSON(t, handler, "POST", "/api/knowledge-versions", admin, "198.51.100.9", map[string]any{"name": "2026 年大纲", "year": 2026})
	if post.Code != 201 {
		t.Fatalf("create version: %d %s", post.Code, post.Body.String())
	}
	var v domain.KnowledgeVersion
	json.Unmarshal(post.Body.Bytes(), &v)
	emptyPublish := serveAuthJSON(t, handler, "POST", "/api/knowledge-versions/"+v.ID+"/publish", admin, "198.51.100.9", nil)
	if emptyPublish.Code != 400 {
		t.Fatal("published empty syllabus")
	}
	point := map[string]any{"version_id": v.ID, "category": "临床综合", "subject": "呼吸系统", "unit": "肺部感染", "sub_item": "肺炎", "topic": "诊断", "outline_code": "001"}
	created := serveAuthJSON(t, handler, "POST", "/api/knowledge-points", admin, "198.51.100.9", point)
	if created.Code != 201 {
		t.Fatalf("create point: %d %s", created.Code, created.Body.String())
	}
	var p domain.KnowledgePoint
	json.Unmarshal(created.Body.Bytes(), &p)
	duplicate := serveAuthJSON(t, handler, "POST", "/api/knowledge-points", admin, "198.51.100.9", point)
	if duplicate.Code != 409 {
		t.Fatalf("duplicate create must not update: %d", duplicate.Code)
	}
	published := serveAuthJSON(t, handler, "POST", "/api/knowledge-versions/"+v.ID+"/publish", admin, "198.51.100.9", nil)
	if published.Code != 200 {
		t.Fatal(published.Body.String())
	}
	listed := serveAuthJSON(t, handler, "GET", "/api/knowledge-points/search?q=诊断&page=9223372036854775807", admin, "198.51.100.9", nil)
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), p.ID) {
		t.Fatalf("default scoped pagination: %d %s", listed.Code, listed.Body.String())
	}
	p.Topic = "诊断修订"
	edit := serveAuthJSON(t, handler, "PUT", "/api/knowledge-points/"+p.ID, admin, "198.51.100.9", p)
	if edit.Code != 200 {
		t.Fatal(edit.Body.String())
	}
	stale := serveAuthJSON(t, handler, "PUT", "/api/knowledge-points/"+p.ID, admin, "198.51.100.9", p)
	if stale.Code != 409 {
		t.Fatalf("stale update: %d", stale.Code)
	}
	registration := serveAuthJSON(t, handler, "POST", "/api/auth/register", "", "198.51.100.10", map[string]any{"username": "readonly-kp", "password": "readonly-password", "display_name": "读者"})
	if registration.Code != 201 {
		t.Fatal(registration.Body.String())
	}
	reader := loginForAuthTest(t, handler, "readonly-kp", "readonly-password", "198.51.100.10")
	for _, request := range []struct {
		method, path string
		body         any
	}{{"DELETE", "/api/knowledge-versions/" + v.ID, nil}, {"POST", "/api/knowledge-versions", map[string]any{"name": "非法版本", "year": 2027}}, {"POST", "/api/knowledge-points", point}, {"PUT", "/api/knowledge-points/" + p.ID, p}, {"DELETE", "/api/knowledge-points/" + p.ID, nil}, {"POST", "/api/knowledge-versions/" + v.ID + "/publish", nil}, {"POST", "/api/knowledge-points/import", nil}} {
		response := serveAuthJSON(t, handler, request.method, request.path, reader, "198.51.100.10", request.body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("read-only mutation allowed: %s %s = %d", request.method, request.path, response.Code)
		}
	}
	if response := serveAuthJSON(t, handler, "GET", "/api/knowledge-points/tree?version_id="+v.ID, reader, "198.51.100.10", nil); response.Code != 200 || !strings.Contains(response.Body.String(), "肺部感染") {
		t.Fatalf("read-only tree: %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'knowledge_%'`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("missing knowledge audit: %d %v", count, err)
	}
}

func TestExpertReadsKnowledgeAndOwnReviewResultsButNotStats(t *testing.T) {
	server, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.kpSvc = knowledge.NewService(server.questionStore.(*postgres.Store))
	handler := server.Handler()
	admin := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.20")
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", admin, "198.51.100.20", map[string]any{
		"username": "expert-readonly", "password": "expert-readonly-password", "display_name": "只读专家", "role": domain.RoleExpert,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create expert: status=%d body=%s", created.Code, created.Body.String())
	}
	expert := loginForAuthTest(t, handler, "expert-readonly", "expert-readonly-password", "198.51.100.21")
	me := serveAuthJSON(t, handler, http.MethodGet, "/api/auth/me", expert, "198.51.100.21", nil)
	if me.Code != http.StatusOK {
		t.Fatalf("expert profile status=%d body=%s", me.Code, me.Body.String())
	}
	var profile struct {
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{domain.PermQuestionView, domain.PermReviewDo} {
		if !slices.Contains(profile.Permissions, permission) {
			t.Fatalf("expert missing personal workspace permission %s: %v", permission, profile.Permissions)
		}
	}
	if slices.Contains(profile.Permissions, domain.PermBatchRun) {
		t.Fatalf("expert 默认不应拥有批量推理权限: %v", profile.Permissions)
	}
	// 审核记录汇总属于独立管理能力，审题老师只处理分配到自己的任务。
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=personal", expert, "198.51.100.21", nil); response.Code != http.StatusForbidden {
		t.Fatalf("expert should not access review results summary: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/review/results?scope=global", expert, "198.51.100.21", nil); response.Code != http.StatusForbidden {
		t.Fatalf("expert should not access global review results: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/stats", expert, "198.51.100.21", nil); response.Code != http.StatusForbidden {
		t.Fatalf("expert should not access stats: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/knowledge-points", expert, "198.51.100.21", nil); response.Code != http.StatusOK {
		t.Fatalf("expert should read knowledge points: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=personal", expert, "198.51.100.21", nil); response.Code != http.StatusOK {
		t.Fatalf("expert should access personal question bank: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=global", expert, "198.51.100.21", nil); response.Code != http.StatusForbidden {
		t.Fatalf("expert should not access global question bank: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := serveAuthJSON(t, handler, http.MethodPost, "/api/knowledge-points", expert, "198.51.100.21", map[string]any{"topic": "越权知识点"}); response.Code != http.StatusForbidden {
		t.Fatalf("expert should not mutate knowledge points: status=%d body=%s", response.Code, response.Body.String())
	}
}
