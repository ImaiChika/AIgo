//go:build integration

package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage/postgres"
	"github.com/lib/pq"
)

type restoredExperienceClient struct{ calls atomic.Int64 }

func (c *restoredExperienceClient) Complete(ctx context.Context, messages []llm.Message, options llm.GenerateOptions) (string, error) {
	c.calls.Add(1)
	return (&fakeLLM{}).Complete(ctx, messages, options)
}

func generationExperiencePG(t *testing.T) (*postgres.Store, string) {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL DSN required")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aigo_c6_genexp_%d", time.Now().UnixNano())
	if _, err = admin.ExecContext(t.Context(), `CREATE SCHEMA `+pq.QuoteIdentifier(schema)); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA `+pq.QuoteIdentifier(schema)+` CASCADE`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	store, err := postgres.New(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	baseline, err := os.ReadFile("../storage/postgres/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.InitSchema(string(baseline)); err != nil {
		t.Fatal(err)
	}
	return store, parsed.String()
}

func TestRestoredGenerationExperienceRejectsInvalidSnapshotBeforeModel(t *testing.T) {
	for _, tc := range []struct {
		name, runOwner, specOwner, snapshotOwner, version string
		mutate                                            func(*domain.PersonalExperienceSnapshot)
	}{
		{"wrong_snapshot_owner", "owner-1", "owner-1", "other-owner", domain.ExperienceProtocolVersion, nil},
		{"missing_snapshot_owner", "owner-1", "owner-1", "", domain.ExperienceProtocolVersion, nil},
		{"unknown_protocol", "owner-1", "owner-1", "owner-1", "personal-experience-unknown", nil},
		{"snapshot_bound_to_wrong_spec_owner", "owner-1", "other-owner", "other-owner", domain.ExperienceProtocolVersion, nil},
		{"missing_spec_owner", "owner-1", "", "owner-1", domain.ExperienceProtocolVersion, nil},
		{"missing_run_owner", "", "owner-1", "owner-1", domain.ExperienceProtocolVersion, nil},
		{"too_many_entries", "owner-1", "owner-1", "owner-1", domain.ExperienceProtocolVersion, func(snapshot *domain.PersonalExperienceSnapshot) {
			snapshot.Entries = append(snapshot.Entries, snapshot.Entries[0], snapshot.Entries[0], snapshot.Entries[0], snapshot.Entries[0], snapshot.Entries[0])
		}},
		{"oversized_rules", "owner-1", "owner-1", "owner-1", domain.ExperienceProtocolVersion, func(snapshot *domain.PersonalExperienceSnapshot) {
			snapshot.Entries[0].Rules[0].Text = strings.Repeat("字", 3001)
		}},
		{"oversized_examples", "owner-1", "owner-1", "owner-1", domain.ExperienceProtocolVersion, func(snapshot *domain.PersonalExperienceSnapshot) {
			snapshot.Entries[0].Rules[0].Before = strings.Repeat("例", 3001)
		}},
		{"aggregate_over_budget", "owner-1", "owner-1", "owner-1", domain.ExperienceProtocolVersion, func(snapshot *domain.PersonalExperienceSnapshot) {
			entry := snapshot.Entries[0]
			entry.Rules = []domain.LearningRule{{Text: strings.Repeat("字", 1600)}}
			snapshot.Entries = []domain.PersonalExperienceUse{entry, entry}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, dsn := generationExperiencePG(t)
			spec := restoredExperienceSpec(tc.specOwner)
			spec.Request.PersonalExperiences.OwnerID = tc.snapshotOwner
			spec.Request.PersonalExperiences.Version = tc.version
			if tc.mutate != nil {
				tc.mutate(spec.Request.PersonalExperiences)
			}
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			runID := "c6-restored-" + tc.name
			if created, err := store.CreateGenerationRun(t.Context(), domain.GenerationRun{ID: runID, OwnerID: tc.runOwner, Status: domain.GenerationRunPending, RequestedCount: 1, MaxAttempts: 3, RequestJSON: raw}); err != nil || !created {
				t.Fatal(created, err)
			}
			// Reopen after the pending snapshot is durable, as a fresh server would.
			store.Close()
			restored, err := postgres.New(dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.Close() })
			run, request, err := restored.ClaimNextGenerationRun(t.Context(), time.Minute)
			if err != nil || run == nil {
				t.Fatal(run, err)
			}
			var frozen GenerationSpec
			if err = json.Unmarshal(request, &frozen); err != nil {
				t.Fatal(err)
			}
			client := &restoredExperienceClient{}
			pipe := New(generator.NewService(client), nil, nil, nil, restored)
			pipe.SetDraftChecker(&recordingChecker{})
			pipe.executeGenerationRun(t.Context(), run, &frozen)
			finished, err := restored.GetGenerationRun(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			if client.calls.Load() != 0 {
				t.Errorf("invalid frozen owner/version reached model: calls=%d", client.calls.Load())
			}
			if finished.Status != domain.GenerationRunFailed || finished.Attempts != 1 || !strings.Contains(finished.Error, "个人经验冻结快照无效") {
				t.Errorf("immutable invalid snapshot must fail once, not retry: status=%s attempts=%d error=%q", finished.Status, finished.Attempts, finished.Error)
			}
			for _, table := range []string{"questions", "ai_check_tasks", "question_experience_contexts"} {
				var count int
				if err := restored.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
					t.Errorf("invalid snapshot left %s rows=%d error=%v", table, count, err)
				}
			}
			next, _, err := restored.ClaimNextGenerationRun(t.Context(), time.Minute)
			if err != nil || next != nil {
				t.Errorf("failed immutable snapshot was re-claimed: %+v %v", next, err)
			}
		})
	}
}

func restoredExperienceSpec(owner string) GenerationSpec {
	point := domain.KnowledgePoint{ID: "110.4.3.1.1", Category: "临床综合", Subject: "消化", Topic: "消化性溃疡", OutlineCode: "110.4.3.1.1"}
	return GenerationSpec{Actor: "tester", OwnerID: owner, Request: domain.GenerationRequest{Subject: "消化", Count: 1, KnowledgePoints: []domain.KnowledgePoint{point}, PersonalExperiences: &domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: owner, Entries: []domain.PersonalExperienceUse{{ID: "private-frozen", Revision: 1, MatchedPointIDs: []string{point.ID}, Rules: []domain.LearningRule{{ID: "r1", Text: "保留冻结的病例表达要求"}}}}}}}
}

func TestRestoredGenerationKeepsValidAndLegacyExperienceSemantics(t *testing.T) {
	for _, tc := range []struct {
		name        string
		experiences bool
		specOwner   string
	}{{"valid_private_snapshot", true, "owner-1"}, {"legacy_without_experience", false, "owner-1"}, {"legacy_without_spec_owner", false, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			store, dsn := generationExperiencePG(t)
			spec := restoredExperienceSpec(tc.specOwner)
			if !tc.experiences {
				spec.Request.PersonalExperiences = nil
			}
			raw, _ := json.Marshal(spec)
			if created, err := store.CreateGenerationRun(t.Context(), domain.GenerationRun{ID: "c6-valid-" + tc.name, OwnerID: "owner-1", Status: domain.GenerationRunPending, RequestedCount: 1, MaxAttempts: 3, RequestJSON: raw}); err != nil || !created {
				t.Fatal(created, err)
			}
			store.Close()
			restored, err := postgres.New(dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.Close() })
			run, request, err := restored.ClaimNextGenerationRun(t.Context(), time.Minute)
			if err != nil || run == nil {
				t.Fatal(run, err)
			}
			var frozen GenerationSpec
			if err = json.Unmarshal(request, &frozen); err != nil {
				t.Fatal(err)
			}
			client := &restoredExperienceClient{}
			pipe := New(generator.NewService(client), nil, nil, nil, restored)
			pipe.SetDraftChecker(&recordingChecker{})
			pipe.executeGenerationRun(t.Context(), run, &frozen)
			finished, err := restored.GetGenerationRun(t.Context(), run.ID)
			if err != nil || finished.Status != domain.GenerationRunSucceeded || len(finished.QuestionIDs) != 1 || client.calls.Load() != 1 {
				t.Fatal(finished, err, client.calls.Load())
			}
			var checks int
			if err = restored.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ai_check_tasks`).Scan(&checks); err != nil || checks != 1 {
				t.Fatal("restored generation must hand off exactly once", checks, err)
			}
			evidence, err := restored.GetQuestionPersonalExperiences(t.Context(), finished.QuestionIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			if tc.experiences && (evidence == nil || evidence.OwnerID != "owner-1" || evidence.Entries[0].Revision != 1) {
				t.Fatal("valid frozen adoption was lost", evidence)
			}
			if !tc.experiences && evidence != nil {
				t.Fatal("legacy generation unexpectedly adopted live personal cards", evidence)
			}
		})
	}
}
