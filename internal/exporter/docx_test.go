package exporter

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"aigo/internal/domain"
)

// 导出的 docx 必须是结构完整的 OOXML 包，且包含题目全部展示字段。
func TestExportToDocxBuildsValidOOXML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "questions.docx")
	question := domain.A2Question{
		ClinicalStem:   "男，45岁。腹痛1天。最可能的诊断是",
		Options:        []domain.Option{{Label: "A", Text: "胃溃疡"}, {Label: "B", Text: "急性胰腺炎"}},
		Answer:         "A",
		Explanation:    "正确答案为 A。故选 A。",
		OutlineCode:    "110.4.3.1.1",
		Difficulty:     "0.65",
		CognitiveLevel: "简单应用",
		ExamPoints:     "诊断与鉴别诊断",
		Profession:     "消化",
		System:         "消化系统",
	}
	if err := ExportToDocx([]domain.A2Question{question}, path); err != nil {
		t.Fatal(err)
	}

	parts := readDocxParts(t, path)
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		if _, ok := parts[name]; !ok {
			t.Fatalf("docx 缺少必需部件 %s", name)
		}
	}

	doc := parts["word/document.xml"]
	for _, want := range []string{
		"国家执业医师考试 A2 型试题",
		"共 1 道题",
		"男，45岁。腹痛1天",
		"A．胃溃疡",
		"B．急性胰腺炎",
		"答案：A",
		"说明：正确答案为 A",
		"大纲代码：110.4.3.1.1",
		"预估难度：0.65",
		"专业：消化",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document.xml 缺少 %q", want)
		}
	}
	for _, unwanted := range []string{"命题人", "朝阳医院AI"} {
		if strings.Contains(doc, unwanted) {
			t.Errorf("document.xml 不应再包含 %q", unwanted)
		}
	}

	// 多行元信息必须用 run 内的 w:br 换行，直接挂在段落下的 w:br 会被 Word 忽略。
	if !strings.Contains(doc, "<w:br/>") {
		t.Error("document.xml 缺少 w:br 换行")
	}
	if strings.Contains(doc, "</w:rPr></w:r><w:br/>") {
		t.Error("w:br 不能位于 run 外")
	}

	// document.xml 必须是合法 XML，Word 才能打开。
	if err := validateXML(doc); err != nil {
		t.Fatalf("document.xml 不是合法 XML: %v", err)
	}
}

// 题干和选项中的特殊字符必须被转义，不能破坏文档结构。
func TestExportToDocxEscapesXML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "escape.docx")
	question := domain.A2Question{
		ClinicalStem: `血压 <90/60mmHg & 心率 >120 次/分，考虑"A"型题`,
		Options:      []domain.Option{{Label: "A", Text: "甲<乙"}},
		Answer:       "A",
	}
	if err := ExportToDocx([]domain.A2Question{question}, path); err != nil {
		t.Fatal(err)
	}
	doc := readDocxParts(t, path)["word/document.xml"]
	if strings.Contains(doc, "<90/60") || strings.Contains(doc, "& 心率") {
		t.Fatalf("特殊字符未转义: %s", doc)
	}
	for _, want := range []string{"&lt;90/60mmHg", "&amp; 心率", "甲&lt;乙"} {
		if !strings.Contains(doc, want) {
			t.Errorf("缺少转义后的文本 %q", want)
		}
	}
	if err := validateXML(doc); err != nil {
		t.Fatalf("document.xml 不是合法 XML: %v", err)
	}
}

// readDocxParts 读取 docx 包内全部部件内容。
func readDocxParts(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("docx 不是有效 zip: %v", err)
	}
	defer zr.Close()
	parts := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开部件 %s 失败: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("读取部件 %s 失败: %v", f.Name, err)
		}
		parts[f.Name] = string(data)
	}
	return parts
}

// validateXML 完整解析 XML，确认没有未闭合标签或非法字符。
func validateXML(content string) error {
	decoder := xml.NewDecoder(strings.NewReader(content))
	for {
		if _, err := decoder.Token(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
