package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// InitBuiltinRoles 对已存在角色的自愈行为：
//   - super_admin 始终恢复完整权限；
//   - admin 自动补齐业务权限并移除角色模板管理权限；
//   - 医学专家恢复个人命题/题库/退修能力，但不获得批量推理、汇总统计或管理权限；
//   - 其他内置角色保留管理员的有效自定义修改。
func TestInitBuiltinRolesHealsAdminRole(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	// 模拟老库：admin 角色缺少后加的题库分层权限，expert 被旧版本误授了管理权限
	admin, err := service.GetRole(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin.Permissions = []string{domain.PermQuestionView, domain.PermUserManage}
	if err := service.SaveRole(ctx, *admin); err != nil {
		t.Fatal(err)
	}
	expert, err := service.GetRole(ctx, "expert")
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{
		domain.PermQuestionView, domain.PermQuestionEdit,
		domain.PermQuestionGenerate, domain.PermQuestionShare, domain.PermReviewDo,
	} {
		if !slices.Contains(expert.Permissions, permission) {
			t.Fatalf("新建医学专家角色缺少 %s: %v", permission, expert.Permissions)
		}
	}
	expert.Permissions = []string{domain.PermReviewDo, domain.PermStatsView, domain.PermKnowledgeMng}
	if err := service.SaveRole(ctx, *expert); err != nil {
		t.Fatal(err)
	}

	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}

	healed, err := service.GetRole(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, perm := range []string{
		domain.PermQuestionView, domain.PermUserManage,
		domain.PermQuestionViewFormal, domain.PermQuestionDeleteFormal, domain.PermQuestionViewEliminated,
	} {
		if !slices.Contains(healed.Permissions, perm) {
			t.Fatalf("admin 角色自愈后应包含 %s，实际 %v", perm, healed.Permissions)
		}
	}

	expertAfter, err := service.GetRole(ctx, "expert")
	if err != nil {
		t.Fatal(err)
	}
	if len(expertAfter.Permissions) != 5 {
		t.Fatalf("expert 角色应固定为 5 个个人工作/审核权限: %v", expertAfter.Permissions)
	}
	for _, permission := range []string{
		domain.PermQuestionView, domain.PermQuestionEdit,
		domain.PermQuestionGenerate, domain.PermQuestionShare, domain.PermReviewDo,
	} {
		if !slices.Contains(expertAfter.Permissions, permission) {
			t.Fatalf("expert 角色自愈后缺少个人工作权限 %s: %v", permission, expertAfter.Permissions)
		}
	}
	if slices.Contains(expertAfter.Permissions, domain.PermBatchRun) {
		t.Fatalf("expert 角色默认不应包含批量推理权限: %v", expertAfter.Permissions)
	}
	for _, forbidden := range []string{domain.PermStatsView, domain.PermKnowledgeMng, domain.PermQuestionViewGlobal, domain.PermReviewResults} {
		if slices.Contains(expertAfter.Permissions, forbidden) {
			t.Fatalf("expert 角色自愈后仍含越界权限 %s: %v", forbidden, expertAfter.Permissions)
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
		slices.Contains(adminRole.Permissions, domain.PermBatchRun) ||
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
		if slices.Contains(role.Permissions, domain.PermBatchRun) {
			t.Fatalf("内置角色 %s 默认不应拥有批量推理权限: %v", roleID, role.Permissions)
		}
		if !slices.Contains(role.Permissions, domain.PermQuestionGenerate) {
			t.Fatalf("内置角色 %s 应保留单题出题权限: %v", roleID, role.Permissions)
		}
	}

	super, err := service.GetRole(ctx, domain.RoleSuperAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(super.Permissions, domain.PermQuestionGenerate) || !slices.Contains(super.Permissions, domain.PermBatchRun) {
		t.Fatalf("超级管理员应同时拥有单题和批量权限: %v", super.Permissions)
	}
}
