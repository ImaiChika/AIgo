package knowledge

import (
	"context"
	"testing"

	"aigo/internal/storage/testutil"
)

func TestImportAndSearch(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewMemoryKnowledgeStore()
	svc := NewService(store)

	// 导入（使用新大纲文件）
	count, err := svc.ImportFromXlsx(ctx, "../../2024年临床医师考试大纲代码给HCH老师-仅限课题使用勿外传.xlsx")
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	t.Logf("导入 %d 个知识点", count)

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
