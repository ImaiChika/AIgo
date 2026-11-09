package postgres

import (
	"strings"
	"testing"

	"aigo/internal/llm"
)

func TestNetworkDefaultsUpgradeOnlyInitializesUntouchedBootstrap(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		revision                                 int64
		search, extract, wantSearch, wantExtract bool
	}{
		{"bootstrap", 1, false, false, true, true},
		{"explicit_off", 7, false, false, false, false},
		{"search_only", 7, true, false, true, false},
		{"both_on", 7, true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dsn, cleanup := migrationTestSchema(t)
			defer cleanup()
			s, err := New(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:47])
			if _, err := s.DB().Exec(`UPDATE generation_quota SET revision=$1,web_search_enabled=$2,web_extractor_enabled=$3 WHERE id=1`, tc.revision, tc.search, tc.extract); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			q, _, err := s.GetGenerationQuota(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if q.Revision != tc.revision+1 || q.LearningConcurrency != 2 || q.ModelTools.WebSearchEnabled != tc.wantSearch || q.ModelTools.WebExtractorEnabled != tc.wantExtract {
				t.Fatalf("unexpected upgrade: %+v %+v", q, q.ModelTools)
			}
			if applied, err := s.Migrate(t.Context()); err != nil || len(applied) != 0 {
				t.Fatalf("replay=%+v %v", applied, err)
			}
			var defaults int
			if err := s.DB().QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='generation_quota' AND column_name IN ('web_search_enabled','web_extractor_enabled') AND column_default='true'`).Scan(&defaults); err != nil || defaults != 2 {
				t.Fatalf("new defaults=%d err=%v", defaults, err)
			}
		})
	}
}

func TestQuotaAuditCommitsExactStatesAndRollsBackOnLogFailure(t *testing.T) {
	s, ctx := newAICheckOutcomeTestStore(t)
	q, _, err := s.GetGenerationQuota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	q.ModelTools = &llm.NetworkTools{}
	if err := s.SaveGenerationQuotaWithAudit(ctx, q, q.Revision, "operator-test"); err != nil {
		t.Fatal(err)
	}
	logs, err := s.ListLogsByActor(ctx, "operator-test")
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%+v %v", logs, err)
	}
	if logs[0].Action != "update_generation_quota" || !strings.Contains(logs[0].Detail, "联网搜索：开启 → 关闭") || !strings.Contains(logs[0].Detail, "网页抓取：开启 → 关闭") {
		t.Fatalf("missing exact audited states: %+v", logs[0])
	}
	current, _, _ := s.GetGenerationQuota(ctx)
	if err := s.SaveGenerationQuotaWithAudit(ctx, q, q.Revision, "operator-test"); err == nil {
		t.Fatal("stale save succeeded")
	}
	logs, _ = s.ListLogsByActor(ctx, "operator-test")
	if len(logs) != 1 {
		t.Fatal("conflict created an audit row")
	}
	if _, err := s.DB().Exec(`CREATE FUNCTION reject_quota_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='update_generation_quota' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_quota_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_quota_audit()`); err != nil {
		t.Fatal(err)
	}
	current.ModelTools = &llm.NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}
	if err := s.SaveGenerationQuotaWithAudit(ctx, current, current.Revision, "operator-test"); err == nil {
		t.Fatal("audit failure was ignored")
	}
	actual, _, _ := s.GetGenerationQuota(ctx)
	if actual.Revision != current.Revision || actual.ModelTools.WebSearchEnabled || actual.ModelTools.WebExtractorEnabled {
		t.Fatalf("audit failed but settings changed: %+v %+v", actual, actual.ModelTools)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := s.ResolveNetworkTools(ctx)
	if p.WebSearchEnabled || p.WebExtractorEnabled {
		t.Fatal("restart/migration reset explicit off choice")
	}
}
