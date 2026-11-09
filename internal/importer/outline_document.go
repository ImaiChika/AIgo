package importer

import (
	"archive/zip"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"aigo/internal/domain"
	"github.com/xuri/excelize/v2"
)

// ReadOutlineDocument reads structured syllabus tables, not free-form medical
// prose. No model is involved in assigning codes or interpreting source content.
func ReadOutlineDocument(path string) ([]domain.KnowledgePoint, error) {
	tables := map[string][][]string{}
	names := []string{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx":
		f, err := excelize.OpenFile(path, excelize.Options{UnzipSizeLimit: 64 << 20, UnzipXMLSizeLimit: 16 << 20})
		if err != nil {
			return nil, err
		}
		defer f.Close()
		for _, name := range f.GetSheetList() {
			rows, err := f.GetRows(name)
			if err != nil {
				return nil, err
			}
			tables[name] = rows
			names = append(names, name)
		}
	case ".csv":
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(b) {
			return nil, fmt.Errorf("CSV 请使用 UTF-8 编码")
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(b), "\ufeff")))
		reader.FieldsPerRecord = -1
		rows, err := reader.ReadAll()
		if err != nil {
			return nil, err
		}
		tables["CSV"] = rows
		names = append(names, "CSV")
	case ".docx":
		ts, err := readWordTables(path)
		if err != nil {
			return nil, err
		}
		for i, rows := range ts {
			name := fmt.Sprintf("表格%d", i+1)
			tables[name] = rows
			names = append(names, name)
		}
	default:
		return nil, fmt.Errorf("支持 .xlsx、.csv 和包含大纲表格的 .docx 文件")
	}
	points := []domain.KnowledgePoint{}
	for _, name := range names {
		ps, err := parseOutlineTable(tables[name])
		if err != nil {
			return nil, fmt.Errorf("%s：%w", name, err)
		}
		points = append(points, ps...)
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("没有找到大纲表格；表头需包含分类、专业/系统、要点、大纲代码，或旧版知识点ID、科目、系统、知识点名称")
	}
	return points, nil
}

var outlineHeaders = map[string]string{
	"分类": "category", "大类": "category", "category": "category",
	"专业/系统": "subject", "专业系统": "subject", "专业": "subject", "系统": "subject", "subject": "subject",
	"科目": "legacy_category",
	"单元": "unit", "章节": "unit", "unit": "unit", "细目": "sub_item", "子单元": "sub_item", "sub_item": "sub_item",
	"要点": "topic", "知识点": "topic", "知识点名称": "topic", "topic": "topic",
	"大纲代码": "outline_code", "知识点代码": "outline_code", "outline_code": "outline_code",
	"知识点id": "legacy_code", "知识点编号": "legacy_code", "id": "legacy_code",
	"关键词": "keywords", "keywords": "keywords",
}

func parseOutlineTable(rows [][]string) ([]domain.KnowledgePoint, error) {
	cols := map[string]int{}
	header := -1
	legacy := false
	for i, row := range rows {
		if i > 20 {
			break
		}
		candidate := map[string]int{}
		for j, cell := range row {
			h := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(cell, "\ufeff")))
			h = strings.ReplaceAll(h, "／", "/")
			if field := outlineHeaders[h]; field != "" {
				candidate[field] = j
			}
		}
		if _, ok := candidate["topic"]; ok {
			_, hasOutlineCode := candidate["outline_code"]
			_, hasLegacyCode := candidate["legacy_code"]
			if hasOutlineCode || hasLegacyCode {
				cols = candidate
				header = i
				legacy = !hasOutlineCode
				break
			}
		}
	}
	if header < 0 {
		for _, row := range rows {
			for _, cell := range row {
				if strings.TrimSpace(cell) != "" {
					return nil, fmt.Errorf("找不到“要点”和“大纲代码”表头")
				}
			}
		}
		return nil, nil
	}
	result := []domain.KnowledgePoint{}
	parents := map[string]string{}
	for i := header + 1; i < len(rows); i++ {
		row := rows[i]
		get := func(key string) string {
			n, ok := cols[key]
			if !ok || n >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[n])
		}
		nonempty := false
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				nonempty = true
				break
			}
		}
		if !nonempty {
			continue
		}
		if legacy {
			code := get("legacy_code")
			category := get("legacy_category")
			subject := get("subject")
			topic := get("topic")
			if topic == "" || code == "" {
				return nil, fmt.Errorf("第%d行：知识点名称和知识点ID不能为空", i+1)
			}
			if category == "" || subject == "" {
				return nil, fmt.Errorf("第%d行：科目和系统不能为空", i+1)
			}
			p := domain.KnowledgePoint{ID: code, Category: category, Subject: subject, Topic: topic, OutlineCode: code}
			if kw := get("keywords"); kw != "" {
				p.Keywords = strings.FieldsFunc(kw, func(r rune) bool { return r == ',' || r == '，' || r == '、' || r == ';' || r == '；' })
			}
			result = append(result, p)
			if len(result) > 50000 {
				return nil, fmt.Errorf("单次导入最多 50000 个知识点")
			}
			continue
		}
		if get("outline_code") == "大纲代码" {
			continue
		} // repeated Word table headings
		// Existing official-outline workbooks include explicit “暂存” rows ending
		// in .0. They reserve a code but carry no examinable knowledge content.
		if get("topic") == "" && strings.HasSuffix(get("outline_code"), ".0") && (get("unit") == "暂存" || get("sub_item") == "暂存") {
			continue
		}
		fields := []string{"category", "subject", "unit", "sub_item"}
		for j, key := range fields {
			if value := get(key); value != "" {
				if value != parents[key] {
					for _, child := range fields[j+1:] {
						parents[child] = ""
					}
				}
				parents[key] = value
			}
		}
		topic, code := get("topic"), get("outline_code")
		if topic == "" || code == "" {
			return nil, fmt.Errorf("第%d行：要点和大纲代码不能为空", i+1)
		}
		if parents["category"] == "" || parents["subject"] == "" {
			return nil, fmt.Errorf("第%d行：分类和专业/系统不能为空", i+1)
		}
		p := domain.KnowledgePoint{ID: code, Category: parents["category"], Subject: parents["subject"], Unit: parents["unit"], SubItem: parents["sub_item"], Topic: topic, OutlineCode: code}
		if kw := get("keywords"); kw != "" {
			p.Keywords = strings.FieldsFunc(kw, func(r rune) bool { return r == ',' || r == '，' || r == '、' || r == ';' || r == '；' })
		}
		result = append(result, p)
		if len(result) > 50000 {
			return nil, fmt.Errorf("单次导入最多 50000 个知识点")
		}
	}
	return result, nil
}

func readWordTables(path string) ([][][]string, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != "word/document.xml" {
			continue
		}
		if f.UncompressedSize64 > 16<<20 {
			return nil, fmt.Errorf("Word 文档正文过大")
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		dec := xml.NewDecoder(io.LimitReader(r, 16<<20))
		tables := [][][]string{}
		var rows [][]string
		var row []string
		var cell strings.Builder
		depth := 0
		inCell := false
		for {
			token, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			switch t := token.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "tbl":
					depth++
					if depth > 1 {
						return nil, fmt.Errorf("不支持嵌套表格，请整理为普通大纲表格")
					}
					rows = nil
				case "tr":
					if depth == 1 {
						row = nil
					}
				case "tc":
					if depth == 1 {
						cell.Reset()
						inCell = true
					}
				case "t":
					if inCell {
						var text string
						if err := dec.DecodeElement(&text, &t); err != nil {
							return nil, err
						}
						cell.WriteString(text)
					}
				case "tab", "br":
					if inCell {
						cell.WriteString(" ")
					}
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "p":
					if inCell && cell.Len() > 0 {
						cell.WriteString(" ")
					}
				case "tc":
					if depth == 1 {
						row = append(row, strings.TrimSpace(cell.String()))
						inCell = false
					}
				case "tr":
					if depth == 1 {
						rows = append(rows, row)
					}
				case "tbl":
					if depth == 1 {
						tables = append(tables, rows)
					}
					depth--
				}
			}
		}
		return tables, nil
	}
	return nil, fmt.Errorf("Word 文件缺少正文")
}
