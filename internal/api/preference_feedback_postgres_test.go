//go:build integration

package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestPreferenceSummarySeparatesCurrentAndHistoricalVersions(t *testing.T) {
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	const run = "preference-summary"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(run, p, 2))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	questions := waitWorkflowQuestions(t, h, token, run, 2)
	q := questions[0]
	path := "/api/questions/" + q.ID + "/preference-feedback"
	r = serveGenerateJSON(t, h, "PUT", path, token, map[string]any{"question_version": q.Version, "rating": "not_met", "revision": 0, "comment": "PRIVATE-SUMMARY-COMMENT"})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	q.Version++
	q.Explanation = ""
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	r = serveGenerateJSON(t, h, "PUT", path, token, map[string]any{"question_version": q.Version, "rating": "partial", "revision": 0})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	r = serveGenerateJSON(t, h, "GET", "/api/preference-feedback/summary", token, nil)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var result struct {
		Eligible int `json:"eligible_current_versions"`
		Unrated  int `json:"unrated_current_versions"`
		Current  struct {
			Partial int `json:"partial"`
			NotMet  int `json:"not_met"`
		} `json:"current"`
		Historical struct {
			NotMet int `json:"not_met"`
		} `json:"historical"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Eligible != 2 || result.Unrated != 1 || result.Current.Partial != 1 || result.Current.NotMet != 0 || result.Historical.NotMet != 1 {
		t.Fatal(result)
	}
	if strings.Contains(r.Body.String(), "PRIVATE-SUMMARY") || strings.Contains(r.Body.String(), q.ID) {
		t.Fatal("summary exposed private detail")
	}
	if g, c := client.counts(); g != 1 || c != 2 {
		t.Fatal("summary invoked model", g, c)
	}
	// Personal ownership is the same for all three personal tiers; global
	// formal/eliminated grants are not required to read one's own annotation.
	if _, err := store.DB().Exec(`UPDATE roles SET permissions=array_remove(array_remove(permissions,'question:view_formal'),'question:view_eliminated') WHERE id IN ('super_admin','admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE questions SET status='published' WHERE id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	if r = serveGenerateJSON(t, h, "GET", path, token, nil); r.Code != 200 {
		t.Fatal("own formal feedback incorrectly required global grants", r.Code, r.Body)
	}
	if _, err := store.DB().Exec(`UPDATE questions SET owner_id='other-summary-author' WHERE id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	r = serveGenerateJSON(t, h, "GET", "/api/preference-feedback/summary", token, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"eligible_current_versions":1`) || !strings.Contains(r.Body.String(), `"historical":{"met":0,"partial":0,"not_met":0}`) {
		t.Fatal("foreign author counted", r.Code, r.Body)
	}
	if _, err := store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'question:view') WHERE id IN ('super_admin','admin')`); err != nil {
		t.Fatal(err)
	}
	if r = serveGenerateJSON(t, h, "GET", "/api/preference-feedback/summary", token, nil); r.Code != 403 {
		t.Fatal("revoked old token retained summary", r.Code, r.Body)
	}
}

func TestPreferenceFeedbackVersionIsolationAndNoRecheck(t *testing.T) {
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	runID := "preference-feedback-run"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(runID, p, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	q := waitWorkflowQuestions(t, h, token, runID, 1)[0]
	path := "/api/questions/" + q.ID + "/preference-feedback"
	before, _ := store.GetQuestionGenerationContext(t.Context(), q.ID)
	frozenBefore, _ := json.Marshal(before)
	r = serveGenerateJSON(t, h, "GET", path, token, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"feedback":null`) {
		t.Fatal(r.Code, r.Body)
	}
	payload := map[string]any{"question_version": q.Version, "revision": 0, "rating": "partial", "comment": "私人反馈：病例顺序仍需调整"}
	r = serveGenerateJSON(t, h, "PUT", path, token, payload)
	var first domain.PreferenceFeedback
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &first) != nil || first.Revision != 1 || first.QuestionVersion != q.Version {
		t.Fatal(r.Code, r.Body)
	}
	if r = serveGenerateJSON(t, h, "PUT", path, token, payload); r.Code != 409 {
		t.Fatal("stale feedback write accepted", r.Code)
	}
	// Personal feedback is not a route to KB contents and does not require its grant.
	if _, err := store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'kb:view') WHERE id IN ('super_admin','admin')`); err != nil {
		t.Fatal(err)
	}
	payload["revision"] = 1
	payload["rating"] = "not_met"
	r = serveGenerateJSON(t, h, "PUT", path, token, payload)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	current, _ := store.GetQuestion(t.Context(), q.ID)
	if current.Version != q.Version || current.Status != domain.StatusAIReviewed || current.ClinicalStem != q.ClinicalStem {
		t.Fatal("annotation mutated question", current)
	}
	_, progress, e := s.aiCheckSvc.ProgressByQuestionIDs(t.Context(), []string{q.ID})
	if e != nil || progress["passed"] != 1 || progress["discarded"] != 0 {
		t.Fatal("personal dissatisfaction changed medical outcome", progress, e)
	}

	frozenAfter, _ := store.GetQuestionGenerationContext(t.Context(), q.ID)
	encoded, _ := json.Marshal(frozenAfter)
	if string(encoded) != string(frozenBefore) {
		t.Fatal("annotation rewrote generation evidence")
	}
	if g, c := client.counts(); g != 1 || c != 1 {
		t.Fatal("annotation invoked model", g, c)
	}
	logs, err := store.ListLogsByQuestion(t.Context(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	feedbackLogs := 0
	for _, log := range logs {
		if log.Action == "preference_feedback" {
			feedbackLogs++
			if strings.Contains(log.Detail, "私人反馈") {
				t.Fatal("private comment leaked into broad audit logs")
			}
		}
	}
	if feedbackLogs != 2 {
		t.Fatal("missing feedback audit", feedbackLogs)
	}
	// Each human-edited content version starts with no copied satisfaction rating.
	current.Version++
	current.Explanation = ""
	if err = store.SaveQuestion(t.Context(), *current); err != nil {
		t.Fatal(err)
	}
	r = serveGenerateJSON(t, h, "GET", path, token, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"feedback":null`) {
		t.Fatal("old feedback attributed to new version", r.Code, r.Body)
	}
	if r = serveGenerateJSON(t, h, "PUT", path, token, payload); r.Code != 409 {
		t.Fatal("old content version wrote after edit", r.Code)
	}
	payload["question_version"] = current.Version
	payload["revision"] = 0
	// Concurrent old windows: exactly one succeeds; the other must refresh.
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Go(func() { response := serveGenerateJSON(t, h, "PUT", path, token, payload); codes <- response.Code })
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	var n int
	if err = store.DB().QueryRow(`SELECT count(*) FROM question_preference_feedback WHERE question_id=$1`, q.ID).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	// Read-only state cases are independent fixtures. A rejection/deletion
	// tombstone must never be rewritten merely to prepare the next test state.
	for _, status := range []domain.QuestionStatus{domain.StatusReviewing, domain.StatusPublished, domain.StatusRejected, domain.StatusArchived} {
		original, err := store.GetQuestionVersion(t.Context(), q.ID, 1)
		if err != nil || original == nil {
			t.Fatal("missing original test snapshot", err)
		}
		frozen, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
		if err != nil {
			t.Fatal(err)
		}
		sealed := original.Snapshot
		sealed.GenerationContext = frozen
		sealed.ID = q.ID + "-sealed-" + string(status)
		sealed.Version = 1
		sealed.Status = status
		sealed.LibraryState, sealed.ConfirmationSource = domain.LibraryStateBackfill(status)
		sealed.ConfirmedVersion = 0
		sealed.DeletedReason = ""
		if status == domain.StatusPublished {
			sealed.ConfirmedVersion = sealed.Version
		} else if status == domain.StatusRejected {
			sealed.ConfirmationSource = ""
			sealed.DeletedReason = domain.DeleteReasonLegacyRejected
		} else if status == domain.StatusArchived {
			sealed.ConfirmationSource = ""
			sealed.DeletedReason = domain.DeleteReasonLegacyArchive
		}
		if err = store.SaveQuestion(t.Context(), sealed); err != nil {
			t.Fatal(err)
		}
		payload["revision"] = 0
		payload["question_version"] = sealed.Version
		r = serveGenerateJSON(t, h, "PUT", "/api/questions/"+sealed.ID+"/preference-feedback", token, payload)
		if r.Code != 409 {
			t.Fatal("sealed/review state accepted annotation", status, r.Code)
		}
	}
	if g, c := client.counts(); g != 1 || c != 1 {
		t.Fatal("human edit/status transition rechecked", g, c)
	}
}
func TestPreferenceFeedbackRejectsForeignRevokedAndMalformedWrites(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload("preference-permissions", p, 1))
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	q := waitWorkflowQuestions(t, h, token, "preference-permissions", 1)[0]
	path := "/api/questions/" + q.ID + "/preference-feedback"
	base := map[string]any{"question_version": q.Version, "revision": 0, "rating": "met"}
	for _, bad := range []map[string]any{{"question_version": 0, "revision": 0, "rating": "met"}, {"question_version": 1, "revision": -1, "rating": "met"}, {"question_version": 1, "revision": 0, "rating": "pass"}, {"question_version": 1, "revision": 0, "rating": "met", "comment": strings.Repeat("字", 1001)}, {"question_version": 1, "revision": 0, "rating": "met", "comment": "bad\x00value"}} {
		if r = serveGenerateJSON(t, h, "PUT", path, token, bad); r.Code != 400 {
			t.Fatal("malformed annotation accepted", r.Code)
		}
	}
	r = serveGenerateJSON(t, h, "POST", "/api/users", token, map[string]any{"username": "feedback-other", "password": "feedback-password", "permissions": []string{domain.PermQuestionGenerate, domain.PermQuestionView, domain.PermQuestionViewGlobal}})
	var other auth.User
	if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &other) != nil {
		t.Fatal(r.Code, r.Body)
	}
	otherToken := loginForAuthTest(t, h, other.Username, "feedback-password", "198.51.100.212")
	for _, method := range []string{"GET", "PUT"} {
		r = serveGenerateJSON(t, h, method, path, otherToken, base)
		if r.Code != 403 && r.Code != 404 {
			t.Fatal("foreign author feedback exposed", method, r.Code)
		}
	}
	if _, err := store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'question:generate') WHERE id IN ('super_admin','admin')`); err != nil {
		t.Fatal(err)
	}
	if r = serveGenerateJSON(t, h, "PUT", path, token, base); r.Code != 403 {
		t.Fatal("revoked old token retained feedback write", r.Code)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM question_preference_feedback`).Scan(&count); err != nil || count != 0 {
		t.Fatal(fmt.Sprint("bad write left feedback: ", count), err)
	}
}

func TestPreferenceFeedbackAuditFailureRollsBackAnnotation(t *testing.T) {
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload("preference-audit-rollback", p, 1))
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	q := waitWorkflowQuestions(t, h, token, "preference-audit-rollback", 1)[0]
	_, err := store.DB().Exec(`CREATE FUNCTION reject_preference_audit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='preference_feedback' THEN RAISE EXCEPTION 'isolated feedback audit fault'; END IF; RETURN NEW; END $$;CREATE TRIGGER preference_audit_fault BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_preference_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/questions/" + q.ID + "/preference-feedback"
	input := map[string]any{"question_version": q.Version, "revision": 0, "rating": "met"}
	r = serveGenerateJSON(t, h, "PUT", path, token, input)
	if r.Code != 503 {
		t.Fatal("injected audit failure not surfaced", r.Code)
	}
	var count int
	if err = store.DB().QueryRow(`SELECT count(*) FROM question_preference_feedback WHERE question_id=$1`, q.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("annotation escaped audit rollback", count, err)
	}
	if _, err = store.DB().Exec(`DROP TRIGGER preference_audit_fault ON audit_logs;DROP FUNCTION reject_preference_audit()`); err != nil {
		t.Fatal(err)
	}
	if r = serveGenerateJSON(t, h, "PUT", path, token, input); r.Code != 200 {
		t.Fatal("same revision not retryable after rollback", r.Code)
	}
	// Historical ownership changes must not reveal an earlier author's private note.
	if _, err = store.DB().Exec(`UPDATE question_preference_feedback SET author_id='different-author',comment='PRIVATE-OLD-AUTHOR' WHERE question_id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	if r = serveGenerateJSON(t, h, "GET", path, token, nil); r.Code != 403 || strings.Contains(r.Body.String(), "PRIVATE-OLD-AUTHOR") {
		t.Fatal("foreign historical feedback leaked", r.Code)
	}

	if g, c := client.counts(); g != 1 || c != 1 {
		t.Fatal("feedback failure retried medical checking", g, c)
	}
}
