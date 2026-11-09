//go:build integration

package postgres

import (
	"slices"
	"testing"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/llm"
	"github.com/lib/pq"
)

func TestLearningMigrationOnlyGrantsUntouchedDefaultsOnce(t *testing.T) {
	teacher := []string{"question:view", "question:edit", "question:confirm", "question:generate", "rule:view", "rule:manage", "rule:use", "batch:run", "question:view_formal", "question:view_eliminated", "question:download", "question:share", "review:submit", "stats:view"}
	for _, tc := range []struct {
		name, id                       string
		permissions                    []string
		fresh, modified, builtin, want bool
	}{
		{name: "standard_before_c4", id: "teacher", permissions: teacher, builtin: true, want: true},
		{name: "new_untouched_c4", id: "teacher", permissions: teacher, fresh: true, builtin: true, want: true},
		{name: "edited_template_exact_set", id: "teacher", permissions: teacher, modified: true, builtin: true},
		{name: "customized_builtin", id: "teacher", permissions: []string{"question:view", "question:generate"}, builtin: true},
		{name: "custom_role", id: "custom-author", permissions: teacher},
		{name: "expert", id: "expert", permissions: []string{"question:view", "review:do", "review:view_results"}, builtin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dsn, cleanup := migrationTestSchema(t)
			defer cleanup()
			s, err := New(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := t.Context()
			applyMigrationPrefix(t, ctx, s, configuredMigrations(baselineSchemaSQL)[:60])
			if _, err = s.db.ExecContext(ctx, `INSERT INTO roles(id,name,permissions,is_builtin)VALUES($1,$1,$2,$3)`, tc.id, pq.Array(tc.permissions), tc.builtin); err != nil {
				t.Fatal(err)
			}
			if !tc.fresh {
				sql := `UPDATE roles SET created_at=NOW()-INTERVAL '3 days',updated_at=(SELECT applied_at-INTERVAL '1 day' FROM schema_migrations WHERE version=60) WHERE id=$1`
				if tc.modified {
					sql = `UPDATE roles SET created_at=NOW()-INTERVAL '3 days',updated_at=NOW() WHERE id=$1`
				}
				if _, err = s.db.ExecContext(ctx, sql, tc.id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			var grants []string
			if err = s.db.QueryRowContext(ctx, `SELECT permissions FROM roles WHERE id=$1`, tc.id).Scan(pq.Array(&grants)); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(grants, domain.PermLearningAnalyze) != tc.want {
				t.Fatal("unsafe default migration", grants)
			}
			var receipts int
			if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM role_capability_migrations WHERE role_id=$1 AND migration_key='c5-learning-v1'`, tc.id).Scan(&receipts); err != nil || receipts != boolInt(tc.want) {
				t.Fatal(receipts, err)
			}
			if _, err = s.db.ExecContext(ctx, `UPDATE roles SET permissions=array_remove(array_remove(array_remove(permissions,'learning:analyze'),'learning:save'),'learning:use'),updated_at=NOW() WHERE id=$1`, tc.id); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			service := auth.NewService(s.db, "learning-migration", time.Hour)
			if err = service.InitBuiltinRoles(ctx); err != nil {
				t.Fatal(err)
			}
			role, err := service.GetRole(ctx, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			for _, permission := range []string{domain.PermLearningAnalyze, domain.PermLearningSave, domain.PermLearningUse} {
				if slices.Contains(role.Permissions, permission) {
					t.Fatal("revoked learning permission returned after restart", role)
				}
			}
		})
	}
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func TestLearningMigrationAllowsSeparateModelCallPurpose(t *testing.T) {
	s := libraryTestStore(t)
	if err := s.SaveModelCall(t.Context(), llm.ModelCallRecord{ID: "learning-call", Stage: "model_call", Operation: "learning-job", Purpose: "learning_analysis", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var purpose string
	if err := s.db.QueryRowContext(t.Context(), `SELECT purpose FROM model_call_records WHERE id='learning-call'`).Scan(&purpose); err != nil || purpose != "learning_analysis" {
		t.Fatal(purpose, err)
	}
}
