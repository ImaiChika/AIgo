//go:build integration

package auth_test

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"context"
	"errors"
	"slices"
	"testing"
)

func TestLearningDefaultsKeepPersonalAndSharedCapabilitiesSeparate(t *testing.T) {
	s, cleanup := loginLimitTestService(t)
	defer cleanup()
	if err := s.InitBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{domain.RoleTeacher, domain.RoleAdmin, domain.RoleSuperAdmin, domain.RoleExpert} {
		r, err := s.GetRole(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"learning:analyze", "learning:save", "learning:use"} {
			if slices.Contains(r.Permissions, p) != (id != domain.RoleExpert) {
				t.Errorf("%s %s default: %v", id, p, r.Permissions)
			}
		}
		if id == domain.RoleTeacher || id == domain.RoleExpert {
			for _, p := range []string{domain.PermKBView, domain.PermKBUse, domain.PermKBSubmit} {
				if !slices.Contains(r.Permissions, p) {
					t.Fatal("authorized Schema65 teacher default missing", p)
				}
			}
			for _, p := range []string{domain.PermKBManage, domain.PermKBReview} {
				if slices.Contains(r.Permissions, p) {
					t.Fatal("teacher default expanded review/management", p)
				}
			}
		}
	}
}

func TestLearningDelegationUsesCurrentIdentityAndChecksLatentCapabilities(t *testing.T) {
	s, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := t.Context()
	if err := s.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InitAdmin("learning-root", "root-password-123", "超管"); err != nil {
		t.Fatal(err)
	}
	root, _ := s.GetUserByUsername("learning-root")
	role := domain.Role{ID: "limited-manager", Name: "受限管理员", Permissions: []string{domain.PermUserManage, domain.PermQuestionView, domain.PermQuestionGenerate}}
	if err := s.SaveRole(ctx, role); err != nil {
		t.Fatal(err)
	}
	actor, err := s.CreateUserAsWithRoles(ctx, root.ID, "learning-manager", "manager-password", "管理员", role.ID, []string{role.ID, domain.RoleTeacher}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	limited := context.WithValue(context.WithValue(ctx, auth.UserIDKey, actor.ID), auth.RoleKey, role.ID)
	for _, tc := range []struct {
		name          string
		before, after []string
	}{
		{"direct_learning_from_other_role", nil, []string{domain.PermLearningSave}},
		{"view_activates_analyze", []string{domain.PermLearningAnalyze}, []string{domain.PermLearningAnalyze, domain.PermQuestionView}},
		{"view_activates_save", []string{domain.PermLearningSave}, []string{domain.PermLearningSave, domain.PermQuestionView}},
		{"generation_activates_use", []string{domain.PermLearningUse}, []string{domain.PermLearningUse, domain.PermQuestionGenerate}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, e := s.CreateUserAsWithRoles(ctx, root.ID, "target-"+tc.name, "target-password", "目标", "", nil, tc.before, nil)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.UpdateUserAsWithRoles(limited, actor.ID, target.ID, target.DisplayName, "", nil, tc.after, nil, nil); !errors.Is(e, auth.ErrPermissionDenied) {
				t.Fatal("current identity expanded learning", e)
			}
			fresh, _ := s.GetUserByID(target.ID)
			if !slices.Equal(fresh.DirectPermissions, tc.before) && !(len(fresh.DirectPermissions) == 0 && len(tc.before) == 0) {
				t.Fatal("failed delegation mutated target", fresh)
			}
		})
	}
}

func TestLearningDependencyActivationIsComparedPerIdentity(t *testing.T) {
	s, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := t.Context()
	if err := s.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InitAdmin("identity-root", "root-password-123", "超管"); err != nil {
		t.Fatal(err)
	}
	root, _ := s.GetUserByUsername("identity-root")
	for _, role := range []domain.Role{
		{ID: "grant-generation", Name: "出题授权管理员", Permissions: []string{domain.PermUserManage, domain.PermQuestionGenerate}},
		{ID: "latent-learning", Name: "学习未激活身份", Permissions: []string{domain.PermLearningUse}},
	} {
		if err := s.SaveRole(ctx, role); err != nil {
			t.Fatal(err)
		}
	}
	actor, err := s.CreateUserAsWithRoles(ctx, root.ID, "identity-granter", "manager-password", "管理员", "grant-generation", []string{"grant-generation"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateUserAsWithRoles(ctx, root.ID, "identity-target", "target-password", "多身份", domain.RoleTeacher, []string{domain.RoleTeacher, "latent-learning"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	limited := context.WithValue(context.WithValue(ctx, auth.UserIDKey, actor.ID), auth.RoleKey, "grant-generation")
	if _, err = s.UpdateUserAsWithRoles(limited, actor.ID, target.ID, target.DisplayName, target.Role, target.Roles, []string{domain.PermQuestionGenerate}, nil, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("existing teacher capability hid activation in another identity", err)
	}
}
