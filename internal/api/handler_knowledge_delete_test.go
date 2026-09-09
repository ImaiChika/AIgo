package api

import (
	"aigo/internal/domain"
	"aigo/internal/knowledge"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeleteSyllabusAPIHandlesEmptyCatalog(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.kpSvc = knowledge.NewService(store)
	handler := server.Handler()
	admin := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	response := serveAuthJSON(t, handler, "DELETE", "/api/knowledge-versions/legacy", admin, "198.51.100.1", nil)
	if response.Code != 200 {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/knowledge-points", "/api/knowledge-points/search", "/api/knowledge-points/meta", "/api/knowledge-points/tree", "/api/knowledge-versions"} {
		r := serveAuthJSON(t, handler, "GET", path, admin, "198.51.100.1", nil)
		if r.Code != 200 {
			t.Fatalf("empty catalog %s: %d %s", path, r.Code, r.Body.String())
		}
		if strings.Contains(r.Body.String(), `"version":{"`) {
			t.Fatal("removed default resurrected")
		}
	}
	r := serveAuthJSON(t, handler, "GET", "/api/knowledge-points/search?version_id=legacy", admin, "198.51.100.1", nil)
	if r.Code != 404 {
		t.Fatalf("explicit removed version: %d", r.Code)
	}
	created := serveAuthJSON(t, handler, "POST", "/api/knowledge-versions", admin, "198.51.100.1", map[string]any{"name": "重新创建", "year": 2026})
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var v domain.KnowledgeVersion
	json.Unmarshal(created.Body.Bytes(), &v)
	versions := serveAuthJSON(t, handler, "GET", "/api/knowledge-versions", admin, "198.51.100.1", nil)
	var catalog struct {
		Default  string                    `json:"default_version_id"`
		Versions []domain.KnowledgeVersion `json:"versions"`
	}
	json.Unmarshal(versions.Body.Bytes(), &catalog)
	if catalog.Default != "" || len(catalog.Versions) != 1 || catalog.Versions[0].ID != v.ID {
		t.Fatalf("draft became default: %s", versions.Body.String())
	}
	var auditCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='knowledge_version_delete'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("delete audit: %d %v", auditCount, err)
	}
}
