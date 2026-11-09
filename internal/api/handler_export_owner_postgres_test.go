package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

func TestExportDownloadsRemainPrivateToCreatingAccount(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	root, err := server.authSvc.GetUserByUsername("admin")
	if err != nil || root == nil {
		t.Fatalf("root: %v", err)
	}
	makeTeacher := func(name string) *auth.User {
		t.Helper()
		user, err := server.authSvc.CreateUserAsWithRoles(context.Background(), root.ID, name, "export-password", name, domain.RoleTeacher, []string{domain.RoleTeacher}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	owner, stranger := makeTeacher("export-owner"), makeTeacher("export-stranger")
	q := domain.A2Question{ID: "private-export-question", OwnerID: owner.ID, ClinicalStem: "患者，男，50岁。持续胸痛2小时。最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}}, Answer: "A", Status: domain.StatusPublished, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		LibraryState: domain.LibraryActive, ConfirmedVersion: 1, ConfirmationSource: domain.ConfirmSourceLegacy}
	if err := store.SaveQuestion(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	ownerToken := loginForAuthTest(t, handler, owner.Username, "export-password", "198.51.100.111")
	strangerToken := loginForAuthTest(t, handler, stranger.Username, "export-password", "198.51.100.112")
	for _, ext := range []string{"xlsx", "docx"} {
		t.Run(ext, func(t *testing.T) {
			exported := serveAuthJSON(t, handler, http.MethodPost, "/api/export/"+ext, ownerToken, "198.51.100.111", map[string]any{"question_ids": []string{q.ID}, "scope": "personal"})
			if exported.Code != 200 {
				t.Fatalf("export: %d %s", exported.Code, exported.Body.String())
			}
			var payload struct {
				Filename string `json:"filename"`
				Path     string `json:"path"`
			}
			if err := json.Unmarshal(exported.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			download := func(token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, "/api/export/download/"+url.PathEscape(payload.Filename), nil)
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			if own := download(ownerToken); own.Code != 200 || own.Body.Len() == 0 {
				t.Fatalf("owner download: %d", own.Code)
			}
			if foreign := download(strangerToken); foreign.Code != 404 {
				t.Fatalf("other teacher can download private export: status=%d", foreign.Code)
			}
			if anonymous := download(""); anonymous.Code != 401 {
				t.Fatalf("anonymous download: %d", anonymous.Code)
			}
			// 旧的共享导出目录不再构成绕过账号隔离的回退入口。
			if err := os.MkdirAll("output/exports", 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("output/exports", "legacy-private."+ext), []byte("old private contents"), 0600); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/export/download/legacy-private."+ext, nil)
			r.Header.Set("Authorization", "Bearer "+strangerToken)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("legacy shared files must not bypass owner isolation: %d", w.Code)
			}
		})
	}
}
