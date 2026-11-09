//go:build integration

package auth_test

import (
	"context"
	"slices"
	"testing"

	"aigo/internal/domain"
)

func TestDualTeacherFreshLoginIgnoresHistoricalExpertDefaultAndKeepsExplicitSession(t *testing.T) {
	s, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := t.Context()
	if err := s.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InitAdmin("login-root", "login-root-password", "登录测试"); err != nil {
		t.Fatal(err)
	}
	root, err := s.GetUserByUsername("login-root")
	if err != nil || root == nil {
		t.Fatal(err)
	}
	dual, err := s.CreateUserAsWithRoles(ctx, root.ID, "dual-default-expert", "dual-login-password", "双身份", domain.RoleExpert, []string{domain.RoleExpert, domain.RoleTeacher}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	firstToken, first, err := s.Login(dual.Username, "dual-login-password")
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if first.Role != domain.RoleTeacher || !slices.Contains(first.Permissions, domain.PermQuestionGenerate) || slices.Contains(first.Permissions, domain.PermReviewDo) {
		t.Fatalf("fresh login must enter authoring without unioning review permissions: %+v", first)
	}
	claims, err := s.ValidateToken(firstToken)
	if err != nil || claims.Role != domain.RoleTeacher {
		t.Fatalf("login JWT role=%+v err=%v", claims, err)
	}
	if !slices.Contains(first.AccountPermissions, domain.PermReviewDo) {
		t.Fatal("login incorrectly removed the mounted expert role")
	}

	expertToken, expert, err := s.SwitchRole(ctx, dual.ID, domain.RoleExpert)
	if err != nil || expert == nil {
		t.Fatal(err)
	}
	if expert.Role != domain.RoleExpert || !slices.Contains(expert.Permissions, domain.PermReviewDo) || slices.Contains(expert.Permissions, domain.PermQuestionGenerate) {
		t.Fatalf("explicit expert session gained authoring permissions: %+v", expert)
	}
	expertClaims, err := s.ValidateToken(expertToken)
	if err != nil || expertClaims.Role != domain.RoleExpert {
		t.Fatalf("switch JWT=%+v err=%v", expertClaims, err)
	}
	for range 2 {
		me, err := s.GetUserByIDForRole(ctx, dual.ID, expertClaims.Role)
		if err != nil || me == nil || me.Role != domain.RoleExpert || slices.Contains(me.Permissions, domain.PermQuestionGenerate) {
			t.Fatal("/me equivalent reset explicit review identity", me, err)
		}
	}

	_, again, err := s.Login(dual.Username, "dual-login-password")
	if err != nil || again == nil || again.Role != domain.RoleTeacher {
		t.Fatal("every fresh login must prefer authoring", again, err)
	}
	// A second login selects a new session identity; it does not invalidate or
	// rewrite another browser's already chosen review identity.
	oldClaims, err := s.ValidateToken(expertToken)
	if err != nil || oldClaims.Role != domain.RoleExpert {
		t.Fatal("fresh login changed existing expert JWT", oldClaims, err)
	}
	me, err := s.GetUserByIDForRole(context.Background(), dual.ID, oldClaims.Role)
	if err != nil || me == nil || me.Role != domain.RoleExpert {
		t.Fatal("existing review session was forced back", me, err)
	}
	stored, err := s.GetUserByID(dual.ID)
	if err != nil || stored == nil || stored.Role != domain.RoleExpert {
		t.Fatal("login silently rewrote stored default", stored, err)
	}
}

func TestFreshLoginPreservesAdminCustomAndSingleRoleDefaults(t *testing.T) {
	s, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := t.Context()
	if err := s.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InitAdmin("control-root", "control-root-password", "控制组"); err != nil {
		t.Fatal(err)
	}
	root, err := s.GetUserByUsername("control-root")
	if err != nil || root == nil {
		t.Fatal(err)
	}
	if err := s.SaveRoleAs(ctx, root.ID, domain.Role{ID: "login-custom", Name: "自定义只读", Permissions: []string{domain.PermStatsView}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, stored           string
		roles                  []string
		canGenerate, canReview bool
	}{
		{"admin", domain.RoleAdmin, []string{domain.RoleAdmin, domain.RoleTeacher, domain.RoleExpert}, true, true},
		{"custom", "login-custom", []string{"login-custom", domain.RoleTeacher, domain.RoleExpert}, false, false},
		{"expert", domain.RoleExpert, []string{domain.RoleExpert}, false, true},
		{"teacher", domain.RoleTeacher, []string{domain.RoleTeacher}, true, false},
		{"none", "", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, err := s.CreateUserAsWithRoles(t.Context(), root.ID, "login-control-"+tc.name, "control-password", tc.name, tc.stored, tc.roles, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			token, got, err := s.Login(user.Username, "control-password")
			if err != nil || got == nil {
				t.Fatal(err)
			}
			if got.Role != tc.stored || slices.Contains(got.Permissions, domain.PermQuestionGenerate) != tc.canGenerate || slices.Contains(got.Permissions, domain.PermReviewDo) != tc.canReview {
				t.Fatalf("unexpected login role/permissions: %+v", got)
			}
			claims, err := s.ValidateToken(token)
			if err != nil || claims.Role != tc.stored {
				t.Fatal(claims, err)
			}
		})
	}
}
