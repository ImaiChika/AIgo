//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/review"
)

func TestReviewRestoreAPIPermissionsAndOwnerWorkspace(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	root := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.210")
	for _, spec := range []struct {
		name, role  string
		roles       []string
		permissions []string
	}{
		{"restore-owner", "teacher", []string{"teacher", "expert"}, nil},
		{"restore-admin", "admin", []string{"admin", "expert"}, nil},
		{"restore-manager-only", "expert", []string{"expert"}, []string{"user:manage"}},
		{"restore-only", "expert", []string{"expert"}, []string{"review:restore"}},
	} {
		res := serveAuthJSON(t, handler, http.MethodPost, "/api/users", root, "198.51.100.210", map[string]any{"username": spec.name, "password": "restore-api-password", "display_name": spec.name, "role": spec.role, "roles": spec.roles, "permissions": spec.permissions})
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", spec.name, res.Code, res.Body)
		}
	}
	owner, err := server.authSvc.GetUserByUsername("restore-owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerToken := loginForAuthTest(t, handler, "restore-owner", "restore-api-password", "198.51.100.211")
	manager := loginForAuthTest(t, handler, "restore-admin", "restore-api-password", "198.51.100.212")
	noRestore := loginForAuthTest(t, handler, "restore-manager-only", "restore-api-password", "198.51.100.213")
	onlyRestore := loginForAuthTest(t, handler, "restore-only", "restore-api-password", "198.51.100.215")
	q := domain.A2Question{ID: "restore-api-question", ClinicalStem: "患者，男，50岁，持续胸痛2小时，最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "诊断甲"}, {Label: "B", Text: "诊断乙"}, {Label: "C", Text: "诊断丙"}, {Label: "D", Text: "诊断丁"}}, Answer: "A", Status: domain.StatusAIReviewed, Version: 1, OwnerID: owner.ID, CreatedBy: owner.Username, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "restore-api-ai", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "restore-api-flow", Name: "还原API流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "一轮", ExpertIDs: []string{owner.ID}, RequiredCount: 1}}}
	if err := server.reviewSvc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	submitted := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", ownerToken, "198.51.100.211", map[string]any{"question_id": q.ID, "flow_id": flow.ID})
	if submitted.Code != 200 {
		t.Fatalf("submit: %d %s", submitted.Code, submitted.Body)
	}
	var task domain.ReviewTask
	if err := json.Unmarshal(submitted.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{ownerToken, noRestore} {
		if r := serveAuthJSON(t, handler, http.MethodGet, "/api/review/restore-candidates", token, "198.51.100.214", nil); r.Code != 403 {
			t.Fatalf("unauthorized restore list: %d %s", r.Code, r.Body)
		}
	}
	listed := serveAuthJSON(t, handler, http.MethodGet, "/api/review/restore-candidates", manager, "198.51.100.212", nil)
	var page struct {
		Items []domain.ReviewRestoreCandidate `json:"items"`
		Total int                             `json:"total"`
	}
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &page) != nil || page.Total != 1 || page.Items[0].CreatorUsername != owner.Username {
		t.Fatalf("management list: %d %s", listed.Code, listed.Body)
	}
	options := serveAuthJSON(t, handler, http.MethodGet, "/api/review/restore-options", onlyRestore, "198.51.100.215", nil)
	var facets domain.ReviewRestoreOptions
	if options.Code != 200 || json.Unmarshal(options.Body.Bytes(), &facets) != nil || len(facets.Flows) != 1 || facets.Flows[0].ID != flow.ID || len(facets.Owners) != 1 || facets.Owners[0].ID != owner.ID {
		t.Fatalf("restore filters must not depend on user/flow management: %d %s", options.Code, options.Body)
	}
	if denied := serveAuthJSON(t, handler, http.MethodGet, "/api/review/restore-options", noRestore, "198.51.100.213", nil); denied.Code != 403 {
		t.Fatalf("restore filter metadata leaked: %d %s", denied.Code, denied.Body)
	}
	detail := serveAuthJSON(t, handler, http.MethodGet, "/api/review/restore-candidates/"+task.ID, manager, "198.51.100.212", nil)
	if detail.Code != 200 {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body)
	}
	var candidate domain.ReviewRestoreCandidate
	if json.Unmarshal(detail.Body.Bytes(), &candidate) != nil || candidate.People[owner.ID] == "" {
		t.Fatal("expert names missing from authorized detail")
	}
	if unrelated := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/"+q.ID, onlyRestore, "198.51.100.215", nil); unrelated.Code != 403 {
		t.Fatalf("restore permission broadened normal question access: %d %s", unrelated.Code, unrelated.Body)
	}
	previewResponse := serveAuthJSON(t, handler, http.MethodPost, "/api/review/restore-preview", manager, "198.51.100.212", map[string]any{"task_ids": []string{task.ID}})
	var preview review.RestorePreview
	if previewResponse.Code != 200 || json.Unmarshal(previewResponse.Body.Bytes(), &preview) != nil {
		t.Fatalf("preview: %d %s", previewResponse.Code, previewResponse.Body)
	}
	restored := serveAuthJSON(t, handler, http.MethodPost, "/api/review/restore", manager, "198.51.100.212", review.RestoreRequest{OperationID: "restore-api-operation", PreviewToken: preview.Token, Reason: "重新选择审核流程"})
	if restored.Code != 200 {
		t.Fatalf("restore: %d %s", restored.Code, restored.Body)
	}
	newQuestions := serveAuthJSON(t, handler, http.MethodGet, "/api/questions/my-new", ownerToken, "198.51.100.211", nil)
	var qs struct {
		Total int `json:"total"`
	}
	if newQuestions.Code != 200 || json.Unmarshal(newQuestions.Body.Bytes(), &qs) != nil || qs.Total != 1 {
		t.Fatalf("question vanished from author workspace: %d %s", newQuestions.Code, newQuestions.Body)
	}
	latest, _ := store.GetQuestion(t.Context(), q.ID)
	if latest.Version != 1 || latest.ReviewRevision != 1 || latest.Status != domain.StatusAIReviewed {
		t.Fatal("restoration changed content version")
	}
	// Once the task is restored, a template-only reference must not lock reviewer qualification.
	changeRole := func(roles []string) int {
		r := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+owner.ID, root, "198.51.100.210", map[string]any{"display_name": owner.DisplayName, "role": "teacher", "roles": roles, "permissions": []string{}, "bank_ids": []string{}, "enabled": true})
		return r.Code
	}
	if code := changeRole([]string{"teacher"}); code != 200 {
		t.Fatalf("template-only reference blocked revocation after restore: %d", code)
	}
	unavailable := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", ownerToken, "198.51.100.211", map[string]any{"question_id": q.ID, "flow_id": flow.ID, "expected_review_revision": 1, "expected_version": 1})
	if unavailable.Code != 409 {
		t.Fatalf("invalid template accepted new work: %d %s", unavailable.Code, unavailable.Body)
	}
	if code := changeRole([]string{"teacher", "expert"}); code != 200 {
		t.Fatalf("restore reviewer qualification: %d", code)
	}
	// Old request must not put the same unchanged content version back into the previous flow.
	old := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", ownerToken, "198.51.100.211", map[string]any{"question_id": q.ID, "flow_id": flow.ID})
	if old.Code != 409 {
		t.Fatalf("old submission survived restoration: %d %s", old.Code, old.Body)
	}
	fresh := serveAuthJSON(t, handler, http.MethodPost, "/api/review/submit", ownerToken, "198.51.100.211", map[string]any{"question_id": q.ID, "flow_id": flow.ID, "expected_review_revision": 1, "expected_version": 1})
	if fresh.Code != 200 {
		t.Fatalf("fresh submit: %d %s", fresh.Code, fresh.Body)
	}
}

func TestReviewAndAuditShareTransaction(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	h := server.Handler()
	token := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.220")
	user, _ := server.authSvc.GetUserByUsername("admin")
	q := domain.A2Question{ID: "audit-atomic-question", ClinicalStem: "患者，男，50岁，持续胸痛2小时，最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}}, Answer: "A", Status: domain.StatusAIReviewed, Version: 1, OwnerID: user.ID, CreatedBy: user.Username, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "audit-atomic-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "audit-atomic-flow", Name: "审计事务", Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{user.ID}, RequiredCount: 1}}}
	if err := server.reviewSvc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	install := func(action string) {
		t.Helper()
		_, err := store.DB().Exec(`CREATE FUNCTION reject_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='` + action + `' THEN RAISE EXCEPTION 'injected audit write failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_review_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_review_audit()`)
		if err != nil {
			t.Fatal(err)
		}
	}
	remove := func() {
		t.Helper()
		if _, err := store.DB().Exec(`DROP TRIGGER reject_review_audit ON audit_logs; DROP FUNCTION reject_review_audit()`); err != nil {
			t.Fatal(err)
		}
	}
	install("submit")
	r := serveAuthJSON(t, h, http.MethodPost, "/api/review/submit", token, "198.51.100.220", map[string]any{"question_id": q.ID, "flow_id": flow.ID})
	if r.Code != 500 {
		t.Fatalf("submit failure: %d %s", r.Code, r.Body)
	}
	current, _ := store.GetQuestion(t.Context(), q.ID)
	task, _ := store.GetTaskByQuestionID(t.Context(), q.ID)
	if current.Status != domain.StatusAIReviewed || task != nil {
		t.Fatal("submit committed without its audit")
	}
	remove()
	r = serveAuthJSON(t, h, http.MethodPost, "/api/review/submit", token, "198.51.100.220", map[string]any{"question_id": q.ID, "flow_id": flow.ID})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	task, _ = store.GetTaskByQuestionID(t.Context(), q.ID)
	install("review")
	r = serveAuthJSON(t, h, http.MethodPost, "/api/review/action", token, "198.51.100.220", map[string]any{"task_id": task.ID, "action": "approved"})
	if r.Code != 500 {
		t.Fatalf("vote failure: %d %s", r.Code, r.Body)
	}
	current, _ = store.GetQuestion(t.Context(), q.ID)
	records, _ := store.ListRecordsByTaskID(t.Context(), task.ID)
	if current.Status != domain.StatusReviewing || len(records) != 0 {
		t.Fatal("vote committed without audit")
	}
	remove()
	r = serveAuthJSON(t, h, http.MethodPost, "/api/review/action", token, "198.51.100.220", map[string]any{"task_id": task.ID, "action": "approved"})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	beforeFinal, _ := store.GetQuestion(t.Context(), q.ID)
	// The live model stores awaiting-final in task.status while the question can remain reviewing.
	candidates, total, e := store.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{Status: "conflict"}, 1, 20, false)
	if e != nil || total != 1 || !candidates[0].Eligible {
		t.Fatalf("live awaiting-final omitted: %d %+v %v", total, candidates, e)
	}
	install("final_approved")
	r = serveAuthJSON(t, h, http.MethodPost, "/api/review/finalize", token, "198.51.100.220", map[string]any{"task_id": task.ID, "action": "approved"})
	if r.Code != 500 {
		t.Fatalf("final failure: %d %s", r.Code, r.Body)
	}
	current, _ = store.GetQuestion(t.Context(), q.ID)
	fresh, _ := store.GetTask(t.Context(), task.ID)
	records, _ = store.ListRecordsByTaskID(t.Context(), task.ID)
	if current.Status != beforeFinal.Status || fresh.Status != domain.StatusConflict || fresh.FinalDecision != nil || len(records) != 1 {
		t.Fatalf("final publication committed without audit: question=%s final=%+v records=%d", current.Status, fresh.FinalDecision, len(records))
	}
}

func TestRevisionResubmitCanChooseNewFlowAndPreservesReason(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	h := server.Handler()
	rootToken := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.230")
	root, _ := server.authSvc.GetUserByUsername("admin")
	ownerID := guardCreateUser(t, h, rootToken, map[string]any{"username": "revision-flow-owner", "password": "revision-flow-password", "display_name": "退修作者", "role": "teacher"})
	ownerToken := loginForAuthTest(t, h, "revision-flow-owner", "revision-flow-password", "198.51.100.231")
	q := domain.A2Question{ID: "revision-api-switch", ClinicalStem: "患者，男，50岁，持续胸痛2小时，最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}}, Answer: "A", Status: domain.StatusAIReviewed, Version: 1, OwnerID: ownerID, CreatedBy: "revision-flow-owner", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "revision-api-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"revision-api-old", "revision-api-new"} {
		if err := server.reviewSvc.CreateFlow(t.Context(), domain.ReviewFlowConfig{ID: id, Name: id, Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{root.ID}, RequiredCount: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	must := func(method, path, token string, body any) []byte {
		t.Helper()
		r := serveAuthJSON(t, h, method, path, token, "198.51.100.232", body)
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body)
		}
		return r.Body.Bytes()
	}
	var original domain.ReviewTask
	if err := json.Unmarshal(must(http.MethodPost, "/api/review/submit", ownerToken, map[string]any{"question_id": q.ID, "flow_id": "revision-api-old"}), &original); err != nil {
		t.Fatal(err)
	}
	must(http.MethodPost, "/api/review/action", rootToken, map[string]any{"task_id": original.ID, "action": "revision_required", "opinion": "补充胸痛的伴随症状"})
	body := map[string]any{"question_ids": []string{q.ID}, "flow_id": "revision-api-new", "expectations": map[string]any{q.ID: domain.ReviewExpectation{Version: 1, PreviousTaskID: original.ID}}}
	var result struct {
		Submitted int `json:"submitted"`
	}
	if err := json.Unmarshal(must(http.MethodPost, "/api/review/resubmit-revisions", ownerToken, body), &result); err != nil || result.Submitted != 0 {
		t.Fatal("unmodified revision resubmitted")
	}
	var beforeEdit domain.A2Question
	if err := json.Unmarshal(must(http.MethodGet, "/api/questions/"+q.ID, ownerToken, nil), &beforeEdit); err != nil {
		t.Fatal(err)
	}
	var saved domain.A2Question
	if err := json.Unmarshal(must(http.MethodPut, "/api/questions/"+q.ID, ownerToken, map[string]any{"clinical_stem": beforeEdit.ClinicalStem + "？", "options": beforeEdit.Options, "answer": beforeEdit.Answer, "change_reason": "补充症状", "expected_version": beforeEdit.Version, "expected_review_revision": beforeEdit.ReviewRevision, "expected_library_revision": beforeEdit.LibraryRevision}), &saved); err != nil || saved.Version != 2 {
		t.Fatalf("new content version: %+v %v", saved, err)
	}
	body["expectations"] = map[string]any{q.ID: domain.ReviewExpectation{Version: saved.Version, ReviewRevision: saved.ReviewRevision, PreviousTaskID: original.ID}}
	if err := json.Unmarshal(must(http.MethodPost, "/api/review/resubmit-revisions", ownerToken, body), &result); err != nil || result.Submitted != 1 {
		t.Fatal("saved revision failed to choose new flow")
	}
	latest, _ := store.GetTaskByQuestionID(t.Context(), q.ID)
	if latest.ID == original.ID || latest.FlowID != "revision-api-new" || latest.PreviousTaskID != original.ID || latest.CurrentRound != 1 {
		t.Fatalf("new assignment: %+v", latest)
	}
	var history struct {
		Items []review.ReviewHistoryItem `json:"items"`
	}
	if err := json.Unmarshal(must(http.MethodGet, "/api/questions/"+q.ID+"/review-history", ownerToken, nil), &history); err != nil || len(history.Items) != 2 {
		t.Fatalf("history: %+v %v", history, err)
	}
	if history.Items[0].FlowID != "revision-api-old" || !history.Items[0].Superseded || len(history.Items[0].Records) != 1 || !strings.Contains(history.Items[0].Records[0].Opinion, "补充胸痛的伴随症状") {
		t.Fatalf("revision cause lost: %+v", history.Items[0])
	}
	must(http.MethodDelete, "/api/review/flows/revision-api-old", rootToken, nil)
}
