package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

func TestBuiltinRoleRevocationsSurviveRestart(t *testing.T) {
	s, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{domain.RoleTeacher, domain.RoleAdmin, domain.RoleExpert} {
		r, err := s.GetRole(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		r.Permissions = []string{domain.PermQuestionView}
		if err = s.SaveRole(ctx, *r); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.InitBuiltinRoles(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{domain.RoleTeacher, domain.RoleAdmin, domain.RoleExpert} {
		r, err := s.GetRole(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Permissions) != 1 || r.Permissions[0] != domain.PermQuestionView {
			t.Errorf("%s startup restored explicitly revoked permissions: %v", id, r.Permissions)
		}
	}
}

// Existing business templates retain explicitly customized valid permissions.
func TestInitBuiltinRolesPreservesCustomizedBusinessRoles(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	custom := map[string][]string{
		domain.RoleAdmin:   {domain.PermQuestionView, domain.PermUserManage},
		domain.RoleExpert:  {domain.PermReviewDo, domain.PermStatsView, domain.PermKnowledgeMng},
		domain.RoleTeacher: {domain.PermQuestionView},
	}
	for id, permissions := range custom {
		role, err := service.GetRole(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		role.Permissions = permissions
		if err = service.SaveRole(ctx, *role); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	for id, permissions := range custom {
		got, err := service.GetRole(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Permissions, permissions) {
			t.Fatalf("%s customized permissions changed: got %v want %v", id, got.Permissions, permissions)
		}
	}
}

func TestSuperAdminBootstrapAndDelegatedAdminBoundaries(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}

	// 模拟旧库中的 admin/admin：先注册，再挂载旧 admin 角色，启动初始化应将其升级。
	legacy, err := service.Register("admin", "legacy-admin-password", "旧管理员")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateUser(legacy.ID, legacy.DisplayName, domain.RoleAdmin, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if created, _, err := service.InitAdmin("admin", "ignored-password", "系统管理员"); err != nil || created {
		t.Fatalf("existing admin should be upgraded in place: created=%v err=%v", created, err)
	}
	upgraded, err := service.GetUserByID(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Role != domain.RoleSuperAdmin || !slices.Contains(upgraded.Permissions, domain.PermRoleManage) {
		t.Fatalf("legacy admin was not upgraded to full super admin: %+v", upgraded)
	}

	adminRole, err := service.GetRole(ctx, domain.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(adminRole.Permissions, domain.PermRoleManage) ||
		slices.Contains(adminRole.Permissions, domain.PermQuestionViewAll) ||
		!slices.Contains(adminRole.Permissions, domain.PermBatchRun) ||
		!slices.Contains(adminRole.Permissions, domain.PermUserManage) ||
		!slices.Contains(adminRole.Permissions, domain.PermReviewFinal) {
		t.Fatalf("delegated admin role has incorrect permissions: %v", adminRole.Permissions)
	}

	delegated, err := service.Register("delegated-admin", "delegated-password", "业务管理员")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateUser(delegated.ID, delegated.DisplayName, domain.RoleAdmin, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	delegated, err = service.GetUserByID(delegated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if delegated.Role == domain.RoleSuperAdmin {
		t.Fatal("delegated admin must not become super admin")
	}
	if ok, err := service.HasPermission(ctx, delegated.ID, domain.PermRoleManage); err != nil || ok {
		t.Fatalf("delegated admin unexpectedly has role management: ok=%v err=%v", ok, err)
	}

	if _, err := service.CreateUserAs(ctx, delegated.ID, "forbidden-super", "forbidden-password", "越权", domain.RoleSuperAdmin, nil, nil); !errors.Is(err, auth.ErrSuperAdminOnly) {
		t.Fatalf("delegated admin assigning super admin should be denied, got %v", err)
	}
	if _, err := service.CreateUserAs(ctx, upgraded.ID, "forbidden-super-by-super", "forbidden-password", "不应分配", domain.RoleSuperAdmin, nil, nil); !errors.Is(err, auth.ErrSuperAdminRoleNotAssignable) {
		t.Fatalf("super admin role should not be assignable from user management, got %v", err)
	}
	if err := service.SaveRoleAs(ctx, delegated.ID, domain.Role{ID: "custom", Name: "越权角色", Permissions: []string{domain.PermQuestionView}}); !errors.Is(err, auth.ErrSuperAdminOnly) {
		t.Fatalf("delegated admin editing role should be denied, got %v", err)
	}
	created, err := service.CreateUserAs(ctx, delegated.ID, "delegated-expert", "expert-password", "审核老师", domain.RoleExpert, nil, nil)
	if err != nil {
		t.Fatalf("delegated admin should be able to create an expert: %v", err)
	}
	if _, err := service.DeleteUserAs(ctx, delegated.ID, created.ID); err != nil {
		t.Fatalf("delegated admin should be able to delete ordinary users: %v", err)
	}
}

func TestBuiltinAuthoringPermissionsAreSeparated(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}

	for _, roleID := range []string{domain.RoleAdmin, domain.RoleExpert, domain.RoleTeacher} {
		role, err := service.GetRole(ctx, roleID)
		if err != nil {
			t.Fatal(err)
		}
		if roleID == domain.RoleExpert && slices.Contains(role.Permissions, domain.PermBatchRun) {
			t.Fatalf("内置角色 %s 默认不应拥有批量推理权限: %v", roleID, role.Permissions)
		}
		if roleID != domain.RoleExpert && !slices.Contains(role.Permissions, domain.PermBatchRun) {
			t.Fatalf("内置角色 %s 应默认拥有批量推理权限: %v", roleID, role.Permissions)
		}
		if roleID == domain.RoleExpert && slices.Contains(role.Permissions, domain.PermQuestionGenerate) {
			t.Fatalf("审题老师不应拥有单题出题权限: %v", role.Permissions)
		}
		if roleID != domain.RoleExpert && !slices.Contains(role.Permissions, domain.PermQuestionGenerate) {
			t.Fatalf("内置角色 %s 应保留单题出题权限: %v", roleID, role.Permissions)
		}
		for _, p := range []string{domain.PermExperienceView, domain.PermExperienceManage} {
			if !slices.Contains(role.Permissions, p) {
				t.Fatalf("teacher/admin template %s missing personal knowledge permission %s", roleID, p)
			}
		}
		if roleID == domain.RoleExpert && slices.Contains(role.Permissions, domain.PermExperienceUse) {
			t.Fatal("review-only identity received authoring use")
		}
		if roleID == domain.RoleTeacher && !slices.Contains(role.Permissions, domain.PermReviewSubmit) {
			t.Fatalf("命题教师应拥有提交审核权限: %v", role.Permissions)
		}
		if roleID == domain.RoleTeacher {
			for _, permission := range []string{domain.PermQuestionViewFormal, domain.PermQuestionViewEliminated} {
				if !slices.Contains(role.Permissions, permission) {
					t.Fatalf("命题教师应能查看本人三层题库，缺少 %s: %v", permission, role.Permissions)
				}
			}
		}
	}

	super, err := service.GetRole(ctx, domain.RoleSuperAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(super.Permissions, domain.PermQuestionGenerate) || !slices.Contains(super.Permissions, domain.PermBatchRun) {
		t.Fatalf("超级管理员应同时拥有单题和批量权限: %v", super.Permissions)
	}

	expert, err := service.GetRole(ctx, domain.RoleExpert)
	if err != nil {
		t.Fatal(err)
	}
	expert.Permissions = append(expert.Permissions, domain.PermBatchRun)
	if err := service.SaveRole(ctx, *expert); err != nil {
		t.Fatal(err)
	}
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	expertAfter, err := service.GetRole(ctx, domain.RoleExpert)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(expertAfter.Permissions, domain.PermBatchRun) {
		t.Fatalf("审题岗位不能因保留模板自定义而获得出题能力: %v", expertAfter.Permissions)
	}
}

func TestCustomAuthoringRoleKeepsBatchPermissionIndependent(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	role := domain.Role{ID: "custom-authoring", Name: "临时命题角色", Permissions: []string{domain.PermQuestionGenerate}}
	if err := service.SaveRole(ctx, role); err != nil {
		t.Fatal(err)
	}
	stored, err := service.GetRole(ctx, role.ID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(stored.Permissions, domain.PermBatchRun) {
		t.Fatalf("单题出题不得隐式授予独立批量权限: %v", stored.Permissions)
	}
	adminRole := domain.Role{ID: "custom-admin-like", Name: "管理员型角色", Permissions: []string{domain.PermQuestionGenerate, domain.PermUserManage}}
	if err := service.SaveRole(ctx, adminRole); err != nil {
		t.Fatal(err)
	}
	adminStored, err := service.GetRole(ctx, adminRole.ID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(adminStored.Permissions, domain.PermBatchRun) {
		t.Fatalf("管理员型角色不应因单题出题自动获得批量推理: %v", adminStored.Permissions)
	}
}

func TestInitBuiltinRolesExcludesAdminQuotaButKeepsBatch(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.InitBuiltinRoles(ctx); err != nil {
			t.Fatal(err)
		}
		admin, err := service.GetRole(ctx, domain.RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(admin.Permissions, domain.PermGenerationQuotaManage) || !slices.Contains(admin.Permissions, domain.PermBatchRun) {
			t.Fatalf("admin permissions after init %d: %v", i, admin.Permissions)
		}
	}
}
