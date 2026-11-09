package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// seedBasicScopeFixture 在 Schema55 上构造两份同代码跨年大纲（各含基础与临床点）、
// 一个仅基础点的版本、一个已删除版本，以及四张知识卡（纯基础/混合/纯临床/草稿）。
func seedBasicScopeFixture(t *testing.T, s *Store) (v2024, v2025, vBasicOnly, vDeleted string, basic2024, clinical2024, basicDeletedVer domain.KnowledgePoint) {
	t.Helper()
	ctx := context.Background()
	mk := func(id, name string, year int) string {
		if err := s.CreateKnowledgeVersion(ctx, domain.KnowledgeVersion{ID: id, Name: name, Year: year, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	v2024 = mk("kv-2024", "2024 临床大纲", 2024)
	v2025 = mk("kv-2025", "2025 临床大纲", 2025)
	vBasicOnly = mk("kv-basic-only", "2026 纯基础", 2026)
	vDeleted = mk("kv-deleted", "2020 旧大纲", 2020)
	point := func(version, category, subject, code string) domain.KnowledgePoint {
		return domain.KnowledgePoint{ID: "kp-" + version + "-" + code, VersionID: version, Category: category, Subject: subject, Topic: code + " 的考点内容", OutlineCode: code}
	}
	basic2024 = point(v2024, "基础医学", "生理", "20.1")
	clinical2024 = point(v2024, "临床综合", "一、呼吸系统", "110.1")
	basicDeletedVer = point(vDeleted, "基础医学", "病生", "20.1")
	for _, p := range []domain.KnowledgePoint{
		basic2024, clinical2024,
		point(v2024, "基础医学", "药理", "20.2"),
		point(v2025, "基础医学", "生理", "20.1"),
		point(v2025, "临床综合", "一、呼吸系统", "110.1"),
		point(vBasicOnly, "基础医学", "生化", "20.9"),
		basicDeletedVer,
	} {
		seedLegacyScopePoint(t, s, p)
	}
	// 同代码但临床内容的新点（验证不按代码前缀误删临床点）。
	if err := s.UpdatePoint(ctx, point(v2025, "临床综合", "三、消化系统", "20.3")); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishKnowledgeVersion(ctx, v2024); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteKnowledgeVersion(ctx, vDeleted); err != nil {
		t.Fatal(err)
	}
	seedBasicScopeCard(t, s, "card-basic-only", "active", []domain.KnowledgePoint{basic2024})
	seedBasicScopeCard(t, s, "card-mixed", "active", []domain.KnowledgePoint{basic2024, clinical2024})
	seedBasicScopeCard(t, s, "card-clinical", "active", []domain.KnowledgePoint{clinical2024})
	seedBasicScopeCard(t, s, "card-draft", "draft", []domain.KnowledgePoint{basic2024})
	return v2024, v2025, vBasicOnly, vDeleted, basic2024, clinical2024, basicDeletedVer
}

func seedLegacyScopePoint(t *testing.T, s *Store, p domain.KnowledgePoint) {
	t.Helper()
	// Historical input predates the new write guards. Use SQL only in fixtures.
	_, err := s.db.Exec(`INSERT INTO knowledge_points(id,version_id,category,subject,topic,outline_code) VALUES($1,$2,$3,$4,$5,$6)`, p.ID, p.VersionID, p.Category, p.Subject, p.Topic, p.OutlineCode)
	if err != nil {
		t.Fatal(err)
	}
}

func seedBasicScopeCard(t *testing.T, s *Store, id, status string, points []domain.KnowledgePoint) {
	t.Helper()
	body := map[string]any{
		"id": id, "revision": 1, "kind": "custom", "status": status,
		"title": id, "knowledge_points": points,
		"content": map[string]any{"custom_text": "资料正文"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO knowledge_cards (id, revision, kind, status, body) VALUES ($1, 1, 'custom', $2, $3)`, id, status, raw); err != nil {
		t.Fatal(err)
	}
}

func basicScopeRowState(t *testing.T, s *Store, id string) (deleted bool, revision int, topic string) {
	t.Helper()
	err := s.db.QueryRow(`SELECT deleted_at IS NOT NULL, revision, topic FROM knowledge_points WHERE id=$1`, id).Scan(&deleted, &revision, &topic)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestSchema56RemovesBasicMedicalScope(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	migrations := configuredMigrations(baselineSchemaSQL)
	// C3 之后最新迁移为 58（个人题库归属）；本测试聚焦 56 的剔除行为，
	// 种子在 55 上完成，随后完整 Migrate 依次应用 56—58。
	if len(migrations) != int(LatestSchemaVersion) || migrations[57].Version != 58 {
		t.Fatalf("迁移清单异常: len=%d", len(migrations))
	}
	applyMigrationPrefix(t, ctx, s, migrations[:55])
	v2024, v2025, vBasicOnly, _, basic2024, _, basicDeletedVer := seedBasicScopeFixture(t, s)

	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// 有效大纲不再返回基础点；临床点保留且修订不变。
	for _, version := range []string{v2024, v2025} {
		points, err := s.ListVersionPoints(ctx, version)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range points {
			if domain.ClassifyBasicScope(p) != domain.ScopeClinical {
				t.Fatalf("版本 %s 仍含非临床点 %s", version, p.OutlineCode)
			}
		}
		if len(points) == 0 {
			t.Fatalf("版本 %s 临床点被误删", version)
		}
	}
	if p, _ := s.GetPoint(ctx, basic2024.ID); p != nil {
		t.Fatal("基础点按 ID 读取应为空")
	}
	gone, revision, topic := basicScopeRowState(t, s, basic2024.ID)
	if !gone || revision != 2 || topic != basic2024.Topic {
		t.Fatalf("墓碑行应保留原文并 +1 修订: gone=%v rev=%d topic=%q", gone, revision, topic)
	}
	// 已删除版本中的行不被再次改动（保留原有墓碑语义）。
	dGone, dRevision, _ := basicScopeRowState(t, s, basicDeletedVer.ID)
	if !dGone || dRevision != 2 {
		t.Fatalf("已删除版本行应保持 revision=2: gone=%v rev=%d", dGone, dRevision)
	}
	if err := s.PublishKnowledgeVersion(ctx, vBasicOnly); err == nil {
		t.Fatal("仅基础点的版本不应可启用")
	}

	// 幂等：重复迁移不重复墓碑、不重复递增修订。
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, revision2, _ := basicScopeRowState(t, s, basic2024.ID); revision2 != revision {
		t.Fatalf("重复执行又递增修订: %d -> %d", revision, revision2)
	}

	// 知识卡：纯基础归档、混合收缩、纯临床与草稿不动；历史版本保留。
	cardState := func(id string) (status string, revision int, mappings int, versions int) {
		var body []byte
		if err := s.db.QueryRow(`SELECT body FROM knowledge_cards WHERE id=$1`, id).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var card domain.KnowledgeCard
		if err := json.Unmarshal(body, &card); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT status, revision FROM knowledge_cards WHERE id=$1`, id).Scan(&status, &revision); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_card_versions WHERE card_id=$1`, id).Scan(&versions); err != nil {
			t.Fatal(err)
		}
		return status, revision, len(card.KnowledgePoints), versions
	}
	if status, revision, mappings, versions := cardState("card-basic-only"); status != "archived" || revision != 2 || mappings != 0 || versions != 2 {
		t.Fatalf("纯基础卡应归档并留历史: %s rev=%d map=%d ver=%d", status, revision, mappings, versions)
	}
	if status, revision, mappings, versions := cardState("card-mixed"); status != "active" || revision != 2 || mappings != 1 || versions != 2 {
		t.Fatalf("混合卡应仅留临床映射: %s rev=%d map=%d ver=%d", status, revision, mappings, versions)
	}
	if status, revision, mappings, versions := cardState("card-clinical"); status != "active" || revision != 1 || mappings != 1 || versions != 0 {
		t.Fatalf("纯临床卡不应被改动: %s rev=%d map=%d ver=%d", status, revision, mappings, versions)
	}
	if status, revision, mappings, versions := cardState("card-draft"); status != "draft" || revision != 1 || mappings != 1 || versions != 0 {
		t.Fatalf("草稿卡不应被改动: %s rev=%d map=%d ver=%d", status, revision, mappings, versions)
	}

	// 回流防御：显式携带已剔除基础点 ID 的写入被拒绝；换临床标签同代码新点不复活旧行。
	forged := basic2024
	forged.Category = "临床综合"
	forged.Subject = "一、呼吸系统"
	if _, _, _, err := s.SaveVersionPoints(ctx, v2024, []domain.KnowledgePoint{forged}, false); !errors.Is(err, storage.ErrKnowledgeConflict) {
		t.Fatalf("复活已剔除基础点应 409: %v", err)
	}
	if _, err := s.ListVersionPoints(ctx, v2024); err != nil {
		t.Fatal(err)
	}
	rowCount := 0
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_points WHERE version_id=$1 AND deleted_at IS NULL`, v2024).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("2024 版有效点应为 1，实际 %d", rowCount)
	}
}
