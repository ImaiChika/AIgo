// Package importer 提供 Excel 数据导入功能。
// 支持从 xlsx 文件读取真题、知识点和考试大纲数据。
package importer

import (
	"fmt"
	"strings"

	"aigo/internal/domain"

	"github.com/xuri/excelize/v2"
)

// ExcelRow 对应真题 xlsx 中的一行原始数据。
type ExcelRow struct {
	ID          string // 题目 ID
	Stem        string // 题干
	Answer      string // 正确答案
	Explanation string // 解析
	OptionA     string // 选项 A
	OptionB     string // 选项 B
	OptionC     string // 选项 C
	OptionD     string // 选项 D
	OptionE     string // 选项 E
}

// ReadXlsx 读取真题 xlsx 文件，返回原始行数据。
// skipHeader 为 true 时跳过第一行表头。
func ReadXlsx(path string, skipHeader bool) ([]ExcelRow, error) {
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

	var result []ExcelRow
	start := 0
	if skipHeader {
		start = 1
	}

	for i := start; i < len(rows); i++ {
		row := rows[i]
		if len(row) < 9 {
			continue // 列数不足则跳过
		}
		result = append(result, ExcelRow{
			ID:          strings.TrimSpace(row[0]),
			Stem:        strings.TrimSpace(row[1]),
			Answer:      strings.TrimSpace(row[2]),
			Explanation: strings.TrimSpace(row[3]),
			OptionA:     strings.TrimSpace(row[4]),
			OptionB:     strings.TrimSpace(row[5]),
			OptionC:     strings.TrimSpace(row[6]),
			OptionD:     strings.TrimSpace(row[7]),
			OptionE:     strings.TrimSpace(row[8]),
		})
	}
	return result, nil
}

// ConvertToQuestions 将原始行数据转换为 A2Question 切片。
// 跳过题干为空或选项不足 4 个的行。
func ConvertToQuestions(rows []ExcelRow) ([]domain.A2Question, []error) {
	var questions []domain.A2Question
	var errs []error

	for _, row := range rows {
		if strings.TrimSpace(row.Stem) == "" {
			errs = append(errs, fmt.Errorf("id=%s: 题干为空，跳过", row.ID))
			continue
		}

		// 构建选项列表（只保留非空选项）
		optionTexts := []string{row.OptionA, row.OptionB, row.OptionC, row.OptionD, row.OptionE}
		labels := []string{"A", "B", "C", "D", "E"}
		var options []domain.Option
		for j, text := range optionTexts {
			if text != "" {
				options = append(options, domain.Option{
					Label: labels[j],
					Text:  text,
				})
			}
		}
		if len(options) < 4 {
			errs = append(errs, fmt.Errorf("id=%s: 选项不足4个(实际%d个)，跳过", row.ID, len(options)))
			continue
		}

		// 校验答案标签是否在选项中
		answerValid := false
		for _, opt := range options {
			if opt.Label == row.Answer {
				answerValid = true
				break
			}
		}
		if !answerValid {
			errs = append(errs, fmt.Errorf("id=%s: 答案标签 %q 不在选项中，跳过", row.ID, row.Answer))
			continue
		}

		q := domain.A2Question{
			ID:           row.ID,
			ClinicalStem: row.Stem,
			Options:      options,
			Answer:       row.Answer,
			Explanation:  row.Explanation,
			KnowledgePoints: []domain.KnowledgePoint{{
				ID:       "imported",
				Category: "临床医学",
				Subject:  "执业医师A2型题",
			}},
			Difficulty: domain.DifficultyMedium,
			Status:     domain.StatusAIDraft,
			Version:    1,
		}
		questions = append(questions, q)
	}

	return questions, errs
}

// ReadExamOutline 读取 2024 年临床医师考试大纲 xlsx 文件。
// 支持读取所有 Sheet，返回知识点切片。
// 列结构：序号、分类、专业/系统、单元、细目、要点、大纲代码
func ReadExamOutline(path string) ([]domain.KnowledgePoint, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	var points []domain.KnowledgePoint

	// 遍历所有 Sheet（如 110基础、110临床）
	for _, name := range f.GetSheetList() {
		rows, err := f.GetRows(name)
		if err != nil {
			continue
		}

		for i, row := range rows {
			if i == 0 {
				continue // 跳过表头
			}
			if len(row) < 7 {
				continue // 列数不足则跳过
			}

			// 列：序号(0)、分类(1)、专业/系统(2)、单元(3)、细目(4)、要点(5)、大纲代码(6)
			outlineCode := strings.TrimSpace(row[6])
			if outlineCode == "" {
				continue // 没有大纲代码则跳过
			}

			p := domain.KnowledgePoint{
				ID:          outlineCode,       // 用大纲代码作为唯一 ID
				Category:    strings.TrimSpace(row[1]), // 分类：基础医学/临床综合
				Subject:     strings.TrimSpace(row[2]), // 专业/系统
				Unit:        strings.TrimSpace(row[3]), // 单元
				SubItem:     strings.TrimSpace(row[4]), // 细目
				Topic:       strings.TrimSpace(row[5]), // 要点
				OutlineCode: outlineCode,
			}
			points = append(points, p)
		}
	}

	return points, nil
}

// ExportToXlsx 将题目列表导出为 xlsx 文件。
// 按照试题命制要求的格式输出。
// 格式规范：宋体五号，西文用 Times New Roman，序号和选项用全角点。
func ExportToXlsx(questions []domain.A2Question, path string) error {
	f := excelize.NewFile()
	sheet := "题目"
	f.NewSheet(sheet)
	f.DeleteSheet("Sheet1")

	// 设置列宽
	f.SetColWidth(sheet, "A", "A", 6)   // 序号
	f.SetColWidth(sheet, "B", "B", 60)  // 题干
	f.SetColWidth(sheet, "C", "G", 20)  // 选项
	f.SetColWidth(sheet, "H", "H", 6)   // 答案
	f.SetColWidth(sheet, "I", "I", 50)  // 解析
	f.SetColWidth(sheet, "J", "J", 16)  // 大纲代码
	f.SetColWidth(sheet, "K", "K", 10)  // 预估难度
	f.SetColWidth(sheet, "L", "L", 12)  // 认知层次
	f.SetColWidth(sheet, "M", "M", 30)  // 考核要点
	f.SetColWidth(sheet, "N", "N", 10)  // 专业
	f.SetColWidth(sheet, "O", "O", 10)  // 系统

	// 表头样式
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 10.5, Family: "宋体"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#D5E8F0"}, Pattern: 1},
	})
	_ = headerStyle

	// 写表头
	headers := []string{
		"序号", "题干", "选项A", "选项B", "选项C", "选项D", "选项E",
		"答案", "解析", "大纲代码", "预估难度", "认知层次", "考核要点", "专业", "系统",
	}
	for i, h := range headers {
		cell := cellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
		f.SetCellStyle(sheet, cell, cell, headerStyle)
	}

	// 写数据行
	for idx, q := range questions {
		row := idx + 2 // 从第 2 行开始
		opts := make(map[string]string)
		for _, o := range q.Options {
			opts[o.Label] = o.Text
		}

		f.SetCellValue(sheet, cellName(1, row), idx+1)              // 序号
		f.SetCellValue(sheet, cellName(2, row), q.ClinicalStem)     // 题干
		f.SetCellValue(sheet, cellName(3, row), opts["A"])          // 选项A
		f.SetCellValue(sheet, cellName(4, row), opts["B"])          // 选项B
		f.SetCellValue(sheet, cellName(5, row), opts["C"])          // 选项C
		f.SetCellValue(sheet, cellName(6, row), opts["D"])          // 选项D
		f.SetCellValue(sheet, cellName(7, row), opts["E"])          // 选项E
		f.SetCellValue(sheet, cellName(8, row), q.Answer)           // 答案
		f.SetCellValue(sheet, cellName(9, row), q.Explanation)      // 解析
		f.SetCellValue(sheet, cellName(10, row), q.OutlineCode)     // 大纲代码
		f.SetCellValue(sheet, cellName(11, row), q.Difficulty)      // 预估难度
		f.SetCellValue(sheet, cellName(12, row), q.CognitiveLevel)  // 认知层次
		f.SetCellValue(sheet, cellName(13, row), q.ExamPoints)      // 考核要点
		f.SetCellValue(sheet, cellName(14, row), q.Profession)      // 专业
		f.SetCellValue(sheet, cellName(15, row), q.System)          // 系统
	}

	return f.SaveAs(path)
}

// cellName 辅助函数：根据列号和行号生成 Excel 单元格名称（如 A1、B2）。
func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}
