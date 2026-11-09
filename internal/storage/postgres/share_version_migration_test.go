//go:build integration

package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestShareVersionMigrationRecoversOnlyProvenSubmittedContent(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	migrations := configuredMigrations(baselineSchemaSQL)
	applyMigrationPrefix(t, ctx, s, migrations[:59])
	submission := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	for _, status := range []string{"pending", "approved", "rejected", "unknown"} {
		q := domain.A2Question{ID: status, OwnerID: "owner", ClinicalStem: "已提交的原病例" + status, Answer: "A", Options: []domain.Option{{Label: "A", Text: "甲"}}, Version: 1, Status: domain.StatusPublished, LibraryState: domain.LibraryActive, ConfirmedVersion: 1, ConfirmationSource: domain.ConfirmSourceLegacy, LibraryRevision: 3, CreatedAt: submission.Add(-time.Hour), UpdatedAt: submission.Add(-time.Hour)}
		if err = s.SaveQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
		shareStatus := status
		if shareStatus == "unknown" {
			shareStatus = "rejected"
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO question_share_requests(id,question_id,owner_id,status,created_at) VALUES($1,$1,'owner',$2,$3)`, status, shareStatus, submission); err != nil {
			t.Fatal(err)
		}
		// The v1 content snapshot predates self/review confirmation and is draft;
		// migration must restore submitted content without using draft metadata.
		if _, err = s.db.ExecContext(ctx, `UPDATE question_versions SET created_at=$2,snapshot=snapshot||'{"status":"ai_draft","library_state":"unfiled","confirmed_version":0,"confirmation_source":""}'::jsonb WHERE question_id=$1`, status, submission.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if status == "rejected" || status == "unknown" {
			q.ClinicalStem = "后来的个人私密修改"
			q.Version = 2
			if err = s.SaveQuestion(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if status == "unknown" {
			if _, err = s.db.ExecContext(ctx, `DELETE FROM question_versions WHERE question_id=$1`, status); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pending", "approved", "rejected"} {
		q, request, err := s.GetQuestionShareSnapshot(ctx, id)
		if err != nil || request.QuestionVersion != 1 || q.ClinicalStem != "已提交的原病例"+id || q.ConfirmedVersion != 1 || q.LibraryState != domain.LibraryActive {
			t.Fatalf("%s recovered wrong version: %+v %+v %v", id, q, request, err)
		}
		raw, _ := json.Marshal(q)
		if string(raw) == "" || q.CreatedBy != "" || q.ConfirmedBy != "" || q.DeletedBy != "" {
			t.Fatal("migration leaked actors")
		}
	}
	if q, request, err := s.GetQuestionShareSnapshot(ctx, "unknown"); !errors.Is(err, storage.ErrQuestionShareSnapshotUnavailable) || q != nil || request.QuestionVersion != 0 {
		t.Fatalf("unproven history used newest private row: %+v %+v %v", q, request, err)
	}
	result, total, err := s.SearchQuestions(ctx, storage.QuestionFilter{GlobalOnly: true, GlobalStatuses: []string{"rejected"}}, 1, 100)
	if err != nil || total != 1 || len(result) != 1 || result[0].ID != "rejected" {
		t.Fatalf("unproven history entered global query: %+v %d %v", result, total, err)
	}
	if applied, err := s.Migrate(ctx); err != nil || len(applied) != 0 {
		t.Fatal("migration not idempotent", applied, err)
	}
}
