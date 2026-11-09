package auth_test

import (
	"context"
	"errors"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// 身份组合回归（2026-09-25）：审题老师被升级为管理员后必须仍能切回原岗位。
// 管理员可与业务岗位共存为多身份（每身份有效权限按所选模板单独计算）；
// 超级管理员仍是唯一最高身份，不可与任何模板组合，也不可由非超管授予。
func TestAdminIdentityCoexistsWithBusinessRoles(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.InitAdmin("root", "root-password-123", "超管"); err != nil {
		t.Fatal(err)
	}
	root, err := service.GetUserByUsername("root")
	if err != nil || root == nil {
		t.Fatalf("读取超管失败: %v", err)
	}

	target, err := service.CreateUserAsWithRoles(ctx, root.ID, "switcher", "switcher-password",
		"切换回归", domain.RoleExpert, []string{domain.RoleExpert}, nil, nil)
	if err != nil {
		t.Fatalf("创建审题老师失败: %v", err)
	}

	// 升级为管理员时保留审题老师身份（此前互斥规则会拒绝该组合）。
	if _, err := service.UpdateUserAsWithRoles(ctx, root.ID, target.ID, "切换回归",
		domain.RoleAdmin, []string{domain.RoleExpert, domain.RoleAdmin}, nil, nil, nil); err != nil {
		t.Fatalf("管理员与审题老师应可共存: %v", err)
	}

	// 双向身份切换都必须成功：管理员 → 审题老师 → 管理员。
	if _, _, err := service.SwitchRole(ctx, target.ID, domain.RoleExpert); err != nil {
		t.Fatalf("切回审题老师失败: %v", err)
	}
	if _, _, err := service.SwitchRole(ctx, target.ID, domain.RoleAdmin); err != nil {
		t.Fatalf("再切回管理员失败: %v", err)
	}
}

func TestRoleCombinationGuardsStillHold(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.InitAdmin("root", "root-password-123", "超管"); err != nil {
		t.Fatal(err)
	}
	root, err := service.GetUserByUsername("root")
	if err != nil || root == nil {
		t.Fatalf("读取超管失败: %v", err)
	}
	teacher, err := service.CreateUserAsWithRoles(ctx, root.ID, "plain-teacher", "teacher-password",
		"教师", domain.RoleTeacher, []string{domain.RoleTeacher}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 超级管理员仍不可与任何模板组合。
	_, err = service.CreateUserAsWithRoles(ctx, root.ID, "combo-super", "combo-password",
		"非法组合", domain.RoleExpert, []string{domain.RoleExpert, domain.RoleSuperAdmin}, nil, nil)
	if !errors.Is(err, auth.ErrSuperAdminRoleNotAssignable) {
		t.Fatalf("super_admin 组合应被拒绝，实际: %v", err)
	}

	// 非超管（管理员身份）仍不能把任何人升级为管理员。
	manager, err := service.CreateUserAsWithRoles(ctx, root.ID, "delegated-mgr", "manager-password",
		"业务管理员", domain.RoleAdmin, []string{domain.RoleAdmin}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateUserAsWithRoles(ctx, manager.ID, teacher.ID, "教师",
		domain.RoleAdmin, []string{domain.RoleAdmin}, nil, nil, nil); !errors.Is(err, auth.ErrSuperAdminOnly) {
		t.Fatalf("非超管授予管理员应被拒绝，实际: %v", err)
	}
}

func TestDelegatedAdminCanPreserveExistingAssignmentsWithoutGrantingNewCapabilities(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := service.InitBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.InitAdmin("root-preserve", "root-password-123", "超管"); err != nil {
		t.Fatal(err)
	}
	root, err := service.GetUserByUsername("root-preserve")
	if err != nil || root == nil {
		t.Fatalf("root: %v", err)
	}
	manager, err := service.CreateUserAsWithRoles(ctx, root.ID, "manager-preserve", "manager-password", "业务管理员", domain.RoleAdmin, []string{domain.RoleAdmin}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.CreateUserAsWithRoles(ctx, root.ID, "dual-preserve", "teacher-password", "双身份教师", domain.RoleTeacher, []string{domain.RoleTeacher, domain.RoleExpert}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateUserAsWithRoles(ctx, manager.ID, target.ID, "仅修改昵称", target.Role, target.Roles, target.DirectPermissions, target.BankIDs, nil)
	if err != nil {
		t.Fatalf("preserving existing teacher/expert roles must not count as granting batch permission: %v", err)
	}
	if updated.DisplayName != "仅修改昵称" {
		t.Fatalf("profile update not saved: %+v", updated)
	}
	disabled := false
	if _, err := service.UpdateUserAsWithRoles(ctx, manager.ID, target.ID, updated.DisplayName, target.Role, target.Roles, target.DirectPermissions, target.BankIDs, &disabled); err != nil {
		t.Fatalf("authorized disable without changing grants: %v", err)
	}
	// 直接授予 view_all 会扩大目标能力，不能把已有教师模板权限当作豁免。
	if _, err := service.UpdateUserAsWithRoles(ctx, manager.ID, target.ID, updated.DisplayName, target.Role, target.Roles, []string{domain.PermQuestionViewAll}, nil, nil); err == nil {
		t.Fatal("a new direct grant beyond the manager's own permissions must still be rejected")
	}
	expert, err := service.CreateUserAsWithRoles(ctx, root.ID, "expert-preserve", "expert-password", "审题老师", domain.RoleExpert, []string{domain.RoleExpert}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 管理员默认拥有批量推理后，教师模板权限全部在其能力范围内，新增教师身份应放行。
	if _, err := service.UpdateUserAsWithRoles(ctx, manager.ID, expert.ID, expert.DisplayName, domain.RoleExpert, []string{domain.RoleExpert, domain.RoleTeacher}, nil, nil, nil); err != nil {
		t.Fatalf("granting teacher role within manager permissions must succeed: %v", err)
	}
	if err := service.SaveRoleAs(ctx, root.ID, domain.Role{ID: "coexistence-view-all", Name: "全库查看角色", Permissions: []string{domain.PermQuestionViewAll}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateUserAsWithRoles(ctx, manager.ID, expert.ID, expert.DisplayName, domain.RoleExpert, []string{domain.RoleExpert, "coexistence-view-all"}, nil, nil, nil); err == nil {
		t.Fatal("a new role containing permissions the manager lacks must still be rejected")
	}
}
