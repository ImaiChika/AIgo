//go:build integration

package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

func TestPersonalKnowledgeDelegationChecksCurrentIdentityAndLatentDependencies(t *testing.T) {
	s, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := t.Context()
	if err := s.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InitAdmin("manual-grant-root", "manual-root-password", "root"); err != nil {
		t.Fatal(err)
	}
	root, _ := s.GetUserByUsername("manual-grant-root")
	for _, role := range []domain.Role{{ID: "manual-view-manager", Name: "只读知识授权者", Permissions: []string{domain.PermUserManage, domain.PermExperienceView}}, {ID: "latent-manual-manager", Name: "潜伏知识维护", Permissions: []string{domain.PermExperienceManage}}} {
		if err := s.SaveRoleAs(ctx, root.ID, role); err != nil {
			t.Fatal(err)
		}
	}
	actor, err := s.CreateUserAsWithRoles(ctx, root.ID, "manual-limited-granter", "manual-actor-password", "下放者", "manual-view-manager", []string{"manual-view-manager", domain.RoleTeacher}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	current := context.WithValue(context.WithValue(ctx, auth.UserIDKey, actor.ID), auth.RoleKey, "manual-view-manager")
	for _, tc := range []struct {
		name          string
		before, after []string
	}{
		{"manage_grant_from_other_identity", nil, []string{domain.PermExperienceView, domain.PermExperienceManage}},
		{"view_activates_latent_manage", []string{domain.PermExperienceManage}, []string{domain.PermExperienceManage, domain.PermExperienceView}},
		{"view_activates_latent_use", []string{domain.PermExperienceUse, domain.PermQuestionGenerate}, []string{domain.PermExperienceUse, domain.PermQuestionGenerate, domain.PermExperienceView}},
		{"new_paid_analysis", []string{domain.PermQuestionView}, []string{domain.PermQuestionView, domain.PermLearningAnalyze}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, e := s.CreateUserAsWithRoles(ctx, root.ID, "manual-target-"+tc.name, "target-password", "目标", "", nil, tc.before, nil)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.UpdateUserAsWithRoles(current, actor.ID, target.ID, target.DisplayName, "", nil, tc.after, nil, nil); !errors.Is(e, auth.ErrPermissionDenied) {
				t.Fatal("latent/new knowledge capability escaped current identity", e)
			}
			fresh, e := s.GetUserByID(target.ID)
			if e != nil || !slices.Equal(fresh.DirectPermissions, tc.before) {
				t.Fatal("failed grant changed target", fresh, e)
			}
		})
	}
	target, err := s.CreateUserAsWithRoles(ctx, root.ID, "manual-cross-role-target", "target-password", "目标", domain.RoleTeacher, []string{domain.RoleTeacher, "latent-manual-manager"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateUserAsWithRoles(current, actor.ID, target.ID, target.DisplayName, target.Role, target.Roles, []string{domain.PermExperienceView}, nil, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("existing teacher capability hid a different identity becoming active", err)
	}
}
