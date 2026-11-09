package postgres

import "testing"

// These legacy-upgrade fixtures deliberately rewind the ledger. Remove the
// post-C4 structures as well so the source schema is real, rather than making
// production CREATE statements silently accept an inconsistent ledger.
func removeLearningSchemaForLegacyFixture(t *testing.T, s *Store) {
	t.Helper()
	// Reconstruct the actual pre-unification physical schema before rewinding
	// its ledger. Production migrations must not tolerate a fictitious hybrid.
	if _, err := s.db.ExecContext(t.Context(), `DO $$ BEGIN
IF EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='batch_jobs' AND column_name='prompt_snapshot') THEN
ALTER TABLE batch_jobs RENAME COLUMN prompt_snapshot TO rules_snapshot;
END IF; END $$`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range generationRuleMigrationStatements()[:2] {
		if _, err := s.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), `DROP INDEX knowledge_card_submission_source;
DROP TABLE knowledge_submissions,knowledge_collection_operations,knowledge_card_choices,personal_knowledge_versions,personal_knowledge_cards;
DROP TABLE question_experience_contexts,learning_import_operations,personal_experience_versions,personal_experiences,learning_analysis_jobs,role_capability_migrations;
ALTER TABLE batch_jobs DROP COLUMN experience_snapshot;
ALTER TABLE model_call_records DROP CONSTRAINT model_call_records_purpose_check;
ALTER TABLE model_call_records ADD CONSTRAINT model_call_records_purpose_check CHECK(purpose IN ('generation','ai_check'))`); err != nil {
		t.Fatal(err)
	}
}
