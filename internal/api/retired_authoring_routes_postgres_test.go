//go:build integration

package api

import (
	"strings"
	"testing"
)

func TestRetiredAuthoringRoutesAndPermissionCatalogueAreAbsent(t *testing.T) {
	s, _, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	h := s.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.229")
	for _, path := range []string{"/api/generation-rules", "/api/generation-rules/preview", "/api/generation-rules/owners", "/api/generation-rules/retired", "/api/system/rag/documents"} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			response := serveGenerateJSON(t, h, method, path, admin, nil)
			if response.Code != 404 {
				t.Fatalf("retired route still registered: %s %s -> %d", method, path, response.Code)
			}
		}
	}
	response := serveGenerateJSON(t, h, "GET", "/api/permissions", admin, nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), `"rule:`) || strings.Contains(response.Body.String(), `"rag:`) {
		t.Fatal("retired capabilities remain in the active catalogue", response.Code, response.Body)
	}
	for _, permission := range []string{"experience:view", "experience:manage", "experience:use"} {
		if !strings.Contains(response.Body.String(), permission) {
			t.Fatal("unified knowledge capability missing", permission)
		}
	}
}
