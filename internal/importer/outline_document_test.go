package importer

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutlineTableHierarchyAndDiagnostics(t *testing.T) {
	points, err := parseOutlineTable([][]string{{"分类", "专业/系统", "单元", "细目", "要点", "大纲代码"}, {"临床综合", "呼吸系统", "肺部感染", "肺炎", "考点1", "001"}, {"", "", "", "", "考点2", "002"}, {"", "", "胸膜疾病", "", "考点3", "003"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 || points[1].SubItem != "肺炎" || points[2].SubItem != "" {
		t.Fatalf("wrong inherited hierarchy: %+v", points)
	}
	_, err = parseOutlineTable([][]string{{"要点", "大纲代码"}, {"考点", ""}})
	if err == nil || !strings.Contains(err.Error(), "第2行") {
		t.Fatalf("missing row error: %v", err)
	}
}

func TestLegacyKnowledgePointTable(t *testing.T) {
	rows := [][]string{
		{"知识点ID", "科目", "系统", "知识点名称", "关键词"},
		{"KP-001", "临床医学", "精神科", "社交焦虑障碍", "社交焦虑障碍、惊恐障碍"},
		{"KP-002", "临床医学", "呼吸内科", "张力性气胸", "张力性气胸"},
	}
	points, err := parseOutlineTable(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].OutlineCode != "KP-001" || points[0].Category != "临床医学" || points[0].Subject != "精神科" {
		t.Fatalf("legacy points: %+v", points)
	}
	if len(points[0].Keywords) != 2 || points[0].Keywords[1] != "惊恐障碍" {
		t.Fatalf("legacy keywords: %+v", points[0].Keywords)
	}
}

func TestLegacyKnowledgeWorkbook(t *testing.T) {
	points, err := ReadOutlineDocument("../../data/knowledge/知识点表.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 516 || points[0].OutlineCode != "KP-001" || points[len(points)-1].OutlineCode != "KP-516" {
		t.Fatalf("legacy workbook points: count=%d first=%+v last=%+v", len(points), points[0], points[len(points)-1])
	}
}

func TestWordTableImport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outline.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(file)
	w, _ := z.Create("word/document.xml")
	fmt.Fprint(w, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:tbl>`)
	for _, row := range [][]string{{"分类", "专业/系统", "单元", "细目", "要点", "大纲代码"}, {"临床综合", "呼吸系统", "肺部感染", "肺炎", "临床表现", "001"}} {
		fmt.Fprint(w, "<w:tr>")
		for _, cell := range row {
			fmt.Fprintf(w, "<w:tc><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:tc>", cell)
		}
		fmt.Fprint(w, "</w:tr>")
	}
	fmt.Fprint(w, "</w:tbl></w:body></w:document>")
	z.Close()
	file.Close()
	ps, err := ReadOutlineDocument(path)
	if err != nil || len(ps) != 1 || ps[0].Topic != "临床表现" {
		t.Fatalf("Word parse: %+v %v", ps, err)
	}
}
