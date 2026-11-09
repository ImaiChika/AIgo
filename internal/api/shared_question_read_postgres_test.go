//go:build integration

package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

func TestSharedSnapshotReadAndExportSurvivePrivateRevision(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	s.knowledgeBaseSvc = &knowledgebase.Service{Store: s.questionStore.(knowledgebase.Store)}
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	perms := append(append([]string{}, owner.DirectPermissions...), domain.PermQuestionShare)
	if _, err := s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, perms, nil, nil); err != nil {
		t.Fatal(err)
	}
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.210")
	created := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "c4-global", "password": "c4-global-password", "permissions": []string{domain.PermQuestionViewGlobal, domain.PermQuestionViewFormal, domain.PermQuestionDownload, domain.PermKBView}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	viewer := loginForAuthTest(t, h, "c4-global", "c4-global-password", "198.51.100.211")
	q := seedPassedQuestion(t, s, h, teacher, v, "c4-shared", "患者，男，45岁。共享原始病例。最可能的诊断是")
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	q = *mustLibraryQuestion(t, s, q.ID)
	guards := func(q domain.A2Question) map[string]any {
		return map[string]any{"expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision}
	}
	shared := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/share", teacher, guards(q))
	if shared.Code != 201 {
		t.Fatal("0 round sharing", shared.Code, shared.Body)
	}
	var response questionShareView
	if err := json.Unmarshal(shared.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	edit := func(q domain.A2Question) *httptest.ResponseRecorder {
		body := guards(q)
		body["clinical_stem"] = "患者，男，45岁。私有第二版病例。最可能的诊断是"
		return serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, teacher, body)
	}
	if r := edit(q); r.Code != 409 {
		t.Fatal("pending must lock", r.Code, r.Body)
	}
	approved := serveGenerateJSON(t, h, "POST", "/api/question-shares/"+response.Request.ID+"/review", admin, map[string]any{"status": "approved", "expected_share_revision": response.Request.Revision})
	if approved.Code != 200 {
		t.Fatal(approved.Code, approved.Body)
	}
	export := func(token, scope, ext string) string {
		r := serveGenerateJSON(t, h, "POST", "/api/export/"+ext, token, map[string]any{"scope": scope, "question_ids": []string{q.ID}})
		if r.Code != 200 {
			t.Fatal(scope, ext, r.Code, r.Body)
		}
		var out struct {
			Filename string `json:"filename"`
		}
		json.Unmarshal(r.Body.Bytes(), &out)
		if out.Filename == "" {
			t.Fatal(r.Body)
		}
		return out.Filename
	}
	personalFile := export(teacher, "personal", "xlsx")
	globalXLSX := export(viewer, "global", "xlsx")
	globalDOCX := export(viewer, "global", "docx")
	if r := edit(q); r.Code != 200 {
		t.Fatal("approved source may revise", r.Code, r.Body)
	}
	q = *mustLibraryQuestion(t, s, q.ID)
	if q.Version != 2 || q.LibraryState != domain.LibraryUnfiled {
		t.Fatal(q)
	}
	for _, path := range []string{"/api/questions/" + q.ID + "?scope=global", "/api/questions/" + q.ID, "/api/questions/" + q.ID + "/versions?scope=global", "/api/questions/search?scope=global&tier=formal&q=共享原始", "/api/questions?scope=global&tier=formal"} {
		r := serveGenerateJSON(t, h, "GET", path, viewer, nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), "共享原始") || strings.Contains(r.Body.String(), "私有第二版") {
			t.Errorf("snapshot read %s: %d %s", path, r.Code, r.Body)
		}
	}
	for _, path := range []string{"/api/questions/" + q.ID + "?scope=personal", "/api/questions/" + q.ID + "/versions?scope=personal", "/api/ai-check/result/" + q.ID, "/api/questions/" + q.ID + "/generation-context", "/api/review/task-by-question/" + q.ID} {
		r := serveGenerateJSON(t, h, "GET", path, viewer, nil)
		if r.Code != 403 {
			t.Errorf("private endpoint leaked: %s %d %s", path, r.Code, r.Body)
		}
	}
	download := func(token, file string) *httptest.ResponseRecorder {
		return serveGenerateJSON(t, h, "GET", "/api/export/download/"+url.PathEscape(file), token, nil)
	}
	if r := download(teacher, personalFile); r.Code != 409 {
		t.Fatal("personal old file must expire", r.Code, r.Body)
	}
	for _, file := range []string{globalXLSX, globalDOCX} {
		r := download(viewer, file)
		if r.Code != 200 {
			t.Fatal("global fixed file remains valid", r.Code, r.Body)
		}
		z, err := zip.NewReader(bytes.NewReader(r.Body.Bytes()), int64(r.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		var content strings.Builder
		for _, part := range z.File {
			if strings.HasSuffix(part.Name, ".xml") {
				rc, err := part.Open()
				if err != nil {
					t.Fatal(err)
				}
				b, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					t.Fatal(err)
				}
				content.Write(b)
			}
		}
		if !strings.Contains(content.String(), "共享原始") || strings.Contains(content.String(), "私有第二版") {
			t.Fatal("exported content drifted", file)
		}
	}
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID)); r.Code != 200 {
		t.Fatal("approved share must permit personal reconfirm", r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/questions/library-delete", teacher, libraryItemsPayload(t, s, q.ID)); r.Code != 409 {
		t.Fatal("shared association must block deletion", r.Code, r.Body)
	}
	globalUser, _ := s.authSvc.GetUserByUsername("c4-global")
	if _, err := s.authSvc.UpdateUserWithRoles(globalUser.ID, globalUser.DisplayName, globalUser.Role, globalUser.Roles, []string{domain.PermQuestionDownload}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := download(viewer, globalXLSX); r.Code != http.StatusForbidden {
		t.Fatal("download must recheck view rights", r.Code, r.Body)
	}
}
