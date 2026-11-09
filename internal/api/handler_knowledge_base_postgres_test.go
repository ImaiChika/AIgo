//go:build integration

package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestKnowledgeBasePermissionMatrixAndLiveRevocation(t *testing.T) {
	server, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	server.knowledgeBaseSvc = &knowledgebase.Service{Store: store}
	h := server.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.160")
	create := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "kb-test", "password": "kb-test-password", "permissions": []string{domain.PermKBView, domain.PermKBManage, domain.PermKBUse}})
	if create.Code != 201 {
		t.Fatal(create.Code, create.Body)
	}
	var user auth.User
	if e := json.Unmarshal(create.Body.Bytes(), &user); e != nil {
		t.Fatal(e)
	}
	token := loginForAuthTest(t, h, "kb-test", "kb-test-password", "198.51.100.161")
	for mask := 0; mask < 8; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			p := []string{}
			for i, code := range []string{domain.PermKBView, domain.PermKBManage, domain.PermKBUse} {
				if mask&(1<<i) != 0 {
					p = append(p, code)
				}
			}
			grant := serveGenerateJSON(t, h, "PUT", "/api/users/"+user.ID, admin, map[string]any{"display_name": user.Username, "roles": []string{}, "permissions": p, "enabled": true})
			if grant.Code != 200 {
				t.Fatal(grant.Code, grant.Body)
			}
			list := serveGenerateJSON(t, h, "GET", "/api/knowledge-base", token, nil)
			want := http.StatusForbidden
			if mask&1 != 0 {
				want = 200
			}
			if list.Code != want {
				t.Fatal(list.Code, list.Body)
			}
			save := serveGenerateJSON(t, h, "POST", "/api/knowledge-base", token, map[string]any{"title": "测试卡", "kind": "custom", "content": map[string]string{"custom_text": "仅测试"}})
			want = 403
			if mask&3 == 3 {
				want = 200
			}
			if save.Code != want {
				t.Fatal(save.Code, save.Body)
			}
			if mask&1 != 0 {
				var result struct {
					Capabilities knowledgebase.Capabilities `json:"capabilities"`
				}
				if e := json.Unmarshal(list.Body.Bytes(), &result); e != nil {
					t.Fatal(e)
				}
				if result.Capabilities.Manage != (mask&3 == 3) || result.Capabilities.Use != (mask&5 == 5) {
					t.Fatal(result)
				}
			}
		})
	}
	// Bootstrap must preserve an explicit admin revoke rather than silently regrant.
	if _, e := store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'kb:manage') WHERE id='admin'`); e != nil {
		t.Fatal(e)
	}
	if e := server.authSvc.InitBuiltinRoles(t.Context()); e != nil {
		t.Fatal(e)
	}
	role, e := server.authSvc.GetRole(t.Context(), "admin")
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range role.Permissions {
		if p == domain.PermKBManage {
			t.Fatal("restart regranted management")
		}
	}
}
func TestKnowledgeBaseAPIConflictAndPublication(t *testing.T) {
	server, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	server.knowledgeBaseSvc = &knowledgebase.Service{Store: store}
	h := server.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.162")
	version, e := server.kpSvc.CreateVersion(t.Context(), "知识卡测试大纲", 2026, "")
	if e != nil {
		t.Fatal(e)
	}
	point, e := server.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: version.ID, Category: "临床综合", Subject: "测试", Unit: "测试", SubItem: "测试", Topic: "测试考点", OutlineCode: "kb.1"})
	if e != nil {
		t.Fatal(e)
	}
	save := serveGenerateJSON(t, h, "POST", "/api/knowledge-base", admin, domain.KnowledgeCard{Title: "测试卡", Kind: "custom", KnowledgePoints: []domain.KnowledgePoint{*point}, Content: domain.KnowledgeContent{CustomText: "仅测试"}})
	if save.Code != 200 {
		t.Fatal(save.Code, save.Body)
	}
	var card domain.KnowledgeCard
	json.Unmarshal(save.Body.Bytes(), &card)
	active := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/"+card.ID+"/activate", admin, map[string]int{"revision": 1})
	if active.Code != 200 {
		t.Fatal(active.Code, active.Body)
	}
	stale := serveGenerateJSON(t, h, "PUT", "/api/knowledge-base/"+card.ID, admin, card)
	if stale.Code != 409 {
		t.Fatal(stale.Code, stale.Body)
	}
	history := serveGenerateJSON(t, h, "GET", "/api/knowledge-base/"+card.ID+"/history", admin, nil)
	if history.Code != 200 {
		t.Fatal(history.Code, history.Body)
	}
	var versions struct {
		Versions []domain.KnowledgeCard `json:"versions"`
	}
	json.Unmarshal(history.Body.Bytes(), &versions)
	if len(versions.Versions) != 2 || versions.Versions[1].Status != "draft" {
		t.Fatal(versions)
	}
	wrong := serveGenerateJSON(t, h, "DELETE", "/api/knowledge-base/"+card.ID, admin, map[string]int{"revision": 1})
	if wrong.Code != 409 {
		t.Fatal(wrong.Code, wrong.Body)
	}
	archived := serveGenerateJSON(t, h, "DELETE", "/api/knowledge-base/"+card.ID, admin, map[string]int{"revision": 2})
	if archived.Code != 200 {
		t.Fatal(archived.Code, archived.Body)
	}
}

func TestRetiredRAGEndpointsCannotOperateOrAuthorizeKnowledgeCards(t *testing.T) {
	server, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	server.knowledgeBaseSvc = &knowledgebase.Service{Store: store}
	h := server.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.163")
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		r := serveGenerateJSON(t, h, method, "/api/system/rag/documents", admin, nil)
		if r.Code != http.StatusNotFound {
			t.Fatal(method, r.Code, r.Body)
		}
	}
	r := serveGenerateJSON(t, h, "GET", "/api/system/rag/documents", "", nil)
	if r.Code != http.StatusNotFound {
		t.Fatal(r.Code, r.Body)
	}
}

func TestKnowledgeBaseDelegationRequiresEffectiveCurrentPermission(t *testing.T) {
	server, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	h := server.Handler()
	super := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.166")
	create := func(name string, permissions []string) auth.User {
		r := serveGenerateJSON(t, h, "POST", "/api/users", super, map[string]any{"username": name, "password": "test-delegation-password", "permissions": permissions})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
		var u auth.User
		if e := json.Unmarshal(r.Body.Bytes(), &u); e != nil {
			t.Fatal(e)
		}
		return u
	}
	actor := create("kb-limited-delegator", []string{domain.PermUserManage, domain.PermKBManage})
	target := create("kb-view-target", []string{domain.PermKBView})
	token := loginForAuthTest(t, h, actor.Username, "test-delegation-password", "198.51.100.167")
	r := serveGenerateJSON(t, h, "PUT", "/api/users/"+target.ID, token, map[string]any{"display_name": target.Username, "roles": []string{}, "permissions": []string{domain.PermKBView, domain.PermKBManage}, "enabled": true})
	if r.Code != 403 {
		t.Fatal("inactive manage permission delegated", r.Code, r.Body)
	}
	var granted bool
	if e := store.DB().QueryRow(`SELECT 'kb:manage'=ANY(permissions) FROM users WHERE id=$1`, target.ID).Scan(&granted); e != nil || granted {
		t.Fatal(granted, e)
	}
	// Granting view makes the actor's manage permission effective, then delegation works.
	actorUpdate := serveGenerateJSON(t, h, "PUT", "/api/users/"+actor.ID, super, map[string]any{"display_name": actor.Username, "roles": []string{}, "permissions": []string{domain.PermUserManage, domain.PermKBManage, domain.PermKBView}, "enabled": true})
	if actorUpdate.Code != 200 {
		t.Fatal(actorUpdate.Code, actorUpdate.Body)
	}
	r = serveGenerateJSON(t, h, "PUT", "/api/users/"+target.ID, token, map[string]any{"display_name": target.Username, "roles": []string{}, "permissions": []string{domain.PermKBView, domain.PermKBManage}, "enabled": true})
	if r.Code != 200 {
		t.Fatal("effective current permission could not delegate", r.Code, r.Body)
	}
}

func TestKnowledgeBaseViewGrantCannotActivateHiddenManagePrivilege(t *testing.T) {
	server, _, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	h := server.Handler()
	super := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.168")
	create := func(name string, p []string) auth.User {
		r := serveGenerateJSON(t, h, "POST", "/api/users", super, map[string]any{"username": name, "password": "test-dependency-password", "permissions": p})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
		var u auth.User
		json.Unmarshal(r.Body.Bytes(), &u)
		return u
	}
	actor := create("kb-read-delegator", []string{domain.PermUserManage, domain.PermKBView})
	target := create("kb-inactive-target", []string{domain.PermKBManage})
	token := loginForAuthTest(t, h, actor.Username, "test-dependency-password", "198.51.100.169")
	update := func(p []string) int {
		r := serveGenerateJSON(t, h, "PUT", "/api/users/"+target.ID, token, map[string]any{"display_name": target.Username, "roles": []string{}, "permissions": p, "enabled": true})
		return r.Code
	}
	if code := update([]string{domain.PermKBManage, domain.PermKBView}); code != 403 {
		t.Fatal("view indirectly enabled manage", code)
	}
	if code := update([]string{domain.PermKBView}); code != 200 {
		t.Fatal("read-only grant with write removed was rejected", code)
	}
}

func TestReferenceYearChineseCaptionPersistsThroughAPIAndHistory(t *testing.T) {
	s, _, h, token, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	year := strings.Repeat("年", 40)
	input := domain.KnowledgeCard{Kind: "exam_reference", Title: "中文考试名称边界", Source: "本轮隔离测试", KnowledgePoints: []domain.KnowledgePoint{point}, Content: domain.KnowledgeContent{Reference: &domain.ExamReference{Year: year, Stem: "隔离参考病例", Options: []domain.Option{{Label: "A", Text: "候选甲"}, {Label: "B", Text: "候选乙"}, {Label: "C", Text: "候选丙"}, {Label: "D", Text: "候选丁"}, {Label: "E", Text: "候选戊"}}, Answer: "A"}}}
	r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base", token, input)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var saved domain.KnowledgeCard
	if e := json.Unmarshal(r.Body.Bytes(), &saved); e != nil {
		t.Fatal(e)
	}
	if saved.Content.Reference.Year != year {
		t.Fatal("caption lost", saved)
	}
	root, _ := s.authSvc.GetUserByUsername("admin")
	if _, e := s.knowledgeBaseSvc.Transition(t.Context(), root.ID, root.Role, saved.ID, "activate", saved.Revision); e != nil {
		t.Fatal(e)
	}
	r = serveGenerateJSON(t, h, "GET", "/api/knowledge-base/"+saved.ID+"/history", token, nil)
	var history struct {
		Versions []domain.KnowledgeCard `json:"versions"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &history) != nil || len(history.Versions) != 2 {
		t.Fatal(r.Code, r.Body)
	}
	for _, v := range history.Versions {
		if v.Content.Reference.Year != year {
			t.Fatal("historical caption truncated")
		}
	}
	input.Content.Reference.Year = year + "长"
	r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base", token, input)
	if r.Code != 400 || !strings.Contains(r.Body.String(), "年份") {
		t.Fatal(r.Code, r.Body)
	}
}
