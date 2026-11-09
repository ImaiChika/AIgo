//go:build integration

package postgres

import (
	"aigo/internal/learning"
	"testing"
)

func TestLearningComparisonRejectsMissingSyntheticOrUnprovenOriginal(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"missing_v1", `DELETE FROM question_versions WHERE question_id=$1 AND version=1`},
		{"synthetic_v1", `UPDATE question_versions SET change_type='backfill',actor='migration' WHERE question_id=$1 AND version=1`},
		{"legacy_zero_result", `UPDATE ai_review_results SET question_version=0 WHERE question_id=$1`},
		{"missing_pass", `DELETE FROM ai_review_results WHERE question_id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, job := learningStoreFixture(t)
			if _, err := store.db.ExecContext(t.Context(), tc.mutation, job.QuestionID); err != nil {
				t.Fatal(err)
			}
			comparison, err := learning.NewService(store, nil).Compare(t.Context(), "owner", "learner", job.QuestionID)
			if err != nil || comparison.CanAnalyze || comparison.BlockedReason == "" {
				t.Fatal("unproven original became a learning source", comparison, err)
			}
			if tc.name == "legacy_zero_result" {
				passed, err := store.HasInitialCheckPass(t.Context(), job.QuestionID)
				if err != nil || !passed {
					t.Fatal("C5 restriction changed C3 legacy evidence behavior", passed, err)
				}
			}
		})
	}
}
