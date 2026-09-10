package domain

import "testing"

func TestAuthoringPermissionsAreSeparateAndAssignable(t *testing.T) {
	byCode := make(map[string]PermissionMeta)
	for _, permission := range AllPermissions() {
		byCode[permission.Code] = permission
	}

	for _, code := range []string{PermQuestionGenerate, PermBatchRun} {
		if !IsValidPermission(code) {
			t.Fatalf("authoring permission %q is not assignable", code)
		}
		if byCode[code].Group != "命题" {
			t.Fatalf("authoring permission %q has group %q, want 命题", code, byCode[code].Group)
		}
	}
	if byCode[PermQuestionGenerate].Name == byCode[PermBatchRun].Name {
		t.Fatalf("single and batch permissions must be distinguishable: %q", byCode[PermQuestionGenerate].Name)
	}
}
