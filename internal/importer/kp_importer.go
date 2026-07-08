package importer

import (
	"fmt"
	"strings"

	"aigo/internal/domain"

	"github.com/xuri/excelize/v2"
)

// KPRow 知识点 xlsx 的一行原始数据。
type KPRow struct {
	ID       string
	Subject  string
	System   string
	Name     string
	Keywords string
}

// ReadKnowledgePoints 从 xlsx 读取知识点。
// 表头：知识点ID | 科目 | 系统 | 知识点名称 | 关键词
func ReadKnowledgePoints(path string) ([]KPRow, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	sheetName := f.GetSheetName(0)
	rows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("读取工作表失败: %w", err)
	}

	if len(rows) < 2 {
		return nil, fmt.Errorf("文件为空或只有表头")
	}

	var result []KPRow
	for i := 1; i < len(rows); i++ { // 跳过表头
		row := rows[i]
		if len(row) < 4 {
			continue
		}
		kw := ""
		if len(row) > 4 {
			kw = strings.TrimSpace(row[4])
		}
		result = append(result, KPRow{
			ID:       strings.TrimSpace(row[0]),
			Subject:  strings.TrimSpace(row[1]),
			System:   strings.TrimSpace(row[2]),
			Name:     strings.TrimSpace(row[3]),
			Keywords: kw,
		})
	}
	return result, nil
}

// ConvertToKnowledgePoints 将原始行转为 KnowledgePoint。
func ConvertToKnowledgePoints(rows []KPRow) []domain.KnowledgePoint {
	var points []domain.KnowledgePoint
	for _, row := range rows {
		if row.ID == "" || row.Name == "" || row.Name == "待归类" {
			continue
		}
		var keywords []string
		if row.Keywords != "" {
			for _, kw := range strings.Split(row.Keywords, "、") {
				kw = strings.TrimSpace(kw)
				if kw != "" {
					keywords = append(keywords, kw)
				}
			}
		}
		points = append(points, domain.KnowledgePoint{
			ID:       row.ID,
			Subject:  row.Subject,
			System:   row.System,
			Topic:    row.Name,
			Keywords: keywords,
		})
	}
	return points
}
