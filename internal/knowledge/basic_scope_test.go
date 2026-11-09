package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

func newBasicScopeService(t *testing.T) (*Service, *domain.KnowledgeVersion) {
	t.Helper()
	ctx := context.Background()
	svc := NewService(testutil.NewMemoryKnowledgeStore())
	v, err := svc.CreateVersion(ctx, "2026 临床大纲", 2026, "")
	if err != nil {
		t.Fatal(err)
	}
	return svc, v
}

func writeOutlineCSV(t *testing.T, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "大纲.csv")
	content := "分类,专业/系统,单元,细目,要点,大纲代码\n" + strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBasicScopeRejectsCreateAndUpdate(t *testing.T) {
	svc, v := newBasicScopeService(t)
	ctx := context.Background()
	if _, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: v.ID, Category: "基础医学", Subject: "生理", Topic: "动作电位", OutlineCode: "20.1"}); err == nil || !strings.Contains(err.Error(), "基础医学") {
		t.Fatalf("基础分类新增应拒绝: %v", err)
	}
	if _, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: v.ID, Category: "临床综合", Subject: "药理", Topic: "抗菌药物", OutlineCode: "21.1"}); err == nil || !strings.Contains(err.Error(), "矛盾") {
		t.Fatalf("分类矛盾新增应拒绝: %v", err)
	}
	created, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: v.ID, Category: "临床综合", Subject: "一、呼吸系统", Topic: "肺炎", OutlineCode: "110.1"})
	if err != nil {
		t.Fatal(err)
	}
	basic := *created
	basic.Category = "基础医学"
	if _, err := svc.UpdatePoint(ctx, created.ID, basic); err == nil || !strings.Contains(err.Error(), "基础医学") {
		t.Fatalf("编辑成基础分类应拒绝: %v", err)
	}
}

func TestImportDocumentsExcludesBasicAndCounts(t *testing.T) {
	svc, v := newBasicScopeService(t)
	ctx := context.Background()
	path := writeOutlineCSV(t,
		"临床综合,一、呼吸系统,肺部感染,肺炎,肺炎的诊断,110.1",
		"基础医学,生理,细胞基本功能,膜电位,静息电位,20.1",
		"基础医学,病生,缺氧,缺氧类型,缺氧的分类,20.2",
	)
	inserted, _, _, excluded, err := svc.ImportDocuments(ctx, v.ID, []string{path}, false)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 1 || excluded != 2 {
		t.Fatalf("want inserted=1 excluded=2, got %d/%d", inserted, excluded)
	}
	points, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if err != nil || len(points) != 1 {
		t.Fatalf("有效大纲应只剩临床点: %+v %v", points, err)
	}
}

func TestImportDocumentsAllBasicFails(t *testing.T) {
	svc, v := newBasicScopeService(t)
	ctx := context.Background()
	path := writeOutlineCSV(t,
		"基础医学,生理,细胞,膜电位,静息电位,20.1",
		"基础医学,生化,代谢,糖酵解,糖酵解过程,20.2",
	)
	_, _, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{path}, false)
	if err == nil || !strings.Contains(err.Error(), "全部") {
		t.Fatalf("全基础导入应整体拒绝: %v", err)
	}
	points, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if len(points) != 0 {
		t.Fatal("全基础导入不得写入任何内容")
	}
}

func TestImportDocumentsConflictedRejectsWholeBatch(t *testing.T) {
	svc, v := newBasicScopeService(t)
	ctx := context.Background()
	path := writeOutlineCSV(t,
		"临床综合,一、呼吸系统,肺部感染,肺炎,肺炎的诊断,110.1",
		"临床综合,药理,用药,抗菌药,抗菌药物应用,21.1",
	)
	_, _, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{path}, false)
	if err == nil || !strings.Contains(err.Error(), "矛盾") {
		t.Fatalf("分类矛盾应整批拒绝: %v", err)
	}
	points, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if len(points) != 0 {
		t.Fatal("矛盾批次不得部分写入")
	}
}

func TestPreviewDocumentsMatchesImportOutcome(t *testing.T) {
	svc, v := newBasicScopeService(t)
	ctx := context.Background()
	if _, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: v.ID, Category: "临床综合", Subject: "一、呼吸系统", Topic: "哮喘", OutlineCode: "110.9"}); err != nil {
		t.Fatal(err)
	}
	path := writeOutlineCSV(t,
		"临床综合,一、呼吸系统,肺部感染,肺炎,肺炎的诊断,110.1",
		"临床综合,一、呼吸系统,肺部感染,肺炎,肺炎的治疗,110.2",
		"基础医学,免疫,免疫细胞,T细胞,T细胞功能,20.3",
	)
	preview, err := svc.PreviewDocuments(ctx, v.ID, []string{path}, false)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Accepted != 2 || preview.ExcludedBasic != 1 || len(preview.Conflicted) != 0 || len(preview.Invalid) != 0 {
		t.Fatalf("预览计数不符: %+v", preview)
	}
	if preview.TotalCurrent != 1 {
		t.Fatalf("当前总量应为 1，实际 %d", preview.TotalCurrent)
	}
	inserted, _, _, excluded, err := svc.ImportDocuments(ctx, v.ID, []string{path}, false)
	if err != nil || inserted != preview.Accepted || excluded != preview.ExcludedBasic {
		t.Fatalf("确认导入与预览不一致: imported=%d excluded=%d err=%v", inserted, excluded, err)
	}
	replace, err := svc.PreviewDocuments(ctx, v.ID, []string{path}, true)
	if err != nil {
		t.Fatal(err)
	}
	if replace.ReplaceWouldRemove != 1 {
		t.Fatalf("替换模式应提示移除既有哮喘点，实际 %d", replace.ReplaceWouldRemove)
	}
}

func TestPreviewDocumentsRejectsConflictingDuplicateCodes(t *testing.T) {
	svc, v := newBasicScopeService(t)
	path := writeOutlineCSV(t,
		"临床综合,呼吸系统,感染,肺炎,肺炎诊断,clinical.same",
		"临床综合,呼吸系统,感染,肺炎,肺炎治疗,clinical.same",
	)
	preview, err := svc.PreviewDocuments(t.Context(), v.ID, []string{path}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Invalid) == 0 {
		t.Fatalf("conflicting codes were advertised as confirmable: %+v", preview)
	}
	if _, _, _, _, err := svc.ImportDocuments(t.Context(), v.ID, []string{path}, true); err == nil {
		t.Fatal("conflicting code batch must never be written")
	}
}

func TestImportRejectsBasicAndClinicalRowsSharingCode(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			svc, v := newBasicScopeService(t)
			rows := []string{"基础医学,生理,细胞,膜电位,静息电位,shared.code", "临床综合,呼吸系统,感染,肺炎,肺炎诊断,shared.code"}
			if reverse {
				rows[0], rows[1] = rows[1], rows[0]
			}
			path := writeOutlineCSV(t, rows...)
			preview, err := svc.PreviewDocuments(t.Context(), v.ID, []string{path}, false)
			if err != nil {
				t.Fatal(err)
			}
			if preview.InvalidCount == 0 {
				t.Fatalf("same-code mixed classification is confirmable: %+v", preview)
			}
			if _, _, _, _, err := svc.ImportDocuments(t.Context(), v.ID, []string{path}, false); err == nil {
				t.Fatal("same-code mixed classification was written")
			}
			points, _ := svc.SearchFiltered(t.Context(), KPSearchOptions{VersionID: v.ID})
			if len(points) != 0 {
				t.Fatal("invalid batch partially wrote", points)
			}
		})
	}
}

func TestSavePointsFiltersBasicScope(t *testing.T) {
	svc, _ := newBasicScopeService(t)
	ctx := context.Background()
	// SavePoints 落到默认已发布版本（legacy）；不指定 VersionID。
	points := []domain.KnowledgePoint{
		{Category: "临床综合", Subject: "一、呼吸系统", Topic: "肺炎", OutlineCode: "110.1"},
		{Category: "基础医学", Subject: "生理", Topic: "静息电位", OutlineCode: "20.1"},
	}
	inserted, _, _, err := svc.SavePoints(ctx, points)
	if err != nil || inserted != 1 {
		t.Fatalf("want 1 inserted, got %d err=%v", inserted, err)
	}
	all, _ := svc.ListAll(ctx)
	basic := 0
	for _, p := range all {
		if domain.ClassifyBasicScope(p) != domain.ScopeClinical {
			basic++
		}
	}
	if basic != 0 {
		t.Fatalf("有效大纲不应包含基础点，实际 %d 条", basic)
	}
}

func TestStoreRejectsDirectBasicPointWrites(t *testing.T) {
	svc, v := newBasicScopeService(t)
	ctx := context.Background()
	store := svc.store
	basic := domain.KnowledgePoint{VersionID: v.ID, Category: "基础医学", Subject: "生理", Topic: "静息电位", OutlineCode: "20.1", ID: "kp-basic-1", Revision: 0}
	if err := store.UpdatePoint(ctx, basic); !errors.Is(err, storage.ErrKnowledgeConflict) {
		t.Fatalf("存储层也必须拒绝新增基础点: %v", err)
	}
	if p, _ := svc.GetByID(ctx, basic.ID); p != nil {
		t.Fatal("基础点不得复活为有效点")
	}
}
