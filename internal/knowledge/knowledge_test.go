package knowledge

import (
	"context"
	"testing"

	"aigo/internal/storage"
)

func TestImportAndSearch(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryKnowledgeStore()
	svc := NewService(store)

	// 导入
	count, err := svc.ImportFromXlsx(ctx, "../../知识点表.xlsx")
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	t.Logf("导入 %d 个知识点", count)

	// 统计
	total, _ := svc.Count(ctx)
	t.Logf("总量: %d", total)

	// 搜索
	results, _ := svc.Search(ctx, "肺炎")
	t.Logf("搜索'肺炎': %d 条", len(results))
	for _, r := range results {
		t.Logf("  %s | %s | %s", r.ID, r.System, r.Topic)
	}

	// 按系统筛选
	systems, _ := svc.ListSystems(ctx)
	t.Logf("系统数: %d", len(systems))
	for sys, cnt := range systems {
		t.Logf("  %s: %d", sys, cnt)
	}
}
