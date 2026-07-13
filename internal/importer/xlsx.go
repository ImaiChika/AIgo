// Package importer 提供 Excel 数据导入功能。
// 支持从 xlsx 文件读取真题和知识点数据。
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
				ID:      "imported",
				Subject: "临床医学",
				Topic:   "执业医师A2型题",
			}},
			Difficulty: domain.DifficultyMedium,
			Status:     domain.StatusAIDraft,
			Version:    1,
		}
		questions = append(questions, q)
	}

	return questions, errs
}
