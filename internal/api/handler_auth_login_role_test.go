//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

func TestFreshDualLoginDefaultsToTeacherWhileExplicitExpertSessionStaysExpert(t *testing.T) {
	s, _, cleanup := authHandlerTestServer(t)
	defer cleanup()
	h := s.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.226")
	created := serveAuthJSON(t, h, "POST", "/api/users", admin, "198.51.100.226", map[string]any{
		"username": "fresh-dual-login", "password": "fresh-dual-password", "display_name": "双身份登录",
		"role": domain.RoleExpert, "roles": []string{domain.RoleExpert, domain.RoleTeacher},
	})
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body)
	}
	decode := func(r *httptest.ResponseRecorder) (string, auth.User) {
		t.Helper()
		var data struct {
			Token string    `json:"token"`
			User  auth.User `json:"user"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &data) != nil || data.Token == "" {
			t.Fatal(r.Code, r.Body)
		}
		return data.Token, data.User
	}
	login := func() (string, auth.User) {
		t.Helper()
		return decode(serveAuthJSON(t, h, "POST", "/api/auth/login", "", "198.51.100.227", map[string]any{"username": "fresh-dual-login", "password": "fresh-dual-password"}))
	}
	assertMe := func(token, role string, generate, review bool) {
		t.Helper()
		r := serveAuthJSON(t, h, "GET", "/api/auth/me", token, "198.51.100.227", nil)
		var me auth.User
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &me) != nil {
			t.Fatal(r.Code, r.Body)
		}
		if me.Role != role || slices.Contains(me.Permissions, domain.PermQuestionGenerate) != generate || slices.Contains(me.Permissions, domain.PermReviewDo) != review {
			t.Fatalf("wrong current identity after /me: %+v", me)
		}
	}
	teacherToken, teacher := login()
	if teacher.Role != domain.RoleTeacher {
		t.Fatal("fresh login entered reviewer workspace", teacher.Role)
	}
	assertMe(teacherToken, domain.RoleTeacher, true, false)
	expertToken, expert := decode(serveAuthJSON(t, h, "POST", "/api/auth/switch-role", teacherToken, "198.51.100.227", map[string]any{"role": domain.RoleExpert}))
	if expert.Role != domain.RoleExpert {
		t.Fatal(expert.Role)
	}
	assertMe(expertToken, domain.RoleExpert, false, true)
	assertMe(expertToken, domain.RoleExpert, false, true)
	newToken, again := login()
	if again.Role != domain.RoleTeacher {
		t.Fatal("repeat fresh login did not return to authoring", again.Role)
	}
	assertMe(newToken, domain.RoleTeacher, true, false)
	assertMe(expertToken, domain.RoleExpert, false, true)
	denied := serveAuthJSON(t, h, "POST", "/api/questions/generate", expertToken, "198.51.100.227", map[string]any{})
	if denied.Code != http.StatusForbidden {
		t.Fatal("expert token inherited authoring ability", denied.Code, denied.Body)
	}
}
