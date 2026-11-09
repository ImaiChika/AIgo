//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/learning"
	"aigo/internal/storage"
)

func TestLearningDifferentAccountsParallelAndRetryYields(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%v", retry), func(t *testing.T) {
			s, h, _, version, cleanup := libraryTestEnv(t)
			defer cleanup()
			store := s.questionStore.(storage.LearningStore)
			gate := make(chan struct{})
			defer func() {
				select {
				case <-gate:
				default:
					close(gate)
				}
			}()
			client := &c5FlowClient{analysisGate: gate, analysisStarted: make(chan struct{}, 4), analysisBadFirst: retry}
			s.learningSvc = learning.NewService(store, client)
			s.learningSvc.PollInterval = 5 * time.Millisecond
			admin := loginForAuthTest(t, h, "admin", "admin-password", "198.18.3.1")
			tokens := []string{}
			sources := []*domain.A2Question{}
			for i := range 3 {
				name := fmt.Sprintf("parallel-learner-%d", i)
				r := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": "owned-learning-parallel-password", "permissions": []string{domain.PermQuestionView, domain.PermQuestionEdit, domain.PermQuestionGenerate, domain.PermLearningAnalyze, domain.PermLearningSave}})
				if r.Code != 201 {
					t.Fatal(r.Code, r.Body)
				}
				token := loginForAuthTest(t, h, name, "owned-learning-parallel-password", fmt.Sprintf("198.18.3.%d", i+2))
				tokens = append(tokens, token)
				u, err := s.authSvc.GetUserByUsername(name)
				if err != nil {
					t.Fatal(err)
				}
				point := domain.KnowledgePoint{ID: "lib.001", VersionID: version, Topic: "题库归属考点", OutlineCode: "lib.001", Category: "临床综合", Subject: "消化"}
				seedCustomerBrowserQuestion(t, s, u.ID, name, point, fmt.Sprintf("parallel-source-%d", i), "患者，男，45岁。仅并发测试。该患者最可能的诊断是")
				q := mustLibraryQuestion(t, s, fmt.Sprintf("parallel-source-%d", i))
				edit := learningRequest(q, "")
				delete(edit, "request_key")
				edit["explanation"] = "已保存的新解析表达"
				if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, token, edit); r.Code != 200 {
					t.Fatal(r.Code, r.Body)
				}
				sources = append(sources, mustLibraryQuestion(t, s, q.ID))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.learningSvc.StartWorkers(ctx, 2)
			defer func() {
				cancel()
				select {
				case <-gate:
				default:
					close(gate)
				}
				select {
				case <-s.learningSvc.WorkersDone():
				case <-time.After(3 * time.Second):
					t.Error("learning workers did not stop")
				}
			}()
			jobs := []domain.LearningJob{}
			for i := range 3 {
				jobs = append(jobs, submitLearningFixture(t, h, tokens[i], sources[i], fmt.Sprintf("parallel-%d", i)))
			}
			for range 2 {
				select {
				case <-client.analysisStarted:
				case <-time.After(3 * time.Second):
					t.Fatal("different accounts did not execute concurrently")
				}
			}
			// The occupied slots cannot be bypassed, and retry delay frees a slot.
			var running, pending int
			pg := s.questionStore.(interface{ DB() *sql.DB })
			if e := pg.DB().QueryRow(`SELECT count(*) FILTER(WHERE status='running'),count(*) FILTER(WHERE status='pending') FROM learning_analysis_jobs`).Scan(&running, &pending); e != nil {
				t.Fatal(e)
			}
			if running != 2 || pending != 1 {
				t.Fatal("global slots or retry yielding incorrect", running, pending)
			}
			r := serveGenerateJSON(t, h, "POST", "/api/questions/"+sources[0].ID+"/learning-analyses", tokens[0], learningRequest(sources[0], "another-inflight"))
			if r.Code != 429 || r.Header().Get("Retry-After") != "5" {
				t.Fatal("one inflight per account lost", r.Code, r.Body)
			}
			r = serveGenerateJSON(t, h, "POST", "/api/questions/"+sources[0].ID+"/learning-analyses", tokens[0], learningRequest(sources[0], "parallel-0"))
			var replay struct {
				Job domain.LearningJob `json:"job"`
			}
			if r.Code != 202 || json.Unmarshal(r.Body.Bytes(), &replay) != nil || replay.Job.ID != jobs[0].ID {
				t.Fatal("idempotent request duplicated", r.Code, r.Body)
			}
			if r := serveGenerateJSON(t, h, "GET", "/api/learning-analyses/"+jobs[0].ID, tokens[1], nil); r.Code != 404 {
				t.Fatal("foreign learning job exposed", r.Code)
			}
			close(gate)
			for i, job := range jobs {
				got := waitLearningJob(t, h, tokens[i], job.ID)
				if got.Status != "succeeded" || got.QuestionID != sources[i].ID || len(got.Candidates) != 3 {
					t.Fatal("parallel result or source mixed", got.Status, got.QuestionID)
				}
			}
			want := 3
			if retry {
				want = 4
			}
			if client.count() != want {
				t.Fatal("model request duplicated", client.count(), want)
			}
		})
	}
}
