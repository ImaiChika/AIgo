//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
)

func modifiedLearningSource(t *testing.T, s *Server, h http.Handler, teacher, v, id string) *domain.A2Question {
	t.Helper()
	q := seedPassedQuestion(t, s, h, teacher, v, id, "男，45岁。旧病例表达。该患者最可能的诊断是")
	edit := learningRequest(&q, "")
	delete(edit, "request_key")
	edit["clinical_stem"] = "男，45岁。已保存的新病例表达。该患者最可能的诊断是"
	if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, teacher, edit); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	return mustLibraryQuestion(t, s, q.ID)
}

func submitLearningFixture(t *testing.T, h http.Handler, teacher string, q *domain.A2Question, key string) domain.LearningJob {
	t.Helper()
	r := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/learning-analyses", teacher, learningRequest(q, key))
	var body struct {
		Job domain.LearningJob `json:"job"`
	}
	if r.Code != 202 || json.Unmarshal(r.Body.Bytes(), &body) != nil {
		t.Fatal(r.Code, r.Body)
	}
	return body.Job
}
func learningImportFixture(job domain.LearningJob, q *domain.A2Question, key string) domain.LearningImportRequest {
	lib, review := q.LibraryRevision, q.ReviewRevision
	candidate := job.Candidates[0]
	return domain.LearningImportRequest{LearningExpectation: domain.LearningExpectation{QuestionID: q.ID, ExpectedVersion: q.Version, ExpectedLibraryRevision: &lib, ExpectedReviewRevision: &review}, JobID: job.ID, ExpectedRevision: job.Revision, ImportKey: key, Items: []domain.LearningImportItem{{CandidateID: candidate.ID, Title: candidate.Title, Scope: candidate.Scope, Rules: append([]domain.LearningRule{}, candidate.Rules...), Status: "draft"}}}
}

func TestLearningConfirmOnlyKeepsPaidResultAndRequiresFreshWriteGuards(t *testing.T) {
	s, h, teacher, v, client, cleanup := learningFlowEnv(t)
	defer cleanup()
	q := modifiedLearningSource(t, s, h, teacher, v, "learning-confirm-only")
	initial := submitLearningFixture(t, h, teacher, q, "confirm-analysis")
	job := waitLearningJob(t, h, teacher, initial.ID)
	if job.Status != "succeeded" {
		t.Fatal(job)
	}
	stale := learningImportFixture(job, q, "confirm-import")
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/import", teacher, stale); r.Code != 409 {
		t.Fatal("old library guard accepted", r.Code, r.Body)
	}
	job = waitLearningJob(t, h, teacher, initial.ID)
	if !job.SourceCurrent || !job.CanImport {
		t.Fatal("confirmation alone discarded paid content comparison", job)
	}
	fresh := learningImportFixture(job, mustLibraryQuestion(t, s, q.ID), "confirm-import")
	if r := serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/import", teacher, fresh); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if client.count() != 1 {
		t.Fatal("status-only confirmation required another model call", client.count())
	}
}

func TestLearningMalformedReplyAndTimeoutAreBoundedWithoutChangingQuestion(t *testing.T) {
	for _, mode := range []string{"invalid_protocol", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s, h, teacher, v, client, cleanup := learningFlowEnv(t)
			defer cleanup()
			q := modifiedLearningSource(t, s, h, teacher, v, "learning-fail-"+mode)
			if mode == "invalid_protocol" {
				raw := `{"candidates":[],"private-secret":"never-echo"}`
				client.analysisReply = &raw
			} else {
				client.analysisGate = make(chan struct{})
				s.learningSvc.Budget = 100 * time.Millisecond
			}
			initial := submitLearningFixture(t, h, teacher, q, "failed-analysis")
			job := waitLearningJob(t, h, teacher, initial.ID)
			if job.Status != "failed" || job.CanImport || len(job.Candidates) != 0 || strings.Contains(job.Error, "never-echo") {
				t.Fatal(job)
			}
			want := 2
			if mode == "timeout" {
				want = 1
				if job.ErrorKind != "timeout" {
					t.Fatal(job)
				}
			}
			if client.count() != want || job.Attempts != want {
				t.Fatal("unexpected unbounded retries", job.Attempts, client.count())
			}
			current := mustLibraryQuestion(t, s, q.ID)
			if current.Version != q.Version || current.Status != q.Status || current.LibraryRevision != q.LibraryRevision || current.ClinicalStem != q.ClinicalStem {
				t.Fatal("failed analysis changed source", current)
			}
			_, checks := client.base.counts()
			if checks != 0 {
				t.Fatal("failed analysis triggered first check", checks)
			}
		})
	}
}

func TestLearningConcurrentSourceRevisionRetainsOnlyStaleAnalysisEvidence(t *testing.T) {
	s, h, teacher, v, client, cleanup := learningFlowEnv(t)
	defer cleanup()
	q := modifiedLearningSource(t, s, h, teacher, v, "learning-mid-call")
	gate := make(chan struct{})
	client.analysisGate = gate
	client.analysisStarted = make(chan struct{}, 1)
	initial := submitLearningFixture(t, h, teacher, q, "stale-analysis")
	select {
	case <-client.analysisStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	edit := learningRequest(q, "")
	delete(edit, "request_key")
	edit["clinical_stem"] = "男，45岁。分析期间再次保存的第三版。该患者最可能的诊断是"
	if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, teacher, edit); r.Code != 200 {
		t.Fatal("model call held source transaction", r.Code, r.Body)
	}
	close(gate)
	job := waitLearningJob(t, h, teacher, initial.ID)
	if job.Status != "succeeded" || job.SourceCurrent || job.CanImport {
		t.Fatal(job)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/import", teacher, learningImportFixture(job, q, "stale-import")); r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
	if current := mustLibraryQuestion(t, s, q.ID); current.Version != 3 || !strings.Contains(current.ClinicalStem, "第三版") {
		t.Fatal(current)
	}
	if client.count() != 1 {
		t.Fatal(client.count())
	}
}

func TestLearningPermissionRevokedBeforeClaimMakesNoPaidCall(t *testing.T) {
	s, h, teacher, v, client, cleanup := learningFlowEnv(t)
	defer cleanup()
	q := modifiedLearningSource(t, s, h, teacher, v, "learning-revoked")
	release, err := s.learningSvc.Limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	initial := submitLearningFixture(t, h, teacher, q, "revoked-analysis")
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	permissions := []string{}
	for _, p := range owner.DirectPermissions {
		if p != domain.PermLearningAnalyze {
			permissions = append(permissions, p)
		}
	}
	if _, err := s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, permissions, nil, nil); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	job := waitLearningJob(t, h, teacher, initial.ID)
	if job.Status != "failed" || job.ErrorKind != "permission_revoked" || client.count() != 0 {
		t.Fatal(job, client.count())
	}
}

func TestLearningSuccessfulRetryKeepsSafeFailureClassification(t *testing.T) {
	for _, kind := range []string{"protocol_error", "attempt_timeout"} {
		t.Run(kind, func(t *testing.T) {
			s, h, teacher, v, client, cleanup := learningFlowEnv(t)
			defer cleanup()
			client.analysisBadFirst = kind == "protocol_error"
			client.analysisTimeoutFirst = kind == "attempt_timeout"
			q := modifiedLearningSource(t, s, h, teacher, v, "learning-attempt-history")
			initial := submitLearningFixture(t, h, teacher, q, "history-analysis")
			job := waitLearningJob(t, h, teacher, initial.ID)
			if job.Status != "succeeded" || job.Attempts != 2 || client.count() != 2 || len(job.AttemptHistory) != 2 {
				t.Fatal(job, client.count())
			}
			if job.AttemptHistory[0].ErrorKind != kind || job.AttemptHistory[1].Status != "succeeded" {
				t.Fatal(job.AttemptHistory)
			}
		})
	}
}
