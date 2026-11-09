//go:build integration

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

func TestKnowledgeImportExpiredAndForeignConfirmation(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	req := knowledgebase.ImportRequest{VersionID: p.VersionID, Document: importTestDocument(t, p.OutlineCode)}
	r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/preview", token, req)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var preview knowledgebase.ImportPreview
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(preview.Token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	claims["ExpiresAt"] = time.Now().Add(-time.Minute).Unix()
	raw, _ = json.Marshal(claims)
	mac := hmac.New(sha256.New, s.authSvc.KnowledgeImportSigningKey())
	mac.Write(raw)
	req.Token = base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, req); r.Code != 409 {
		t.Fatal("expired preview accepted", r.Code, r.Body)
	}
	created := serveGenerateJSON(t, h, "POST", "/api/users", token, map[string]any{"username": "import-other", "password": "isolated-import-password", "permissions": []string{"kb:view", "kb:manage"}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	other := loginForAuthTest(t, h, "import-other", "isolated-import-password", "198.51.100.210")
	req.Token = preview.Token
	if r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", other, req); r.Code != 409 {
		t.Fatal("foreign preview accepted", r.Code, r.Body)
	}
	req.Token = strings.Repeat(".", 100000)
	if r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, req); r.Code != 409 {
		t.Fatal("oversized confirmation token accepted", r.Code)
	}
	var count int
	if err = store.DB().QueryRow(`SELECT count(*) FROM knowledge_import_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestKnowledgeImportFiftyDraftBoundary(t *testing.T) {
	_, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	doc := knowledgebase.ImportDocument{Format: knowledgebase.ImportFormat}
	for i := 0; i < 50; i++ {
		doc.Cards = append(doc.Cards, knowledgebase.ImportCard{Kind: "custom", Title: fmt.Sprintf("边界草稿%d", i), PointCodes: []string{p.OutlineCode}, Content: domain.KnowledgeContent{CustomText: "明确标识的隔离测试资料"}})
	}
	raw, _ := json.Marshal(doc)
	req := knowledgebase.ImportRequest{VersionID: p.VersionID, Document: string(raw)}
	r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/preview", token, req)
	var preview knowledgebase.ImportPreview
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &preview) != nil || !preview.Valid || len(preview.Rows) != 50 {
		t.Fatal(r.Code, r.Body)
	}
	req.Token = preview.Token
	r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, req)
	var saved knowledgebase.ImportResult
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &saved) != nil || saved.Saved != 50 {
		t.Fatal(r.Code, r.Body)
	}
	var drafts, questions int
	if err := store.DB().QueryRow(`SELECT (SELECT count(*) FROM knowledge_cards WHERE status='draft'),(SELECT count(*) FROM questions)`).Scan(&drafts, &questions); err != nil || drafts != 50 || questions != 0 {
		t.Fatal(drafts, questions, err)
	}
	if g, c := client.counts(); g != 0 || c != 0 {
		t.Fatal(g, c)
	}
}

func importTestDocument(t *testing.T, code string) string {
	t.Helper()
	doc := knowledgebase.ImportDocument{Format: knowledgebase.ImportFormat, Cards: []knowledgebase.ImportCard{
		{Kind: "medical", Title: "导入考点", PointCodes: []string{code}, Source: "隔离模拟资料", Content: domain.KnowledgeContent{ExamTargets: []string{"导入目标"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "导入事实，仅测试"}}}},
		{Kind: "custom", Title: "导入自定义", PointCodes: []string{code}, Content: domain.KnowledgeContent{CustomText: "导入参考，仅测试"}},
	}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestKnowledgeImportPreviewAtomicConfirmAndReplay(t *testing.T) {
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	req := knowledgebase.ImportRequest{VersionID: p.VersionID, Document: importTestDocument(t, p.OutlineCode)}
	preview := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/preview", token, req)
	if preview.Code != 200 {
		t.Fatal(preview.Code, preview.Body)
	}
	var result knowledgebase.ImportPreview
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil || !result.Valid || len(result.Rows) != 2 || result.Token == "" {
		t.Fatal(result, err)
	}
	var cards, receipts int
	if err := store.DB().QueryRow(`SELECT (SELECT count(*) FROM knowledge_cards),(SELECT count(*) FROM knowledge_import_receipts)`).Scan(&cards, &receipts); err != nil || cards != 1 || receipts != 0 {
		t.Fatal("preview wrote data", cards, receipts, err)
	}
	req.Token = result.Token
	responses := make(chan *knowledgebase.ImportResult, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, req)
			if r.Code != 200 {
				t.Error(r.Code, r.Body)
				return
			}
			var saved knowledgebase.ImportResult
			if e := json.Unmarshal(r.Body.Bytes(), &saved); e != nil {
				t.Error(e)
				return
			}
			responses <- &saved
		}()
	}
	wg.Wait()
	close(responses)
	var ids string
	for r := range responses {
		raw, _ := json.Marshal(r)
		if r.Saved != 2 || (ids != "" && ids != string(raw)) {
			t.Fatal("repeat confirm duplicated cards", r)
		}
		ids = string(raw)
		for _, c := range r.Cards {
			if c.Status != "draft" || c.Revision != 1 {
				t.Fatal(c)
			}
		}
	}
	if ids == "" {
		t.Fatal("no successful confirmation")
	}
	var questions, tasks int
	if err := store.DB().QueryRow(`SELECT (SELECT count(*) FROM knowledge_cards),(SELECT count(*) FROM knowledge_import_receipts),(SELECT count(*) FROM questions),(SELECT count(*) FROM ai_check_tasks)`).Scan(&cards, &receipts, &questions, &tasks); err != nil || cards != 3 || receipts != 1 || questions != 0 || tasks != 0 {
		t.Fatal(cards, receipts, questions, tasks, err)
	}
	if g, c := client.counts(); g != 0 || c != 0 {
		t.Fatal("reference import called models", g, c)
	}
	changed := req
	changed.Document = strings.Replace(req.Document, "导入考点", "被篡改", 1)
	if r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, changed); r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
	// A finished import is replayed rather than rebound after syllabus edits.
	current, err := store.GetPoint(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Topic = "改版考点"
	if err = store.UpdatePoint(t.Context(), *current); err != nil {
		t.Fatal(err)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, req); r.Code != 200 || r.Body.String() != ids+"\n" {
		t.Fatal("replay re-resolved the old import", r.Code, r.Body)
	}
	_ = s
}

func TestKnowledgeImportStalePreviewRevocationAndRollback(t *testing.T) {
	_, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	request := knowledgebase.ImportRequest{VersionID: p.VersionID, Document: importTestDocument(t, p.OutlineCode)}
	prepare := func() knowledgebase.ImportRequest {
		t.Helper()
		r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/preview", token, request)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		var v knowledgebase.ImportPreview
		if e := json.Unmarshal(r.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		copy := request
		copy.Token = v.Token
		return copy
	}
	stale := prepare()
	point, err := store.GetPoint(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	point.Topic = "预览后修改"
	if err = store.UpdatePoint(t.Context(), *point); err != nil {
		t.Fatal(err)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, stale); r.Code != 409 {
		t.Fatal("stale preview accepted", r.Code, r.Body)
	}
	fresh := prepare()
	if _, err = store.DB().Exec(`CREATE FUNCTION fail_import_card() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.body->>'title'='导入自定义' THEN RAISE EXCEPTION 'injected import write failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_import_card BEFORE INSERT ON knowledge_cards FOR EACH ROW EXECUTE FUNCTION fail_import_card()`); err != nil {
		t.Fatal(err)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, fresh); r.Code != 503 {
		t.Fatal(r.Code, r.Body)
	}
	var cards, receipts int
	if err = store.DB().QueryRow(`SELECT (SELECT count(*) FROM knowledge_cards),(SELECT count(*) FROM knowledge_import_receipts)`).Scan(&cards, &receipts); err != nil || cards != 1 || receipts != 0 {
		t.Fatal("partial import survived rollback", cards, receipts, err)
	}
	if _, err = store.DB().Exec(`DROP TRIGGER fail_import_card ON knowledge_cards; DROP FUNCTION fail_import_card(); UPDATE roles SET permissions=array_remove(permissions,'kb:manage') WHERE id IN ('super_admin','admin')`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"preview", "confirm"} {
		r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/"+path, token, fresh)
		if r.Code != 403 {
			t.Fatal("old token bypassed revoke", path, r.Code, r.Body)
		}
	}
}

func TestKnowledgeImportInvalidRowsAndVersionScope(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	request := knowledgebase.ImportRequest{VersionID: p.VersionID, Document: importTestDocument(t, "missing-code")}
	r := serveGenerateJSON(t, h, http.MethodPost, "/api/knowledge-base/import/preview", token, request)
	var preview knowledgebase.ImportPreview
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &preview) != nil || preview.Valid || preview.Token != "" || len(preview.Rows) != 2 || preview.Rows[0].Error == "" {
		t.Fatal(r.Code, r.Body)
	}
	other, err := s.kpSvc.CreateVersion(t.Context(), "另一大纲", 2027, "")
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: other.ID, Category: p.Category, Subject: p.Subject, Unit: p.Unit, OutlineCode: p.OutlineCode, Topic: "同码不同点"})
	if err != nil {
		t.Fatal(err)
	}
	request.Document = importTestDocument(t, p.OutlineCode)
	request.VersionID = other.ID
	r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/preview", token, request)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &preview) != nil || !preview.Valid || preview.Rows[0].Card.KnowledgePoints[0].ID != op.ID {
		t.Fatal("cross-year mapping", r.Code, r.Body)
	}
	if _, err = store.DB().Exec(`UPDATE knowledge_versions SET deleted_at=NOW() WHERE id=$1`, other.ID); err != nil {
		t.Fatal(err)
	}
	request.Token = preview.Token
	if r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, request); r.Code != 409 {
		t.Fatal("deleted outline restored by import", r.Code, r.Body)
	}
}
