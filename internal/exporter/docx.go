// Package exporter 提供题目导出功能。
// 支持导出为 xlsx 和 docx 格式。
// 格式要求来自《试题命制要求》（二）格式要求。
package exporter

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"

	"aigo/internal/domain"
)

// docxContentTypes 是 OOXML 包的内容类型声明，Word 依赖它识别文档主部件。
const docxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

// docxRels 把包根关系指向文档主部件。
const docxRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

// docxSectionProperties 定义 A4 纵向纸张和 1 英寸页边距。
const docxSectionProperties = `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>`

// ExportToDocx 将题目列表导出为 Word 文档（.docx 格式）。
// 直接按 OOXML 规范生成，不依赖 pandoc 等外部程序，容器与宿主机行为一致。
func ExportToDocx(questions []domain.A2Question, path string) (err error) {
	document, err := buildDocumentXML(questions)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	zw := zip.NewWriter(f)
	parts := []struct {
		name    string
		content string
	}{
		{"[Content_Types].xml", docxContentTypes},
		{"_rels/.rels", docxRels},
		{"word/document.xml", document},
	}
	for _, part := range parts {
		w, createErr := zw.Create(part.name)
		if createErr != nil {
			return createErr
		}
		if _, writeErr := io.WriteString(w, part.content); writeErr != nil {
			return writeErr
		}
	}
	return zw.Close()
}

// paraStyle 描述段落级格式。度量单位为缇（twip），字号为半磅（21 = 10.5pt）。
type paraStyle struct {
	Align       string // center 等 w:jc 值，空表示默认左对齐
	IndentLeft  int
	SpaceBefore int
	SpaceAfter  int
	BorderTop   bool
}

// runStyle 描述字符级格式。
type runStyle struct {
	Size  int
	Bold  bool
	Color string
}

// buildDocumentXML 生成 word/document.xml 的完整内容。
func buildDocumentXML(questions []domain.A2Question) (string, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	b.WriteString(paragraph(run("国家执业医师考试 A2 型试题", runStyle{Size: 32, Bold: true}), paraStyle{Align: "center", SpaceAfter: 120}))
	b.WriteString(paragraph(run(fmt.Sprintf("共 %d 道题", len(questions)), runStyle{Size: 21}), paraStyle{Align: "center", SpaceAfter: 240}))

	for i, q := range questions {
		stem := run(fmt.Sprintf("%d．", i+1), runStyle{Size: 21, Bold: true}) + run(q.ClinicalStem, runStyle{Size: 21})
		b.WriteString(paragraph(stem, paraStyle{SpaceAfter: 120}))

		for _, opt := range q.Options {
			text := fmt.Sprintf("%s．%s", opt.Label, opt.Text)
			b.WriteString(paragraph(run(text, runStyle{Size: 21}), paraStyle{IndentLeft: 480, SpaceAfter: 60}))
		}

		b.WriteString(paragraph(run("答案："+q.Answer, runStyle{Size: 21, Bold: true}), paraStyle{SpaceBefore: 120, SpaceAfter: 60}))
		if q.Explanation != "" {
			b.WriteString(paragraph(run("说明："+q.Explanation, runStyle{Size: 21}), paraStyle{SpaceAfter: 60}))
		}

		meta := make([]string, 0, 8)
		if q.OutlineCode != "" {
			meta = append(meta, "大纲代码："+q.OutlineCode)
		}
		if q.Difficulty != "" {
			meta = append(meta, "预估难度："+string(q.Difficulty))
		}
		if q.CognitiveLevel != "" {
			meta = append(meta, "认知层次："+q.CognitiveLevel)
		}
		if q.ExamPoints != "" {
			meta = append(meta, "考核要点："+q.ExamPoints)
		}
		if q.Profession != "" {
			meta = append(meta, "专业："+q.Profession)
		}
		if q.System != "" {
			meta = append(meta, "系统："+q.System)
		}
		meta = append(meta, "命题人：朝阳医院AI")
		b.WriteString(paragraph(run(strings.Join(meta, "\n"), runStyle{Size: 18, Color: "666666"}), paraStyle{SpaceBefore: 120, SpaceAfter: 240, BorderTop: true}))
	}

	b.WriteString(docxSectionProperties)
	b.WriteString(`</w:body></w:document>`)
	return b.String(), nil
}

// paragraph 包装一个或多个 run，并输出段落属性。
func paragraph(runs string, style paraStyle) string {
	var b strings.Builder
	b.WriteString("<w:p>")
	if style.Align != "" || style.IndentLeft > 0 || style.SpaceBefore > 0 || style.SpaceAfter > 0 || style.BorderTop {
		b.WriteString("<w:pPr>")
		if style.BorderTop {
			b.WriteString(`<w:pBdr><w:top w:val="single" w:sz="4" w:space="4" w:color="EEEEEE"/></w:pBdr>`)
		}
		if style.Align != "" {
			fmt.Fprintf(&b, `<w:jc w:val="%s"/>`, style.Align)
		}
		if style.IndentLeft > 0 {
			fmt.Fprintf(&b, `<w:ind w:left="%d"/>`, style.IndentLeft)
		}
		if style.SpaceBefore > 0 || style.SpaceAfter > 0 {
			b.WriteString("<w:spacing")
			if style.SpaceBefore > 0 {
				fmt.Fprintf(&b, ` w:before="%d"`, style.SpaceBefore)
			}
			if style.SpaceAfter > 0 {
				fmt.Fprintf(&b, ` w:after="%d"`, style.SpaceAfter)
			}
			b.WriteString("/>")
		}
		b.WriteString("</w:pPr>")
	}
	b.WriteString(runs)
	b.WriteString("</w:p>")
	return b.String()
}

// run 生成字符 run；文本中的换行转换为 w:br，保证多行元信息在 Word 中正确换行。
// w:br 必须位于 w:r 内部，不能直接挂在 w:p 下，否则 Word 会忽略换行。
func run(text string, style runStyle) string {
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		b.WriteString("<w:r><w:rPr>")
		b.WriteString(`<w:rFonts w:ascii="宋体" w:eastAsia="宋体" w:hAnsi="宋体"/>`)
		if style.Bold {
			b.WriteString("<w:b/><w:bCs/>")
		}
		if style.Color != "" {
			fmt.Fprintf(&b, `<w:color w:val="%s"/>`, style.Color)
		}
		if style.Size > 0 {
			fmt.Fprintf(&b, `<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, style.Size, style.Size)
		}
		b.WriteString("</w:rPr>")
		if i > 0 {
			b.WriteString("<w:br/>")
		}
		b.WriteString(`<w:t xml:space="preserve">`)
		b.WriteString(escapeXML(line))
		b.WriteString("</w:t></w:r>")
	}
	return b.String()
}

// escapeXML 转义 XML 文本节点的特殊字符。
func escapeXML(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// xml.EscapeText 对 strings.Builder 不会失败；保守回退为空串避免输出半成品。
		return ""
	}
	return b.String()
}
