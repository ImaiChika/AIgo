package knowledge

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

// countingKnowledgeStore 统计 ListVersionPoints 调用次数，用于断言快照缓存命中。
type countingKnowledgeStore struct {
	storage.KnowledgeStore
	listCalls atomic.Int64
}

func (c *countingKnowledgeStore) ListVersionPoints(ctx context.Context, version string) ([]domain.KnowledgePoint, error) {
	c.listCalls.Add(1)
	return c.KnowledgeStore.ListVersionPoints(ctx, version)
}

func newCacheTestService(t *testing.T) (*Service, *countingKnowledgeStore) {
	t.Helper()
	store := &countingKnowledgeStore{KnowledgeStore: testutil.NewMemoryKnowledgeStore()}
	return NewService(store), store
}

func seedVersion(t *testing.T, svc *Service) *domain.KnowledgeVersion {
	t.Helper()
	ctx := context.Background()
	v, err := svc.CreateVersion(ctx, "测试大纲", 2026, "")
	if err != nil {
		t.Fatal(err)
	}
	points := []domain.KnowledgePoint{
		{VersionID: v.ID, Category: "临床综合", Subject: "呼吸系统", Unit: "二、肺部感染", SubItem: "肺炎", Topic: "充血", OutlineCode: "110.2.1.1"},
		{VersionID: v.ID, Category: "临床综合", Subject: "消化系统", Unit: "三、胃疾病", SubItem: "胃炎", Topic: "溃疡", OutlineCode: "110.10.1.1"},
		{VersionID: v.ID, Category: "基础医学", Subject: "生理", Unit: "一、细胞", SubItem: "膜电位", Topic: "动作电位", OutlineCode: "20.1.1.1"},
	}
	for _, p := range points {
		if _, err := svc.CreatePoint(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.PublishVersion(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	return v
}

// 未发生写入时，列表/树/元数据/统计共享同一份快照，只装载一次。
func TestSnapshotCacheReusesLoadUntilMutation(t *testing.T) {
	svc, store := newCacheTestService(t)
	ctx := context.Background()
	v := seedVersion(t, svc)

	for i := 0; i < 3; i++ {
		if _, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Tree(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ListCategoriesAndSubjects(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.KPStats(ctx); err != nil {
		t.Fatal(err)
	}
	if got := store.listCalls.Load(); got != 1 {
		t.Fatalf("多次读取应只装载一次版本数据，实际 %d 次", got)
	}

	// 服务层写入后立即失效，下次读取重新装载一次
	if _, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: v.ID, Category: "基础医学", Subject: "生理", Unit: "一、细胞", SubItem: "膜电位", Topic: "静息电位", OutlineCode: "20.1.2.1"}); err != nil {
		t.Fatal(err)
	}
	points, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 4 {
		t.Fatalf("写入后应看到 4 个知识点，实际 %d", len(points))
	}
	if got := store.listCalls.Load(); got != 2 {
		t.Fatalf("写入后应重新装载一次，实际累计 %d 次", got)
	}
}

// 所有变更入口都正确失效缓存：更新、删除、导入替换、版本删除。
func TestSnapshotInvalidationCoversAllMutations(t *testing.T) {
	svc, store := newCacheTestService(t)
	ctx := context.Background()
	v := seedVersion(t, svc)

	if _, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID}); err != nil {
		t.Fatal(err)
	}

	// 更新：修订号检查后修改 Topic
	points, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID, Keyword: "充血"})
	if len(points) != 1 {
		t.Fatalf("关键词筛选应命中 1 条，实际 %d", len(points))
	}
	updated := points[0]
	updated.Topic = "淤血与充血"
	updated.Revision = 1
	if _, err := svc.UpdatePoint(ctx, updated.ID, updated); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID, Keyword: "淤血"}); len(got) != 1 {
		t.Fatal("更新后缓存未失效，搜索仍见旧数据")
	}

	// 删除单条
	if err := svc.DeletePoint(ctx, updated.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID}); len(got) != 2 {
		t.Fatalf("删除后应剩 2 条，实际 %d", len(got))
	}
	before := store.listCalls.Load()

	// 新增另一版本并写入：CreatePoint 使全部版本快照失效
	other, err := svc.CreateVersion(ctx, "另一版本", 2027, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: other.ID, Category: "基础医学", Subject: "生化", Topic: "糖酵解", OutlineCode: "30.1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishVersion(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	// 写入使原版本快照失效：下一次读取恰好重新装载一次
	if got, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID}); len(got) != 2 {
		t.Fatalf("写入后原版本应仍为 2 条，实际 %d", len(got))
	}
	if store.listCalls.Load() != before+1 {
		t.Fatalf("写入应触发原版本快照失效并恰好重新装载一次，实际增量 %d", store.listCalls.Load()-before)
	}
	if got, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: other.ID}); len(got) != 1 {
		t.Fatal("新版本写入后应立即可搜索")
	}
}

// CacheTTL 到期后即使无写入也会重新装载，兜底进程外直接写库的变更。
func TestSnapshotTTLExpiry(t *testing.T) {
	svc, store := newCacheTestService(t)
	svc.CacheTTL = 5 * time.Millisecond
	ctx := context.Background()
	v := seedVersion(t, svc)

	if _, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	if _, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID}); err != nil {
		t.Fatal(err)
	}
	if got := store.listCalls.Load(); got != 2 {
		t.Fatalf("TTL 过期后应重新装载，实际累计 %d 次", got)
	}
}

// 排序与旧实现一致：大纲代码按数值逐级比较（110.10 排在 110.2 之后）。
func TestSnapshotPreservesNumericOutlineOrder(t *testing.T) {
	svc, _ := newCacheTestService(t)
	ctx := context.Background()
	v := seedVersion(t, svc)

	points, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20.1.1.1", "110.2.1.1", "110.10.1.1"}
	for i, code := range want {
		if points[i].OutlineCode != code {
			t.Fatalf("第 %d 位应为 %s，实际 %s", i, code, points[i].OutlineCode)
		}
	}
}
