package importer

import (
	"testing"
)

func TestReadXlsx(t *testing.T) {
	rows, err := ReadXlsx("../../2025执医a2.xlsx", true)
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

func TestConvertToQuestions(t *testing.T) {
	rows, err := ReadXlsx("../../2025执医a2.xlsx", true)
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
