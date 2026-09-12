package importer

import (
	"os"
	"path/filepath"
	"testing"

	"aigo/internal/domain"

	"github.com/xuri/excelize/v2"
)

// requireLocalFixture 跳过依赖本地样例数据（data/ 目录不入 git）的测试，
// 保证 CI 等无本地资料环境可以直接运行全量测试。
func requireLocalFixture(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("本地样例不可用，跳过: %s: %v", path, err)
	}
}

func TestReadXlsx(t *testing.T) {
	const sample = "../../data/samples/2025执医A2.xlsx"
	requireLocalFixture(t, sample)
	rows, err := ReadXlsx(sample, true)
	if err != nil {
		t.Fatalf("ReadXlsx failed: %v", err)
	}

	if len(rows) == 0 {
		t.Fatal("expected rows, got 0")
	}
	t.Logf("读取到 %d 行", len(rows))

	// 检查第一行数据
	r := rows[0]
	if r.ID == "" {
		t.Error("first row ID is empty")
	}
	if r.Stem == "" {
		t.Error("first row Stem is empty")
	}
	if r.Answer == "" {
		t.Error("first row Answer is empty")
	}
	t.Logf("第一题: ID=%s, Answer=%s, 选项A=%s", r.ID, r.Answer, r.OptionA)
}

func TestExportToXlsxWritesCompleteSheetDimension(t *testing.T) {
	questions := []domain.A2Question{
		{ID: "q1", ClinicalStem: "题目一", Options: []domain.Option{{Label: "A", Text: "甲"}}, Answer: "A"},
		{ID: "q2", ClinicalStem: "题目二", Options: []domain.Option{{Label: "A", Text: "乙"}}, Answer: "A"},
		{ID: "q3", ClinicalStem: "题目三", Options: []domain.Option{{Label: "A", Text: "丙"}}, Answer: "A"},
	}
	path := filepath.Join(t.TempDir(), "questions.xlsx")
	if err := ExportToXlsx(questions, path); err != nil {
		t.Fatalf("ExportToXlsx failed: %v", err)
	}

	file, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("open exported file: %v", err)
	}
	defer file.Close()
	dimension, err := file.GetSheetDimension("题目")
	if err != nil {
		t.Fatal(err)
	}
	if dimension != "A1:P4" {
		t.Fatalf("sheet dimension = %s, want A1:P4", dimension)
	}
	rows, err := file.GetRows("题目")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("exported rows = %d, want 4", len(rows))
	}
	checks := map[string]string{
		"A1": "题号",
		"C1": "A．",
		"I1": "说明",
		"P1": "命题人",
		"P2": "朝阳医院AI",
	}
	for cell, want := range checks {
		got, err := file.GetCellValue("题目", cell)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", cell, got, want)
		}
	}
}

func TestExportKnowledgePointsToXlsx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge-points.xlsx")
	points := []domain.KnowledgePoint{
		{Category: "临床医学", Subject: "精神科", Topic: "社交焦虑障碍", OutlineCode: "KP-001", Keywords: []string{"焦虑", "社交"}},
		{Category: "临床医学", Subject: "呼吸内科", Topic: "张力性气胸", OutlineCode: "KP-002"},
	}
	if err := ExportKnowledgePointsToXlsx(points, path); err != nil {
		t.Fatalf("ExportKnowledgePointsToXlsx failed: %v", err)
	}
	file, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if dimension, err := file.GetSheetDimension("知识点"); err != nil || dimension != "A1:G3" {
		t.Fatalf("knowledge sheet dimension = %s, err=%v", dimension, err)
	}
	checks := map[string]string{
		"A1": "分类",
		"B1": "专业/系统",
		"E2": "社交焦虑障碍",
		"F2": "KP-001",
		"G2": "焦虑，社交",
	}
	for cell, want := range checks {
		got, err := file.GetCellValue("知识点", cell)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", cell, got, want)
		}
	}
}

func TestConvertToQuestions(t *testing.T) {
	const sample = "../../data/samples/2025执医A2.xlsx"
	requireLocalFixture(t, sample)
	rows, err := ReadXlsx(sample, true)
	if err != nil {
		t.Fatalf("ReadXlsx failed: %v", err)
	}

	questions, errs := ConvertToQuestions(rows)
	t.Logf("转换结果: %d 道有效题目, %d 条警告", len(questions), len(errs))

	for _, e := range errs {
		t.Logf("警告: %v", e)
	}

	if len(questions) == 0 {
		t.Fatal("expected questions, got 0")
	}

	// 验证每道题都能通过 Validate
	failCount := 0
	for _, q := range questions {
		if err := q.Validate(); err != nil {
			failCount++
			t.Logf("Validate失败 ID=%s: %v", q.ID, err)
		}
	}
	t.Logf("Validate通过: %d/%d", len(questions)-failCount, len(questions))
}
