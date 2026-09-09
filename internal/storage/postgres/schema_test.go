package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestSchemaCreatesBankBeforeMembershipTable(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(schema)
	bankPos := strings.Index(text, "CREATE TABLE IF NOT EXISTS question_banks")
	membershipPos := strings.Index(text, "CREATE TABLE IF NOT EXISTS question_bank_members")
	if bankPos < 0 || membershipPos < 0 {
		t.Fatal("schema 缺少题库表或题目-题库成员表")
	}
	if bankPos > membershipPos {
		t.Fatal("question_banks 必须先于引用它的 question_bank_members 创建")
	}
}

func TestSchemaPersistsBatchBackendProfileAndModel(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(schema)
	for _, column := range []string{
		"backend TEXT NOT NULL DEFAULT 'dashscope'",
		"backend_profile TEXT NOT NULL DEFAULT 'dashscope-default'",
		"model TEXT NOT NULL DEFAULT ''",
	} {
		if !strings.Contains(text, column) {
			t.Errorf("batch_jobs schema missing %q", column)
		}
	}
}

func TestSchemaIncludesQuestionVersionsAndReviewBinding(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(schema)
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS question_versions",
		"UNIQUE (question_id, version)",
		"question_version INT NOT NULL DEFAULT 1",
		"idx_question_versions_question",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("version schema missing %q", fragment)
		}
	}
}

func TestSchemaRejectsLegacyApprovedLifecycleStatus(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(schema)
	for _, constraint := range []string{"questions_no_legacy_approved", "review_tasks_no_legacy_approved"} {
		if !strings.Contains(text, constraint) {
			t.Errorf("schema missing lifecycle constraint %s", constraint)
		}
	}
}

func TestSchemaIncludesVersionedMigrationLedger(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(schema)
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS schema_migrations",
		"version BIGINT PRIMARY KEY",
		"checksum TEXT NOT NULL",
		"applied_at TIMESTAMPTZ NOT NULL",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("migration ledger schema missing %q", fragment)
		}
	}
}
