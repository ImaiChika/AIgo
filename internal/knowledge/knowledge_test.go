package knowledge

import (
	"context"
	"os"
	"testing"

	"aigo/internal/storage/testutil"
)

func TestImportAndSearch(t *testing.T) {
	// 大纲原件按约定不入 git（资料保密），无本地样例时跳过，保证 CI 可全量运行
	const outline = "../../data/reference/2024年临床医师考试大纲代码给HCH老师-仅限课题使用勿外传.xlsx"
	if _, err := os.Stat(outline); err != nil {
		t.Skipf("本地样例不可用，跳过: %s: %v", outline, err)
	}
	ctx := context.Background()
	store := testutil.NewMemoryKnowledgeStore()
	svc := NewService(store)

	// 导入（使用新大纲文件）
	inserted, updated, duplicated, err := svc.ImportFromXlsx(ctx, outline)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	t.Logf("导入: 新增 %d, 更新 %d, 重复 %d", inserted, updated, duplicated)

	// 再次导入同一文件：全部应为重复（验证去重）
	inserted2, _, duplicated2, err := svc.ImportFromXlsx(ctx, outline)
	if err != nil {
		t.Fatalf("Reimport failed: %v", err)
	}
	t.Logf("重复导入: 新增 %d, 跳过重复 %d", inserted2, duplicated2)
	if inserted2 != 0 || duplicated2 == 0 {
		t.Fatalf("重复导入应全部跳过：新增 %d, 重复 %d", inserted2, duplicated2)
	}

	// 统计
	total, _ := svc.Count(ctx)
	t.Logf("总量: %d", total)

	// 搜索
	results, _ := svc.Search(ctx, "充血")
	t.Logf("搜索'充血': %d 条", len(results))
	for _, r := range results {
		t.Logf("  %s | %s | %s | %s", r.ID, r.Subject, r.Unit, r.Topic)
	}

	// 按分类统计
	categories, _ := svc.ListCategories(ctx)
	t.Logf("分类数: %d", len(categories))
	for cat, cnt := range categories {
		t.Logf("  %s: %d", cat, cnt)
	}
}
