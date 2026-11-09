//go:build integration

package postgres

import (
	"testing"

	"aigo/internal/domain"
)

func TestReviewCompletionMigrationPreservesLegacyConflict(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	t.Cleanup(cleanup)
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	migrations := configuredMigrations(baselineSchemaSQL)
	applyMigrationPrefix(t, t.Context(), s, migrations[:59])
	if _, err := s.db.ExecContext(t.Context(), `
	 INSERT INTO questions(id,clinical_stem,options,answer,status,version,library_state,library_revision)
	 VALUES('legacy-mode-q','旧题','[]','A','reviewing',1,'unfiled',1);
	 INSERT INTO review_flows(id,name,rounds) VALUES('legacy-mode-f','旧一轮','[{"round_number":1,"expert_ids":["r1"],"required_count":1}]');
	 INSERT INTO review_tasks(id,question_id,flow_id,status,question_version,assigned_to,round_results)
	 VALUES('legacy-mode-t','legacy-mode-q','legacy-mode-f','conflict',1,ARRAY['r1'],'[{"round_number":1,"approved_count":1,"passed":true}]');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	flow, err := s.GetFlowConfig(t.Context(), "legacy-mode-f")
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.GetTask(t.Context(), "legacy-mode-t")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.GetQuestion(t.Context(), "legacy-mode-q")
	if err != nil {
		t.Fatal(err)
	}
	if flow.CompletionMode != domain.ReviewCompletionExternalFinal || task.CompletionMode != domain.ReviewCompletionExternalFinal || task.Status != domain.StatusConflict || task.LibraryBeforeSubmit != nil || q.Status != domain.StatusReviewing || q.LibraryState != domain.LibraryUnfiled || q.ConfirmedVersion != 0 {
		t.Fatalf("migration reinterpreted old progress: flow=%+v task=%+v q=%+v", flow, task, q)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetTask(t.Context(), task.ID)
	if again.Status != domain.StatusConflict {
		t.Fatal("idempotent startup auto-completed old task", again)
	}
}
