package generator

import (
	"context"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/llm"
)

// MockClient 模拟 LLM 客户端，用于测试。
type MockClient struct {
	Response string
	Err      error
}

func (m *MockClient) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (string, error) {
	return m.Response, m.Err
}

// TestBuildPrompt 验证 prompt 包含完整知识点信息。
func TestBuildPrompt(t *testing.T) {
	kp := domain.KnowledgePoint{
		ID:          "110.2.6.2.1.1",
		Category:    "基础医学",
		Subject:     "病理",
		Unit:        "二、局部血液循环障碍",
		SubItem:     "1．充血和淤血",
		Topic:       "（1）充血的概念和类型",
		OutlineCode: "110.2.6.2.1.1",
	}

	prompt := buildPrompt(kp, 1, false)

	// 验证 prompt 包含关键信息
	checks := []string{
		"110.2.6.2.1.1",           // 大纲代码
		"基础医学",                 // 分类
		"病理",                     // 专业
		"二、局部血液循环障碍",       // 单元
		"1．充血和淤血",            // 细目
		"（1）充血的概念和类型",     // 要点
	}

	for _, check := range checks {
		if !contains(prompt, check) {
			t.Errorf("prompt 缺少关键信息: %s", check)
		}
	}

	t.Log("✓ prompt 包含完整知识点信息")
}

// TestGenerateWithMock 验证生成流程正确填充所有字段。
func TestGenerateWithMock(t *testing.T) {
	// 模拟 LLM 返回
	mockResponse := `[{
		"clinical_stem": "男，45岁。反复上腹痛2年，加重1天。查体：腹软，上腹部压痛。该患者最可能的诊断是",
		"options": [
			{"label": "A", "text": "胃溃疡"},
			{"label": "B", "text": "十二指肠溃疡"},
			{"label": "C", "text": "胃癌"},
			{"label": "D", "text": "慢性胃炎"},
			{"label": "E", "text": "功能性消化不良"}
		],
		"answer": "A",
		"explanation": "该患者为中年男性，反复上腹痛2年，具有典型的胃溃疡临床特点。",
		"difficulty": "0.65",
		"cognitive_level": "简单应用",
		"exam_points": "诊断与鉴别诊断，临床表现"
	}]`

	client := &MockClient{Response: mockResponse}
	svc := NewService(client)

	kp := domain.KnowledgePoint{
		ID:          "110.4.3.1.1",
		Category:    "临床综合",
		Subject:     "消化",
		Unit:        "三、消化系统",
		SubItem:     "1．消化性溃疡",
		Topic:       "消化性溃疡的诊断与治疗",
		OutlineCode: "110.4.3.1.1",
	}

	req := domain.GenerationRequest{
		Subject:         "消化",
		Difficulty:      "0.65",
		KnowledgePoints: []domain.KnowledgePoint{kp},
		Count:           1,
	}

	questions, err := svc.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	if len(questions) != 1 {
		t.Fatalf("期望 1 道题，实际 %d 道", len(questions))
	}

	q := questions[0]

	// 验证所有字段
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"大纲代码", q.OutlineCode, "110.4.3.1.1"},
		{"专业", q.Profession, "消化"},
		{"系统", q.System, "临床综合"},
		{"答案", q.Answer, "A"},
		{"难度", string(q.Difficulty), "0.65"},
		{"认知层次", q.CognitiveLevel, "简单应用"},
		{"考核要点", q.ExamPoints, "诊断与鉴别诊断，临床表现"},
	}

	for _, tt := range tests {
		if tt.got != tt.expected {
			t.Errorf("%s: 期望 %q, 实际 %q", tt.name, tt.expected, tt.got)
		}
	}

	// 验证选项
	if len(q.Options) != 5 {
		t.Errorf("选项数: 期望 5, 实际 %d", len(q.Options))
	}

	// 验证知识点关联
	if len(q.KnowledgePoints) != 1 {
		t.Errorf("知识点数: 期望 1, 实际 %d", len(q.KnowledgePoints))
	}

	if q.KnowledgePoints[0].OutlineCode != "110.4.3.1.1" {
		t.Errorf("知识点大纲代码: 期望 110.4.3.1.1, 实际 %s", q.KnowledgePoints[0].OutlineCode)
	}

	// 验证题干
	if q.ClinicalStem == "" {
		t.Error("题干为空")
	}

	// 验证解析
	if q.Explanation == "" {
		t.Error("解析为空")
	}

	t.Log("✓ 所有字段正确填充")
	t.Logf("  大纲代码: %s", q.OutlineCode)
	t.Logf("  专业: %s", q.Profession)
	t.Logf("  系统: %s", q.System)
	t.Logf("  难度: %s", q.Difficulty)
	t.Logf("  认知层次: %s", q.CognitiveLevel)
	t.Logf("  考核要点: %s", q.ExamPoints)
	t.Logf("  选项数: %d", len(q.Options))
	t.Logf("  题干长度: %d 字", len(q.ClinicalStem))
	t.Logf("  解析长度: %d 字", len(q.Explanation))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
