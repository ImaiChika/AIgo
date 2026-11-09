package batch

import (
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLocalBatchPersonalExperienceSurvivesSubmissionRestartAndRetry(t *testing.T) {
	point := domain.KnowledgePoint{ID: "experience-point", OutlineCode: "110.1", Category: "临床综合", Subject: "内科", Topic: "经验考点"}
	snapshot := &domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: "owner", Entries: []domain.PersonalExperienceUse{{ID: "private-experience", Revision: 1, Title: "表达", MatchedPointIDs: []string{point.ID}, Rules: []domain.LearningRule{{ID: "private-rule", Text: "冻结版本一表达经验"}}}}}
	for _, mode := range []string{"submit", "restart", "retry"} {
		t.Run(mode, func(t *testing.T) {
			client := &promptCaptureClient{}
			jobs := &localBatchStore{}
			questions := testutil.NewMemoryStore()
			raw, _ := json.Marshal(snapshot)
			points, _ := json.Marshal([]domain.KnowledgePoint{point})
			if mode != "submit" {
				status, output := "in_progress", `{"questions":[],"items":[]}`
				if mode == "retry" {
					status = "completed"
					output = `{"questions":[],"items":[{"status":"failed","error":"interrupted"}]}`
				}
				jobs.job = storage.BatchJobRecord{ID: "experience-job", OwnerID: "owner", Backend: localSingleAPIBackend, Status: status, TotalCount: 1, PointsJSON: string(points), OutputJSON: output, ExperienceSnapshotJSON: string(raw)}
			}
			executor := NewLocalExecutorWithConfig(generator.NewService(client), questions, jobs, "test", LocalConfig{RecoveryInterval: 10 * time.Millisecond})
			defer executor.Close()
			id := "experience-job"
			ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{OwnerID: "owner"})
			if mode == "submit" {
				var err error
				id, _, err = executor.GenerateAndSubmit(generator.WithPersonalExperiences(ctx, snapshot), []domain.KnowledgePoint{point}, 1, "个人经验")
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "retry" {
				if _, err := executor.RetryFailed(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				job, _ := jobs.GetBatchJob(ctx, id)
				if job.Status == "completed" && job.Completed == 1 {
					if job.ExperienceSnapshotJSON == "" {
						t.Fatal("experience snapshot was not persisted")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("batch did not recover", job)
				}
				time.Sleep(5 * time.Millisecond)
			}
			client.mu.Lock()
			messages := append(client.messages[:0:0], client.messages...)
			client.mu.Unlock()
			if len(messages) != 1 || !strings.Contains(messages[0][1].Content, "冻结版本一表达经验") {
				t.Fatal("recovery lost frozen experience", messages)
			}
			public, _ := executor.GetJobStatus(ctx, id)
			body, _ := json.Marshal(public)
			if strings.Contains(string(body), "冻结版本一表达经验") || strings.Contains(string(body), "private-experience") {
				t.Fatal("public batch status leaked private rules")
			}
		})
	}
}

func TestCorruptBatchPersonalExperienceSnapshotStopsBeforeModel(t *testing.T) {
	point := domain.KnowledgePoint{ID: "corrupt-experience-point", OutlineCode: "110.1", Category: "临床综合", Subject: "内科", Topic: "经验考点"}
	for _, tc := range []struct {
		name    string
		entries []domain.PersonalExperienceUse
	}{
		{"too_many", make([]domain.PersonalExperienceUse, 6)},
		{"oversized", []domain.PersonalExperienceUse{{MatchedPointIDs: []string{point.ID}, Rules: []domain.LearningRule{{Text: strings.Repeat("字", 3001)}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := &domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: "owner", Entries: tc.entries}
			raw, _ := json.Marshal(snapshot)
			points, _ := json.Marshal([]domain.KnowledgePoint{point})
			jobs := &localBatchStore{job: storage.BatchJobRecord{ID: "bad-snapshot-" + tc.name, OwnerID: "owner", Backend: localSingleAPIBackend, Status: "in_progress", TotalCount: 1, PointsJSON: string(points), OutputJSON: `{"questions":[],"items":[]}`, ExperienceSnapshotJSON: string(raw)}}
			id := jobs.job.ID
			client := &promptCaptureClient{}
			executor := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), jobs, "test", LocalConfig{RecoveryInterval: 10 * time.Millisecond})
			defer executor.Close()
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				record, _ := jobs.GetBatchJob(t.Context(), id)
				if record.Status == "failed" {
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("invalid snapshot did not fail terminally", record)
				case <-ticker.C:
				}
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			if len(client.messages) != 0 {
				t.Fatalf("invalid immutable snapshot used model %d times", len(client.messages))
			}
		})
	}
}
