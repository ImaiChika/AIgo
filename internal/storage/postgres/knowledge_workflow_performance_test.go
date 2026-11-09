//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"github.com/lib/pq"
)

type knowledgeWorkflowLegacyPerformanceReader struct {
	knowledgebase.Store
	store *Store
}

// Retain the pre-optimization round trips as a benchmark control only. Both
// paths use the same database, fixture, access checks and snapshot serializer.
func (r knowledgeWorkflowLegacyPerformanceReader) ResolveKnowledgeCardPoints(ctx context.Context, points []domain.KnowledgePoint) ([]domain.KnowledgePoint, error) {
	if _, err := r.store.execContext(ctx, `SELECT pg_advisory_xact_lock_shared(4704385982817583184)`); err != nil {
		return nil, err
	}
	result := make([]domain.KnowledgePoint, 0, len(points))
	for _, p := range points {
		var version string
		err := r.store.queryRowContext(ctx, `SELECT id FROM knowledge_versions WHERE id=$1 AND deleted_at IS NULL FOR KEY SHARE`, p.VersionID).Scan(&version)
		if err != nil {
			return nil, err
		}
		kp, err := scanKP(r.store.queryRowContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.id=$1 AND p.deleted_at IS NULL FOR SHARE OF p`, p.ID))
		if err != nil {
			return nil, err
		}
		if kp == nil || kp.VersionID != p.VersionID {
			return nil, knowledgebase.ErrInvalid
		}
		result = append(result, *kp)
	}
	return result, nil
}

// Only synthetic data is seeded. Preparation includes the normal authorization
// transaction, point resolution, bounded card selection and JSON snapshot copy.
func knowledgeWorkflowPerformanceStore(b *testing.B, background int) (*Store, []domain.KnowledgePoint) {
	b.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		b.Skip("set AIGO_TEST_POSTGRES_DSN for isolated knowledge preparation benchmarks")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		b.Fatal(err)
	}
	schema := fmt.Sprintf("workflow_perf_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		_ = admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		b.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := New(u.String())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if _, err = s.Migrate(b.Context()); err != nil {
		b.Fatal(err)
	}
	if _, err = s.DB().Exec(`INSERT INTO roles(id,name,permissions) VALUES('perf-role','隔离测试',ARRAY['kb:view','kb:use']); INSERT INTO users(id,username,password_hash,display_name,role,roles,enabled) VALUES('perf-user','perf-user','unused','隔离测试','perf-role',ARRAY['perf-role'],true); INSERT INTO knowledge_versions(id,name,year,status) VALUES('perf-version','隔离性能大纲',2024,'published')`); err != nil {
		b.Fatal(err)
	}
	var points []domain.KnowledgePoint
	for i := 0; i < 100; i++ {
		p := domain.KnowledgePoint{ID: fmt.Sprintf("perf-point-%03d", i), VersionID: "perf-version", VersionName: "隔离性能大纲", VersionYear: 2024, Revision: 1, Category: "临床综合", Subject: "隔离专业", Topic: "隔离考点", OutlineCode: fmt.Sprintf("perf.%03d", i)}
		if _, err = s.DB().Exec(`INSERT INTO knowledge_points(id,version_id,category,subject,topic,outline_code,keywords,revision) VALUES($1,$2,$3,$4,$5,$6,'{}',1)`, p.ID, p.VersionID, p.Category, p.Subject, p.Topic, p.OutlineCode); err != nil {
			b.Fatal(err)
		}
		points = append(points, p)
		for _, kind := range []string{"medical", "exam_reference", "custom"} {
			c := domain.KnowledgeCard{ID: fmt.Sprintf("perf-%03d-%s", i, kind), Revision: 1, Kind: kind, Status: "active", Title: "仅性能测试资料", Source: "人工合成，非医学认证", KnowledgePoints: []domain.KnowledgePoint{p}}
			switch kind {
			case "medical":
				c.Content = domain.KnowledgeContent{ExamTargets: []string{"隔离目标"}, Features: []string{"隔离特征"}, Preconditions: []string{"仅适用合成验收"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "隔离事实", AppliesWhen: "仅合成场景", Source: "人工合成"}}}
			case "exam_reference":
				c.Content.Reference = &domain.ExamReference{Stem: "人工合成，非真题。该患者最可能的诊断是"}
			case "custom":
				c.Content.CustomText = "人工合成病例，仅用于性能测量。"
			}
			body, _ := json.Marshal(c)
			if _, err = s.DB().Exec(`INSERT INTO knowledge_cards(id,revision,kind,status,body) VALUES($1,1,$2,'active',$3)`, c.ID, c.Kind, body); err != nil {
				b.Fatal(err)
			}
		}
	}
	if background > 0 {
		template := domain.KnowledgeCard{Revision: 1, Kind: "custom", Status: "active", Title: "无关隔离资料", KnowledgePoints: []domain.KnowledgePoint{{ID: "unrelated-point", VersionID: "perf-version", Revision: 1}}, Content: domain.KnowledgeContent{CustomText: "不应进入所选考点的模拟资料"}}
		raw, _ := json.Marshal(template)
		if _, err = s.DB().Exec(`INSERT INTO knowledge_cards(id,revision,kind,status,body) SELECT 'perf-background-'||n,1,'custom','active',$1::jsonb || jsonb_build_object('id','perf-background-'||n) FROM generate_series(1,$2::int) n`, raw, background); err != nil {
			b.Fatal(err)
		}
	}
	if _, err = s.DB().Exec(`ANALYZE knowledge_cards; ANALYZE knowledge_points`); err != nil {
		b.Fatal(err)
	}
	return s, points
}

func BenchmarkKnowledgeWorkflowPrepare(b *testing.B) {
	benchmarkKnowledgeWorkflowPrepare(b, 0)
}

func BenchmarkKnowledgeWorkflowPrepareLargeCatalog(b *testing.B) {
	benchmarkKnowledgeWorkflowPrepare(b, 10000)
}

func benchmarkKnowledgeWorkflowPrepare(b *testing.B, background int) {
	s, points := knowledgeWorkflowPerformanceStore(b, background)

	for _, mode := range []struct {
		name   string
		reader knowledgebase.Store
	}{{"legacy", knowledgeWorkflowLegacyPerformanceReader{Store: s, store: s}}, {"current", s}} {
		svc := knowledgebase.Service{Store: mode.reader}
		for _, count := range []int{1, 10, 100} {
			b.Run(fmt.Sprintf("%s/points_%d", mode.name, count), func(b *testing.B) {
				b.ReportAllocs()
				var size int
				for b.Loop() {
					err := svc.WithPreparedSnapshot(b.Context(), "perf-user", "perf-role", "knowledge", points[:count], func(_ context.Context, snapshot *domain.KnowledgeWorkflowSnapshot) error {
						if len(snapshot.Bundles) != count {
							return fmt.Errorf("missing prepared bundles")
						}
						raw, err := json.Marshal(snapshot)
						size = len(raw)
						return err
					})
					if err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(size), "snapshot_B")
			})
		}
	}
}
