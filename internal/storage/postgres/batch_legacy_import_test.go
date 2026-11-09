//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

func legacyImportQuestion(id string) domain.A2Question {
	return domain.A2Question{ID: id, OwnerID: "legacy-owner", CreatedBy: "legacy-author", ClinicalStem: "患者，男，45岁。反复上腹痛2年，该患者最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}}, Answer: "A", Version: 1, Status: domain.StatusAIDraft}
}
func legacyImportJob(t *testing.T, s *Store, id string) storage.BatchJobRecord {
	t.Helper()
	questions := []domain.A2Question{legacyImportQuestion(id + "-a"), legacyImportQuestion(id + "-b")}
	raw, _ := json.Marshal(map[string]any{"questions": questions, "items": []any{map[string]any{"status": "ok", "question_id": questions[0].ID, "count": 1}, map[string]any{"status": "ok", "question_id": questions[1].ID, "count": 1}}})
	j := quotaTestBatch(id, "legacy-owner", "completed", 2, 2, 0)
	j.Backend = "local_single_api"
	j.OutputJSON = string(raw)
	if err := s.SaveBatchJob(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	return j
}
func legacyImporter(t *testing.T, s *Store) *batch.LocalExecutor {
	t.Helper()
	ex := batch.NewLocalExecutorWithConfig(nil, s, s, "legacy", batch.LocalConfig{Context: t.Context(), RecoveryInterval: time.Hour})
	t.Cleanup(ex.Close)
	return ex
}
func TestLegacyBatchImportRecoversExactStaleClaim(t *testing.T) {
	s, ctx := newAICheckOutcomeTestStore(t)
	job := legacyImportJob(t, s, "legacy-crash")
	claimed, err := s.ClaimBatchJobImport(ctx, job.ID)
	if err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	q := legacyImportQuestion(job.ID + "-a")
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	ex := legacyImporter(t, s)
	result, err := ex.ImportResults(storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: "legacy-author", OwnerID: "legacy-owner"}), job.ID, nil)
	if err != nil || result.Saved != 2 {
		t.Fatalf("stale exact import claim never recovers: result=%+v err=%v", result, err)
	}
	var checks int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_check_tasks`).Scan(&checks); err != nil || checks != 2 {
		t.Fatalf("checks=%d err=%v", checks, err)
	}
	_, usage, err := s.GetGenerationQuota(ctx)
	if err != nil || usage.BatchImportReservedQuestions != 0 || usage.PendingQuestionUnits != 2 {
		t.Fatal(usage, err)
	}
	replay, err := ex.ImportResults(ctx, job.ID, nil)
	if err != nil || replay.Saved != 2 {
		t.Fatal(replay, err)
	}
	var after int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_check_tasks`).Scan(&after)
	if after != checks {
		t.Fatal("replayed import queued a second logical check")
	}
}
func TestLegacyBatchImportRollsBackBeforeAnyFirstCheck(t *testing.T) {
	s, ctx := newAICheckOutcomeTestStore(t)
	job := legacyImportJob(t, s, "legacy-atomic")
	if _, err := s.db.ExecContext(ctx, `CREATE FUNCTION fail_legacy_second_question() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='legacy-atomic-b' THEN RAISE EXCEPTION 'injected legacy import save failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_legacy_second BEFORE INSERT ON questions FOR EACH ROW EXECUTE FUNCTION fail_legacy_second_question()`); err != nil {
		t.Fatal(err)
	}
	ex := legacyImporter(t, s)
	change := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: "legacy-author", OwnerID: "legacy-owner"})
	result, err := ex.ImportResults(change, job.ID, nil)
	if err == nil {
		t.Fatalf("partial import committed despite second write failure: %+v", result)
	}
	var questions, checks int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM questions`).Scan(&questions)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_check_tasks`).Scan(&checks)
	record, _ := s.GetBatchJob(ctx, job.ID)
	if questions != 0 || checks != 0 || record.ImportedAt != "" || record.ImportResult != "" {
		t.Fatalf("partial import/claim/check persisted: q=%d checks=%d job=%+v", questions, checks, record)
	}
	if _, err := s.db.ExecContext(context.Background(), `DROP TRIGGER fail_legacy_second ON questions`); err != nil {
		t.Fatal(err)
	}
	result, err = ex.ImportResults(change, job.ID, nil)
	if err != nil || result.Saved != 2 {
		t.Fatal(result, err)
	}
}
