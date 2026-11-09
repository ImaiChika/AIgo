//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"testing"
)

func TestSamplingMigrationPreservesExistingCheckTemperature(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, e := New(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:68])
	if _, e = s.db.Exec(`INSERT INTO questions(id,clinical_stem,options,answer,status,version) VALUES('old-check','旧题','[]','A','ai_draft',1)`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`INSERT INTO ai_check_tasks(id,question_id,question_version,status,max_attempts) VALUES('old-task','old-check',1,'pending',3)`); e != nil {
		t.Fatal(e)
	}
	applied, e := s.Migrate(t.Context())
	if e != nil || len(applied) != 1 || applied[0].Version != 69 {
		t.Fatal(applied, e)
	}
	c, e := s.GetModelSamplingConfig(t.Context())
	if e != nil || c.GenerationTemperature != 0.4 || c.CheckTemperature != 0.2 {
		t.Fatal(c, e)
	}
	c.CheckTemperature = 0.6
	if e = s.SaveModelSamplingConfig(t.Context(), c); e != nil {
		t.Fatal(e)
	}
	old, e := s.LatestCheckTasksByQuestionIDs(t.Context(), []string{"old-check"})
	if e != nil || old["old-check"].SamplingTemperature == nil || *old["old-check"].SamplingTemperature != domain.DefaultCheckTemperature {
		t.Fatal(old, e)
	}
}
