package auth

import (
	"reflect"
	"testing"

	"aigo/internal/domain"
)

func TestLoginRolePrefersTeacherOverExpert(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored string
		roles  []string
		want   string
	}{
		{"dual_stored_expert", domain.RoleExpert, []string{domain.RoleExpert, domain.RoleTeacher}, domain.RoleTeacher},
		{"dual_stored_teacher", domain.RoleTeacher, []string{domain.RoleTeacher, domain.RoleExpert}, domain.RoleTeacher},
		{"dual_expert_sorted_first", "", []string{domain.RoleExpert, domain.RoleTeacher}, domain.RoleTeacher},
		{"dual_teacher_sorted_first", "", []string{domain.RoleTeacher, domain.RoleExpert}, domain.RoleTeacher},
		{"only_expert", domain.RoleExpert, []string{domain.RoleExpert}, domain.RoleExpert},
		{"only_teacher", domain.RoleTeacher, []string{domain.RoleTeacher}, domain.RoleTeacher},
		{"explicit_admin", domain.RoleAdmin, []string{domain.RoleAdmin, domain.RoleExpert, domain.RoleTeacher}, domain.RoleAdmin},
		{"explicit_custom", "custom-reader", []string{"custom-reader", domain.RoleExpert, domain.RoleTeacher}, "custom-reader"},
		{"expert_without_teacher", domain.RoleExpert, []string{domain.RoleExpert, "custom-reader"}, domain.RoleExpert},
		{"super_admin", domain.RoleSuperAdmin, []string{domain.RoleSuperAdmin}, domain.RoleSuperAdmin},
		{"no_role", "", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roles := append([]string(nil), tc.roles...)
			if got := loginRoleID(tc.stored, tc.roles); got != tc.want {
				t.Errorf("login role=%q want=%q", got, tc.want)
			}
			if !reflect.DeepEqual(roles, tc.roles) {
				t.Fatal("choosing login role changed assigned roles")
			}
			if tc.stored != "" && defaultRoleID(tc.stored, tc.roles) != tc.stored {
				t.Fatal("login preference changed stored/default role semantics")
			}
		})
	}
}
