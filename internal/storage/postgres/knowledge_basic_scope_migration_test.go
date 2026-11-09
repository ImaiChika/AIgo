//go:build integration

package postgres

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/knowledge"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
)

func basicScopeUpgradeStore(t *testing.T) *Store {
	t.Helper()
	_, dsn, cleanup := migrationTestSchema(t)
	t.Cleanup(cleanup)
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	migrations := configuredMigrations(baselineSchemaSQL)
	// 先应用 1—55 与 58（个人题库列，供 SaveQuestion 写入新字段），保持 56
	//（基础范围剔除）未应用：本测试验证的正是 56 的行为。
	applyMigrationPrefix(t, t.Context(), s, migrations[:55])
	applyMigrationPrefix(t, t.Context(), s, migrations[57:59])
	return s
}

func TestBasicScopeUpgradeWithRealCardServiceAndFrozenHistory(t *testing.T) {
	s := basicScopeUpgradeStore(t)
	v, _, _, _, basic, _, _ := seedBasicScopeFixture(t, s)
	a := auth.NewService(s.DB(), "c1-isolated-card-test-secret", time.Hour)
	if err := a.InitBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.InitAdmin("c1-migration-admin", "c1-local-password", "迁移验证"); err != nil {
		t.Fatal(err)
	}
	u, err := a.GetUserByUsername("c1-migration-admin")
	if err != nil {
		t.Fatal(err)
	}
	point, err := s.GetPoint(t.Context(), basic.ID)
	if err != nil {
		t.Fatal(err)
	}
	kb := knowledgebase.Service{Store: s}
	card, err := kb.Save(t.Context(), u.ID, u.Role, "", domain.KnowledgeCard{Kind: "custom", Title: "真实历史卡", KnowledgePoints: []domain.KnowledgePoint{*point}, Content: domain.KnowledgeContent{CustomText: "迁移前资料"}})
	if err != nil {
		t.Fatal(err)
	}
	card, err = kb.Transition(t.Context(), u.ID, u.Role, card.ID, "activate", card.Revision)
	if err != nil {
		t.Fatal(err)
	}
	q := domain.A2Question{ID: "historical-basic-question", ClinicalStem: "患者，男，40岁。旧题病例。该患者最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}}, Answer: "A", KnowledgePoints: []domain.KnowledgePoint{*point}, Status: domain.StatusAIReviewed, Version: 1, OwnerID: u.ID}
	if err = s.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"knowledge_points": []domain.KnowledgePoint{*point}, "cards": []domain.KnowledgeCard{card}})
	if _, err = s.db.Exec(`INSERT INTO question_generation_context(question_id,owner_id,workflow_version,body) VALUES($1,$2,'knowledge-v1',$3)`, q.ID, u.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO generation_runs(id,owner_id,status,requested_count,request_json) VALUES('old-frozen-run',$1,'pending',1,$2)`, u.ID, raw); err != nil {
		t.Fatal(err)
	}
	pointsJSON, _ := json.Marshal([]domain.KnowledgePoint{*point})
	if _, err = s.db.Exec(`INSERT INTO batch_jobs(id,backend,status,total_count,points_json,output_json,workflow_snapshot) VALUES('old-frozen-batch','local_single_api','in_progress',1,$1,'{}',$2)`, pointsJSON, raw); err != nil {
		t.Fatal(err)
	}
	hash := func(table string) string {
		var out string
		if err := s.db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + table + ` t`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := map[string]string{}
	for _, table := range []string{"questions", "question_versions", "question_generation_context", "generation_runs", "batch_jobs"} {
		before[table] = hash(table)
	}
	var originalCardHistory string
	if err = s.db.QueryRow(`SELECT jsonb_agg(body ORDER BY revision)::text FROM knowledge_card_versions WHERE card_id=$1`, card.ID).Scan(&originalCardHistory); err != nil {
		t.Fatal(err)
	}
	// 只应用 56（基础范围剔除）后核对冻结表：整行哈希覆盖所有列，
	// 因此后续新增列的迁移（如 57 批量计划）不能混入本断言。
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[55:56])
	for table, want := range before {
		if hash(table) != want {
			t.Fatalf("migration rewrote frozen historical table %s", table)
		}
	}
	history, err := s.KnowledgeCardHistory(t.Context(), card.ID)
	if err != nil || len(history) != 3 || history[0].Status != "archived" || history[0].Revision != 3 {
		t.Fatalf("real card migration/history invalid: %+v %v", history, err)
	}
	var preserved string
	if err = s.db.QueryRow(`SELECT jsonb_agg(body ORDER BY revision)::text FROM knowledge_card_versions WHERE card_id=$1 AND revision<=2`, card.ID).Scan(&preserved); err != nil || preserved != originalCardHistory {
		t.Fatal("original normal-service card revisions changed", err)
	}
	// Exercise SQL idempotence itself, not just the migration ledger skip.
	state := hash("knowledge_points") + hash("knowledge_cards") + hash("knowledge_card_versions")
	for _, statement := range removeBasicMedicalScopeStatements() {
		if _, err = s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if state != hash("knowledge_points")+hash("knowledge_cards")+hash("knowledge_card_versions") {
		t.Fatal("reapplying scope SQL changed revisions or content")
	}
	if points, err := s.ListVersionPoints(t.Context(), v); err != nil || len(points) != 1 {
		t.Fatal(points, err)
	}
}

func TestBasicScopeUpgradePreservesCompleteCardHistory(t *testing.T) {
	s := basicScopeUpgradeStore(t)
	seedBasicScopeFixture(t, s)
	// Normal WriteKnowledgeCard stores the CURRENT revision in both tables.
	// The original C1 fixture omitted this history and hid the upgrade failure.
	if _, err := s.db.Exec(`INSERT INTO knowledge_card_versions(card_id,revision,body) SELECT id,revision,body FROM knowledge_cards`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.db.QueryRow(`SELECT body::text FROM knowledge_card_versions WHERE card_id='card-mixed' AND revision=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[55:56])
	history, err := s.KnowledgeCardHistory(t.Context(), "card-mixed")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.GetKnowledgeCard(t.Context(), "card-mixed")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Revision != current.Revision || len(history[0].KnowledgePoints) != 1 {
		t.Fatalf("history must include migrated current revision, current=%+v history=%+v", current, history)
	}
	var after string
	if err := s.db.QueryRow(`SELECT body::text FROM knowledge_card_versions WHERE card_id='card-mixed' AND revision=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("immutable original card history changed")
	}
}

func TestBasicScopeUpgradeQuarantinesConflictedAndWhitespaceRows(t *testing.T) {
	s := basicScopeUpgradeStore(t)
	v, _, _, _, _, _, _ := seedBasicScopeFixture(t, s)
	cases := []struct{ name, category, subject string }{
		{"conflicted", "临床综合", "药理"},
		{"tab_basic", "\t基础医学\t", "生理"},
		{"wide_space_basic", "\u3000基础医学\u3000", "病理"},
	}
	for _, tc := range cases {
		_, err := s.db.Exec(`INSERT INTO knowledge_points(id,version_id,category,subject,topic,outline_code) VALUES($1,$2,$3,$4,'历史考点',$1)`, tc.name, v, tc.category, tc.subject)
		if err != nil {
			t.Fatal(err)
		}
	}
	// 56（被测迁移）尚未应用：显式应用后再断言；57/58 已在夹具中就位。
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[55:56])
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := s.GetPoint(t.Context(), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Fatalf("non-clinical legacy row still selectable: %+v", p)
			}
		})
	}
}

func TestBasicScopeRemovedCodeCannotReenterWithFreshID(t *testing.T) {
	for _, entry := range []string{"create", "import"} {
		t.Run(entry, func(t *testing.T) {
			s := basicScopeUpgradeStore(t)
			v, _, _, _, basic, _, _ := seedBasicScopeFixture(t, s)
			applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[55:56])
			forged := domain.KnowledgePoint{ID: "fresh-clinical-id", VersionID: v, Category: "临床综合", Subject: "呼吸系统", Topic: "重新包装的历史基础条目", OutlineCode: basic.OutlineCode}
			var err error
			if entry == "create" {
				_, err = knowledge.NewService(s).CreatePoint(t.Context(), forged)
			} else {
				_, _, _, err = s.SaveVersionPoints(t.Context(), v, []domain.KnowledgePoint{forged}, false)
			}
			if err == nil {
				t.Fatal("a fresh ID bypassed the removed basic-code guard")
			}
		})
	}
}

func TestBasicScopePreviewRejectsRemovedCode(t *testing.T) {
	s := basicScopeUpgradeStore(t)
	v, _, _, _, basic, _, _ := seedBasicScopeFixture(t, s)
	// 56（被测迁移）尚未应用：显式应用后再断言；57/58 已在夹具中就位。
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[55:56])
	path := filepath.Join(t.TempDir(), "outline.csv")
	if err := os.WriteFile(path, []byte("分类,专业/系统,单元,细目,要点,大纲代码\n临床综合,呼吸系统,感染,肺炎,换标签重新录入,"+basic.OutlineCode+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := knowledge.NewService(s).PreviewDocuments(t.Context(), v, []string{path}, false)
	if err != nil {
		t.Fatal(err)
	}
	if preview.InvalidCount == 0 {
		t.Fatalf("known removed code was offered for confirmation: %+v", preview)
	}
}

func TestBasicScopeGuardsRejectIncomingWritesButAllowOtherYearCodes(t *testing.T) {
	for _, name := range []string{"new_basic", "new_conflicted", "edit_basic", "other_year_same_code"} {
		t.Run(name, func(t *testing.T) {
			s := basicScopeUpgradeStore(t)
			v, _, _, _, basic, clinical, _ := seedBasicScopeFixture(t, s)
			applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[55:56])
			if name == "other_year_same_code" {
				if err := s.CreateKnowledgeVersion(t.Context(), domain.KnowledgeVersion{ID: "different-year", Name: "其他年度临床版", Year: 2028, CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				_, err := knowledge.NewService(s).CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: "different-year", Category: "临床综合", Subject: "呼吸系统", Topic: "新年度临床考点", OutlineCode: basic.OutlineCode})
				if err != nil {
					t.Fatalf("removed code from another year incorrectly blocked new year: %v", err)
				}
				return
			}
			p := domain.KnowledgePoint{ID: "invalid-new", VersionID: v, Category: "基础医学", Subject: "生理", Topic: "不应写入", OutlineCode: "invalid.new"}
			if name == "new_conflicted" {
				p.Category = "临床综合"
			}
			if name == "edit_basic" {
				old, err := s.GetPoint(t.Context(), clinical.ID)
				if err != nil {
					t.Fatal(err)
				}
				p = *old
				p.Category = "基础医学"
			}
			if err := s.UpdatePoint(t.Context(), p); !errors.Is(err, storage.ErrKnowledgeConflict) {
				t.Fatalf("storage accepted non-clinical %s: %v", name, err)
			}
			if name == "edit_basic" {
				after, err := s.GetPoint(t.Context(), clinical.ID)
				if err != nil || after.Category != clinical.Category || after.Revision != 1 {
					t.Fatalf("rejected edit changed original row: %+v %v", after, err)
				}
			} else if after, err := s.GetPoint(t.Context(), p.ID); err != nil || after != nil {
				t.Fatal("rejected create left a point", after, err)
			}
		})
	}
}
