//go:build integration

package api

import (
	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage/postgres"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lib/pq"
)

// Explicit native-PG drill only; it cannot target the normal local DB or ECS.
func TestKnowledgeBackupRestoreRoundTrip(t *testing.T) {
	if os.Getenv("AIGO_RUN_LOCAL_RESTORE") != "1" {
		t.Skip("explicit isolated restore opt-in required")
	}
	u, err := url.Parse(os.Getenv("AIGO_TEST_POSTGRES_DSN"))
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != "15516" || u.Path != "/postgres" || u.User == nil || u.User.Username() != "p_complete_acceptance" {
		t.Fatal("restore drill requires this round's dedicated cluster")
	}
	bin := os.Getenv("AIGO_PG_BIN")
	if bin == "" {
		t.Fatal("explicit PG binary directory required")
	}
	root, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sourceName := fmt.Sprintf("aigo_p6_source_%d", time.Now().UnixNano())
	targetName := fmt.Sprintf("aigo_p6_restore_%d", time.Now().UnixNano())
	for _, name := range []string{sourceName, targetName} {
		if _, err = root.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier(name) + ` TEMPLATE template0`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, e := root.Exec(`DROP DATABASE ` + pq.QuoteIdentifier(name) + ` WITH (FORCE)`); e != nil {
				t.Error("owned restore DB cleanup failed", e)
			}
		}()
	}
	sourceURL := *u
	sourceURL.Path = "/" + sourceName
	t.Setenv("AIGO_TEST_POSTGRES_DSN", sourceURL.String())
	s, store, h, token, _, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	points := importUserExamFixture(t, s, h, token)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload("p6-complete", points[0], 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	completed := waitWorkflowQuestions(t, h, token, "p6-complete", 1)[0]
	r = serveGenerateJSON(t, h, "PUT", "/api/questions/"+completed.ID+"/preference-feedback", token, map[string]any{"question_version": completed.Version, "revision": 0, "rating": "not_met", "comment": "仅本地恢复验收的私有反馈"})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "restore-controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: s.aiCheckSvc, CheckMaxAttempts: 3})
	s.batchSvc = ex
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"knowledge_point_ids": []string{points[1].ID}, "version_id": points[1].VersionID, "count": 2, "workflow_mode": "knowledge"})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		g, c := client.counts()
		var committed int
		if err = store.DB().QueryRow(`SELECT count(*) FROM ai_check_tasks WHERE status='succeeded'`).Scan(&committed); err != nil {
			t.Fatal(err)
		}
		if g == 3 && c == 3 && committed == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g, c := client.counts(); g != 3 || c != 3 {
		t.Fatal("initial live workflow did not finish", g, c)
	}
	cancel()
	ex.Close()
	r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload("p6-pending", points[0], 2))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	pending, err := store.GetGenerationRun(t.Context(), "p6-pending")
	if err != nil || pending.Status != domain.GenerationRunPending {
		t.Fatal("drill pending task missing", err)
	}
	var schema string
	if err = store.DB().QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	fingerprint := func(db *sql.DB) map[string]string {
		t.Helper()
		rows, e := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname=$1 ORDER BY tablename`, schema)
		if e != nil {
			t.Fatal(e)
		}
		tables := []string{}
		for rows.Next() {
			var table string
			if e = rows.Scan(&table); e != nil {
				t.Fatal(e)
			}
			tables = append(tables, table)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		values := map[string]string{}
		for _, table := range tables {
			var value string
			q := `SELECT count(*)::text||':'||coalesce(md5(string_agg(md5(row_to_json(t)::text),'' ORDER BY md5(row_to_json(t)::text))),md5('')) FROM ` + pq.QuoteIdentifier(schema) + `.` + pq.QuoteIdentifier(table) + ` t`
			if e = db.QueryRow(q).Scan(&value); e != nil {
				t.Fatal(e)
			}
			values[table] = value
		}
		return values
	}
	before := fingerprint(store.DB())
	stableSource := false
	// Scope-close evidence may finish just after the visible outcome; wait for a
	// stable stopped source, rather than treating a live snapshot as unchanged.
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		next := fingerprint(store.DB())
		if reflect.DeepEqual(before, next) {
			stableSource = true
			break
		}
		before = next
	}
	if !stableSource {
		t.Fatal("stopped source did not become stable before backup")
	}
	artifact := filepath.Join(t.TempDir(), "isolated.dump")
	command := func(name string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), filepath.Join(bin, name), args...)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("native %s failed (%d diagnostic bytes)", name, len(out))
		}
	}
	command("pg_dump", "--format=custom", "--no-owner", "--no-acl", "--file="+artifact, sourceURL.String())
	command("pg_restore", "--list", artifact)
	targetURL := *u
	targetURL.Path = "/" + targetName
	command("pg_restore", "--exit-on-error", "--single-transaction", "--no-owner", "--no-acl", "--dbname="+targetURL.String(), artifact)
	query := targetURL.Query()
	query.Set("search_path", schema)
	targetURL.RawQuery = query.Encode()
	restored, err := postgres.New(targetURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.CheckSchemaVersion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := fingerprint(restored.DB()); !reflect.DeepEqual(before, after) {
		t.Fatal("restored module table fingerprints differ")
	}
	if applied, e := restored.Migrate(t.Context()); e != nil || len(applied) != 0 {
		t.Fatal("restored latest schema unexpectedly migrated", e)
	}
	current, err := restored.GetGenerationRun(t.Context(), "p6-pending")
	if err != nil {
		t.Fatal(err)
	}
	var left, right any
	json.Unmarshal(pending.RequestJSON, &left)
	json.Unmarshal(current.RequestJSON, &right)
	if !reflect.DeepEqual(left, right) {
		t.Fatal("backup changed frozen pending request")
	}
	checker := aicheck.NewService(client, restored, restored, restored, "restore-controlled")
	pipe := pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(restored, restored, restored, nil), nil, restored)
	pipe.SetDraftChecker(checker)
	restoreCtx, stop := context.WithCancel(t.Context())
	defer stop()
	checker.StartWorkers(restoreCtx, 2)
	pipe.StartGenerationWorkers(restoreCtx, 2)
	deadline = time.Now().Add(10 * time.Second)
	resumed := false
	for time.Now().Before(deadline) {
		run, e := restored.GetGenerationRun(t.Context(), "p6-pending")
		if e != nil {
			t.Fatal(e)
		}
		if len(run.QuestionIDs) == 2 {
			_, progress, e := checker.ProgressByQuestionIDs(t.Context(), run.QuestionIDs)
			if e == nil && progress["passed"] == 2 {
				resumed = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !resumed {
		t.Fatal("restored pending first checks did not finish")
	}
	if g, c := client.counts(); g != 4 || c != 5 {
		t.Fatal("restore repeated completed work or lost pending task", g, c)
	}
	if q, e := restored.GetQuestion(t.Context(), completed.ID); e != nil || q == nil || q.Version != 1 || q.Status != domain.StatusAIReviewed {
		t.Fatal("restored completed question changed", e)
	}
	info, _ := os.Stat(artifact)
	t.Logf("LOCAL_RESTORE tables=%d dump_bytes=%d schema=%d resumed_questions=2 no_recheck_of_completed=1", len(before), info.Size(), postgres.LatestSchemaVersion)
}
