// 两个存储后端（内存 + PostgreSQL）的 SearchQuestions 语义一致性测试。
// 断言同一份种子数据在两种实现下得到相同的过滤、排序和分页结果。
package storage_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/postgres"
	"aigo/internal/storage/testutil"

	"github.com/lib/pq"
)

func TestSearchQuestionsSemantics(t *testing.T) {
	backends := []struct {
		name        string
		open        func(t *testing.T) (storage.QuestionStore, func())
		createBanks bool // PostgreSQL 有外键约束，题库必须先建；内存实现无此约束
	}{
		{name: "memory", open: openMemoryQuestionStore, createBanks: false},
		{name: "postgres", open: openPostgresQuestionStore, createBanks: true},
	}
	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			runSearchQuestionsSemantics(t, backend.open, backend.createBanks)
		})
	}
}

func openMemoryQuestionStore(t *testing.T) (storage.QuestionStore, func()) {
	t.Helper()
	return testutil.NewMemoryStore(), func() {}
}

func openPostgresQuestionStore(t *testing.T) (storage.QuestionStore, func()) {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL search semantics test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := fmt.Sprintf("aigo_search_sem_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	store, err := postgres.New(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitSchema(""); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = store.Close()
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)
		_ = adminDB.Close()
	}
	return store, cleanup
}

// seedSearchQuestions 覆盖全部过滤维度的种子数据（创建时间递增，q5 最新）。
func seedSearchQuestions(t *testing.T, store storage.QuestionStore, createBanks bool) {
	t.Helper()
	ctx := context.Background()
	if createBanks {
		for _, bank := range []domain.QuestionBank{
			{ID: "bank-a", Name: "内科题库", CreatedAt: time.Now()},
			{ID: "bank-b", Name: "外科题库", CreatedAt: time.Now()},
		} {
			if err := store.(interface {
				SaveBank(context.Context, domain.QuestionBank) error
			}).SaveBank(ctx, bank); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed := []domain.A2Question{
		{ID: "q1", Status: domain.StatusAIDraft, Difficulty: "0.65", Profession: "内科", System: "传染病、性传播疾病", OutlineCode: "110.4.3.1.1", ClinicalStem: "患者胸痛考虑急性心肌梗死", Explanation: "考虑冠状动脉闭塞", Answer: "A", BankIDs: []string{"bank-a"}},
		{ID: "q2", Status: domain.StatusPublished, Difficulty: "0.85", Profession: "外科", OutlineCode: "110.5.1.2", ClinicalStem: "患者胃溃疡穿孔需要急诊手术", Options: []domain.Option{{Label: "A", Text: "观察"}, {Label: "B", Text: "出现腹膜刺激征时手术"}}, Answer: "B", BankIDs: []string{"bank-a", "bank-b"}},
		{ID: "q3", Status: domain.StatusAIDraft, Difficulty: "0.65", Profession: "内科", OutlineCode: "210.1.1", ClinicalStem: "肺部感染影像学表现", KnowledgePoints: []domain.KnowledgePoint{{Topic: "肺炎影像知识点", Subject: "呼吸系统"}}, Answer: "C", BankIDs: nil},
		{ID: "q4", Status: domain.StatusRejected, Difficulty: "0.75", Profession: "儿科", OutlineCode: "110.4.9.9", ClinicalStem: "小儿腹泻脱水的补液方案", Answer: "D", BankIDs: []string{"bank-b"}},
		{ID: "q5", Status: domain.StatusAIDraft, Difficulty: "0.75", Profession: "内科", OutlineCode: "110.4.3.2", ClinicalStem: "生存率100%的患者随访", Answer: "E", BankIDs: []string{"bank-a"}},
	}
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	for i, q := range seed {
		q.Version = 1
		q.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		q.UpdatedAt = q.CreatedAt
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func runSearchQuestionsSemantics(t *testing.T, open func(t *testing.T) (storage.QuestionStore, func()), createBanks bool) {
	t.Helper()
	store, cleanup := open(t)
	defer cleanup()
	seedSearchQuestions(t, store, createBanks)
	ctx := context.Background()

	assertPage := func(name string, filter storage.QuestionFilter, page, pageSize int, wantTotal int, wantIDs ...string) {
		t.Helper()
		items, total, err := store.SearchQuestions(ctx, filter, page, pageSize)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if total != wantTotal {
			t.Fatalf("%s: total=%d want=%d", name, total, wantTotal)
		}
		if len(items) != len(wantIDs) {
			t.Fatalf("%s: got %d items, want %d (%v)", name, len(items), len(wantIDs), items)
		}
		for i, want := range wantIDs {
			if items[i].ID != want {
				t.Fatalf("%s: item[%d]=%s want=%s", name, i, items[i].ID, want)
			}
		}
	}

	// 无过滤：按创建时间倒序全量分页
	assertPage("all page1", storage.QuestionFilter{}, 1, 2, 5, "q5", "q4")
	assertPage("all page2", storage.QuestionFilter{}, 2, 2, 5, "q3", "q2")
	assertPage("all page3", storage.QuestionFilter{}, 3, 2, 5, "q1")

	// 等值过滤
	assertPage("status", storage.QuestionFilter{Status: "ai_draft"}, 1, 100, 3, "q5", "q3", "q1")
	assertPage("difficulty", storage.QuestionFilter{Difficulty: "0.65"}, 1, 100, 2, "q3", "q1")
	assertPage("difficulty medium band", storage.QuestionFilter{DifficultyBand: "medium"}, 1, 100, 4, "q5", "q4", "q3", "q1")
	assertPage("difficulty hard band", storage.QuestionFilter{DifficultyBand: "hard"}, 1, 100, 1, "q2")

	// 专业多选（任一命中）
	assertPage("professions", storage.QuestionFilter{Professions: []string{"外科", "儿科"}}, 1, 100, 2, "q4", "q2")

	// 大纲代码前缀（子树）
	assertPage("outline subtree", storage.QuestionFilter{OutlineCode: "110.4"}, 1, 100, 3, "q5", "q4", "q1")
	assertPage("outline leaf prefix", storage.QuestionFilter{OutlineCode: "110.4.3"}, 1, 100, 2, "q5", "q1")

	// 多字段关键词包含匹配（题干/选项/解析/专业/系统/知识点/ID 等，不区分大小写）
	assertPage("keyword stem", storage.QuestionFilter{Keyword: "溃疡"}, 1, 100, 1, "q2")
	assertPage("keyword answer case-insensitive", storage.QuestionFilter{Keyword: "c"}, 1, 100, 1, "q3")
	assertPage("keyword id", storage.QuestionFilter{Keyword: "Q4"}, 1, 100, 1, "q4")
	assertPage("keyword system", storage.QuestionFilter{Keyword: "传染病"}, 1, 100, 1, "q1")
	assertPage("keyword option", storage.QuestionFilter{Keyword: "腹膜刺激征"}, 1, 100, 1, "q2")
	assertPage("keyword explanation", storage.QuestionFilter{Keyword: "冠状动脉"}, 1, 100, 1, "q1")
	assertPage("keyword knowledge point", storage.QuestionFilter{Keyword: "影像知识点"}, 1, 100, 1, "q3")
	assertPage("multiple keyword AND", storage.QuestionFilter{Keyword: "患者，心肌"}, 1, 100, 1, "q1")
	// LIKE 通配符按字面匹配："%" 只命中题干含字面 % 的 q5，而不是所有行
	assertPage("keyword percent literal", storage.QuestionFilter{Keyword: "%"}, 1, 100, 1, "q5")

	// 题库（多对多）
	assertPage("bank-a", storage.QuestionFilter{BankID: "bank-a"}, 1, 100, 3, "q5", "q2", "q1")
	assertPage("bank-b", storage.QuestionFilter{BankID: "bank-b"}, 1, 100, 2, "q4", "q2")

	// 权限范围：受限用户只见范围内题库；范围为空则什么都看不到
	assertPage("scope bank-b", storage.QuestionFilter{ScopeRestricted: true, BankScope: []string{"bank-b"}}, 1, 100, 2, "q4", "q2")
	assertPage("scope empty", storage.QuestionFilter{ScopeRestricted: true}, 1, 100, 0)
	assertPage("classifiable only", storage.QuestionFilter{ClassifiableOnly: true}, 1, 100, 3, "q5", "q3", "q1")

	// 组合：范围内再按状态筛
	assertPage("scope+status", storage.QuestionFilter{ScopeRestricted: true, BankScope: []string{"bank-a"}, Status: "ai_draft"}, 1, 100, 2, "q5", "q1")

	// 专业去重列表（筛选下拉用）
	professions, err := store.ListProfessions(ctx)
	if err != nil {
		t.Fatalf("ListProfessions: %v", err)
	}
	wantProfessions := []string{"儿科", "内科", "外科"}
	if len(professions) != len(wantProfessions) {
		t.Fatalf("professions=%v want=%v", professions, wantProfessions)
	}
	for i, want := range wantProfessions {
		if professions[i] != want {
			t.Fatalf("professions[%d]=%s want=%s", i, professions[i], want)
		}
	}
}
