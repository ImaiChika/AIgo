package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestConfiguredMigrationsAreOrderedAndChecksummed(t *testing.T) {
	migrations := configuredMigrations(baselineSchemaSQL)
	if err := validateMigrationDefinitions(migrations); err != nil {
		t.Fatal(err)
	}
	if got := migrations[len(migrations)-1].Version; got != LatestSchemaVersion {
		t.Fatalf("latest migration=%d, declared=%d", got, LatestSchemaVersion)
	}
	seenChecksums := make(map[string]bool)
	for _, migration := range migrations {
		info := migrationInfo(migration)
		if len(info.Checksum) != 64 {
			t.Fatalf("migration %d checksum length=%d", migration.Version, len(info.Checksum))
		}
		if seenChecksums[info.Checksum] {
			t.Fatalf("migration %d reuses a checksum", migration.Version)
		}
		seenChecksums[info.Checksum] = true
	}
}

func TestRecentMigrationsArePresent(t *testing.T) {
	migrations := configuredMigrations(baselineSchemaSQL)
	var snapshot, performance, search, removeImages, superAdmin, personalQuestions, batchOwnership, legacyGlobal migration
	for _, candidate := range migrations {
		switch candidate.Version {
		case 12:
			snapshot = candidate
		case 13:
			performance = candidate
		case 14:
			search = candidate
		case 15:
			removeImages = candidate
		case 16:
			superAdmin = candidate
		case 17:
			personalQuestions = candidate
		case 18:
			batchOwnership = candidate
		case 19:
			legacyGlobal = candidate
		}
	}
	snapshotSQL := strings.Join(snapshot.Statements, "\n")
	if !strings.Contains(snapshotSQL, "submission_bank_id") ||
		!strings.Contains(snapshotSQL, "idx_review_tasks_submission_bank") {
		t.Fatalf("migration 12 does not persist review submission bank snapshot: %s", snapshotSQL)
	}
	if !strings.Contains(strings.Join(performance.Statements, "\n"), "idx_review_tasks_question_created") {
		t.Fatal("migration 13 does not add the review-result latest-task index")
	}
	searchSQL := strings.Join(search.Statements, "\n")
	if !strings.Contains(searchSQL, "search_text") || !strings.Contains(searchSQL, "idx_questions_search_text_trgm") {
		t.Fatal("migration 14 does not add unified question search text and index")
	}
	removeImagesSQL := strings.Join(removeImages.Statements, "\n")
	for _, fragment := range []string{"snapshot - 'media_refs'", "DROP TABLE IF EXISTS generated_images", "DROP TABLE IF EXISTS image_prompts", "DROP COLUMN IF EXISTS media_refs", "array_remove"} {
		if !strings.Contains(removeImagesSQL, fragment) {
			t.Fatalf("migration 15 does not fully remove question image data: missing %q", fragment)
		}
	}
	superAdminSQL := strings.Join(superAdmin.Statements, "\n")
	for _, fragment := range []string{"username='admin'", "idx_users_single_super_admin", "role='super_admin'"} {
		if !strings.Contains(superAdminSQL, fragment) {
			t.Fatalf("migration 16 does not enforce a single super admin: missing %q", fragment)
		}
	}
	personalQuestionsSQL := strings.Join(personalQuestions.Statements, "\n")
	for _, fragment := range []string{"owner_id", "question_share_requests", "status IN ('pending','approved','rejected')", "legacy-share-"} {
		if !strings.Contains(personalQuestionsSQL, fragment) {
			t.Fatalf("migration 17 does not add personal/global question isolation: missing %q", fragment)
		}
	}
	batchOwnershipSQL := strings.Join(batchOwnership.Statements, "\n")
	if !strings.Contains(batchOwnershipSQL, "batch_jobs") || !strings.Contains(batchOwnershipSQL, "owner_id") {
		t.Fatalf("migration 18 does not isolate batch tasks by owner: %s", batchOwnershipSQL)
	}
	legacyGlobalSQL := strings.Join(legacyGlobal.Statements, "\n")
	if !strings.Contains(legacyGlobalSQL, "legacy-share-") || !strings.Contains(legacyGlobalSQL, "owner_id=''") || !strings.Contains(legacyGlobalSQL, "question_share_requests") {
		t.Fatalf("migration 19 does not restore legacy global question tiers: %s", legacyGlobalSQL)
	}
}

func TestVersionedMigrationsFreshConcurrentAndIdempotent(t *testing.T) {
	adminDB, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	_ = adminDB

	store1, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()
	store2, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := make(chan []MigrationInfo, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{store1, store2} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			applied, err := s.Migrate(ctx)
			results <- applied
			errs <- err
		}(store)
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migration failed: %v", err)
		}
	}
	var appliedCount int
	for applied := range results {
		appliedCount += len(applied)
	}
	if appliedCount != int(LatestSchemaVersion) {
		t.Fatalf("concurrent migration applications=%d, want %d total", appliedCount, LatestSchemaVersion)
	}
	if err := store1.CheckSchemaVersion(ctx); err != nil {
		t.Fatalf("fresh migrated database should be current: %v", err)
	}
	if err := store1.CheckReadiness(ctx); err != nil {
		t.Fatalf("fresh migrated database should be ready: %v", err)
	}
	for _, relation := range []string{"image_prompts", "generated_images", "image_review_records"} {
		var table sql.NullString
		if err := store1.db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&table); err != nil {
			t.Fatal(err)
		}
		if table.Valid {
			t.Fatalf("removed image table %s still exists", relation)
		}
	}
	var mediaColumnCount int
	if err := store1.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='questions' AND column_name='media_refs'`).Scan(&mediaColumnCount); err != nil {
		t.Fatal(err)
	}
	if mediaColumnCount != 0 {
		t.Fatal("questions.media_refs still exists after migration 15")
	}

	again, err := store1.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("idempotent migrate applied %d versions, want 0", len(again))
	}
	var recorded int
	if err := store1.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != int(LatestSchemaVersion) {
		t.Fatalf("recorded migrations=%d, want %d", recorded, LatestSchemaVersion)
	}
}

func TestRemoveQuestionImagesMigrationPreservesQuestion(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	store, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	migrations := configuredMigrations(baselineSchemaSQL)
	applyMigrationPrefix(t, ctx, store, migrations[:14])

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO questions (id, clinical_stem, options, answer, media_refs, search_text, status, version)
		VALUES ('q-with-image', '应保留的题干', '[{"label":"A","text":"甲"},{"label":"B","text":"乙"},{"label":"C","text":"丙"},{"label":"D","text":"丁"}]', 'A', '[{"id":"media-1","kind":"image","uri":"output/images/legacy.png","description":"legacy-image-marker"}]', 'legacy-image-marker', 'ai_reviewed', 1);
		INSERT INTO question_versions (id, question_id, version, snapshot)
		VALUES ('qv-with-image', 'q-with-image', 1, '{"id":"q-with-image","clinical_stem":"应保留的题干","media_refs":[{"id":"media-1"}]}');
		INSERT INTO image_prompts (id, question_id) VALUES ('prompt-1', 'q-with-image');
		INSERT INTO generated_images (id, prompt_id, question_id, image_path) VALUES ('image-1', 'prompt-1', 'q-with-image', 'output/images/legacy.png');
		INSERT INTO image_review_records (id, image_id, expert_id, conclusion) VALUES ('review-1', 'image-1', 'expert-1', 'approved');
		INSERT INTO roles (id, name, permissions) VALUES ('legacy-image-role', '旧图片角色', ARRAY['image:generate','question:view','image:review']);
		INSERT INTO users (id, username, password_hash, role, permissions) VALUES ('legacy-image-user', 'legacy-image-user', 'unused', '', ARRAY['image:generate','question:edit','image:review'])
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.migrate(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	var stem, searchText string
	if err := store.db.QueryRowContext(ctx, `SELECT clinical_stem, search_text FROM questions WHERE id='q-with-image'`).Scan(&stem, &searchText); err != nil {
		t.Fatalf("question was deleted by image cleanup: %v", err)
	}
	if stem != "应保留的题干" {
		t.Fatalf("question content changed: %q", stem)
	}
	if strings.Contains(searchText, "legacy-image-marker") {
		t.Fatalf("removed image metadata remains searchable: %q", searchText)
	}
	var snapshotHasMedia bool
	if err := store.db.QueryRowContext(ctx, `SELECT snapshot ? 'media_refs' FROM question_versions WHERE id='qv-with-image'`).Scan(&snapshotHasMedia); err != nil {
		t.Fatal(err)
	}
	if snapshotHasMedia {
		t.Fatal("question version snapshot still contains media_refs")
	}
	var rolePermissions, userPermissions []string
	if err := store.db.QueryRowContext(ctx, `SELECT permissions FROM roles WHERE id='legacy-image-role'`).Scan(pq.Array(&rolePermissions)); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT permissions FROM users WHERE id='legacy-image-user'`).Scan(pq.Array(&userPermissions)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rolePermissions, ","); got != "question:view" {
		t.Fatalf("legacy role image permissions were not removed: %v", rolePermissions)
	}
	if got := strings.Join(userPermissions, ","); got != "question:edit" {
		t.Fatalf("legacy user image permissions were not removed: %v", userPermissions)
	}
	for _, relation := range []string{"image_prompts", "generated_images", "image_review_records"} {
		var table sql.NullString
		if err := store.db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&table); err != nil {
			t.Fatal(err)
		}
		if table.Valid {
			t.Fatalf("removed image table %s still exists", relation)
		}
	}
}

func applyMigrationPrefix(t *testing.T, ctx context.Context, store *Store, migrations []migration) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, migrationTableSQL); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range migrations {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range candidate.Statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				t.Fatalf("apply migration %d: %v", candidate.Version, err)
			}
		}
		info := migrationInfo(candidate)
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1,$2,$3)`, info.Version, info.Name, info.Checksum); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationFailureRollsBackVersion(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	store, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	migrations := []migration{{Version: 1, Name: "rollback_base", Statements: []string{`CREATE TABLE rollback_base (id INT PRIMARY KEY)`}}}
	for version := int64(2); version < LatestSchemaVersion; version++ {
		migrations = append(migrations, migration{Version: version, Name: fmt.Sprintf("rollback_before_%d", version), Statements: []string{`SELECT 1`}})
	}
	migrations = append(migrations, migration{Version: LatestSchemaVersion, Name: "rollback_failure", Statements: []string{
		`CREATE TABLE rollback_probe (id INT PRIMARY KEY)`,
		`INSERT INTO table_that_does_not_exist (id) VALUES (1)`,
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	applied, err := store.migrate(ctx, migrations)
	if err == nil || !strings.Contains(err.Error(), "已回滚") {
		t.Fatalf("migration should fail with rollback message, applied=%v err=%v", applied, err)
	}
	var probe sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT to_regclass('rollback_probe')::text`).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe.Valid {
		t.Fatalf("failed migration left table %q behind", probe.String)
	}
	var versions []int64
	rows, err := store.db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if len(versions) != int(LatestSchemaVersion-1) {
		t.Fatalf("failed migration ledger=%v, want %d earlier versions", versions, LatestSchemaVersion-1)
	}
	for i, version := range versions {
		if version != int64(i+1) {
			t.Fatalf("unexpected ledger: %v", versions)
		}
	}
}

func TestSchemaVersionCheckRejectsPendingAndTamperedMigrations(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	store, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.CheckSchemaVersion(ctx); err == nil {
		t.Fatal("uninitialized database must fail schema version check")
	}
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE schema_migrations SET checksum='tampered' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchemaVersion(ctx); err == nil || !strings.Contains(err.Error(), "校验和") {
		t.Fatalf("tampered migration must be rejected, got %v", err)
	}
}

func migrationTestSchema(t *testing.T) (*sql.DB, string, func()) {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL migration integration tests")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := fmt.Sprintf("aigo_migration_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	testDSN, err := migrationDSNWithSearchPath(dsn, schemaName)
	if err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)
		_ = adminDB.Close()
	}
	return adminDB, testDSN, cleanup
}

func migrationDSNWithSearchPath(dsn, schemaName string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
