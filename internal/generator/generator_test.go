package generator

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/llm"
)

// MockClient 模拟 LLM 客户端，用于测试。
type MockClient struct {
	Response     string
	Err          error
	LastOptions  llm.GenerateOptions
	LastMessages []llm.Message
}

func (m *MockClient) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (string, error) {
	m.LastOptions = opts
	m.LastMessages = append([]llm.Message(nil), messages...)
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
		"110.2.6.2.1.1", // 大纲代码
		"基础医学",          // 分类
		"病理",            // 专业
		"二、局部血液循环障碍",    // 单元
		"1．充血和淤血",       // 细目
		"（1）充血的概念和类型",   // 要点
		"从受控词表选择",       // 专家考核要点约束
	}

	for _, check := range checks {
		if !contains(prompt, check) {
			t.Errorf("prompt 缺少关键信息: %s", check)
		}
	}

	t.Log("✓ prompt 包含完整知识点信息")
}

func TestSystemPromptContainsExpertA2Rules(t *testing.T) {
	prompt := getSystemPrompt(false)
	for _, want := range []string{
		"成人以“患者，男/女，年龄。”开头",
		"直接叙述病例，不出现",
		"不使用“哪个”“什么”",
		"不使用“哪个”“什么”或问号",
		"逐项解释其余四个干扰项",
		"故选 X",
		"具体处置措施",
		"exam_points 不得填写具体疾病名称",
		"cognitive_level 只能是“记忆、理解、应用、综合应用”之一",
		"不要输出历史旧标签“简单应用”",
		"合格示例（仅学习格式与说明写法",
		"调整膳食结构并增加适宜运动",
		"十二指肠溃疡并出血",
	} {
		if !contains(prompt, want) {
			t.Errorf("专家A2系统提示词缺少规则: %s", want)
		}
	}
}

// TestGenerateWithMock 验证生成流程正确填充所有字段。
func TestGenerateWithMock(t *testing.T) {
	// 模拟 LLM 返回
	mockResponse := `[{
		"clinical_stem": "男，45岁。反复上腹痛2年，加重1天。T 36.8℃，P 80次/分，R 18次/分，BP 120/80mmHg，腹软，上腹部压痛。该患者最可能的诊断是",
		"options": [
			{"label": "A", "text": "胃溃疡"},
			{"label": "B", "text": "十二指肠溃疡"},
			{"label": "C", "text": "胃癌"},
			{"label": "D", "text": "慢性胃炎"},
			{"label": "E", "text": "功能性消化不良"}
		],
		"answer": "A",
		"explanation": "正确答案为 A。该患者反复上腹痛，结合疼痛特点考虑胃溃疡。B 项疼痛规律更符合十二指肠溃疡；C 项缺乏消瘦等警示表现；D 项不能完整解释典型节律性疼痛；E 项应在排除器质性疾病后考虑。故选 A。",
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
		{"系统", q.System, "消化"},
		{"答案", q.Answer, "A"},
		{"难度", string(q.Difficulty), "0.65"},
		{"认知层次", q.CognitiveLevel, "应用"},
		{"考核要点", q.ExamPoints, "诊断与鉴别诊断，临床表现"},
		{"格式协议", q.FormatVersion, domain.A2FormatSecondWave},
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
	if !strings.HasPrefix(q.ClinicalStem, "患者，男，45岁。") {
		t.Fatalf("新题未使用患者开头: %s", q.ClinicalStem)
	}
	if len(client.LastMessages) != 2 || !strings.Contains(client.LastMessages[1].Content, "目标预估难度：0.65") {
		t.Fatal("本次目标难度没有进入生成请求")
	}

	// 验证解析
	if q.Explanation == "" {
		t.Error("解析为空")
	}
	if client.LastOptions.MaxTokens != 0 {
		t.Errorf("生成请求不应设置token上限，实际 MaxTokens=%d", client.LastOptions.MaxTokens)
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

func TestPromptExamplesMeetSecondWaveFormat(t *testing.T) {
	examples := regexp.MustCompile(`(?m)^\[\{.*\}\]$`).FindAllString(getSystemPrompt(false), -1)
	if len(examples) != 2 {
		t.Fatalf("expected two examples, got %d", len(examples))
	}
	for _, raw := range examples {
		var questions []domain.A2Question
		if err := json.Unmarshal([]byte(raw), &questions); err != nil {
			t.Fatal(err)
		}
		for _, q := range questions {
			q.FormatVersion, q.OutlineCode, q.Profession, q.System = domain.A2FormatSecondWave, "example", "示例专业", "示例系统"
			if err := q.ValidateGeneratedA2(); err != nil {
				t.Fatalf("示例不合规: %s: %v", q.ClinicalStem, err)
			}
		}
	}
}

func TestRequestedDifficultyMatchesExistingProjectBands(t *testing.T) {
	for _, tt := range []struct {
		name      string
		requested domain.Difficulty
		want      string
	}{
		{"easy", domain.DifficultyEasy, "0.55"},
		{"medium", domain.DifficultyMedium, "0.65"},
		{"hard", domain.DifficultyHard, "0.85"},
		{"numeric", "0.75", "0.75"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if prompt := buildPrompt(domain.KnowledgePoint{}, 1, false, tt.requested); !strings.Contains(prompt, "目标预估难度："+tt.want) {
				t.Fatalf("目标难度没有匹配现有界面/题库分组: %s", prompt)
			}
		})
	}
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
