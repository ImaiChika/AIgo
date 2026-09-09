package pipeline

import (
	"context"
	"sync"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage/testutil"
)

// fakeLLM 返回固定的单题生成结果。
type fakeLLM struct{}

func (f *fakeLLM) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	return `[{
		"clinical_stem": "男，45岁。反复上腹痛2年，加重1天。查体：T 36.8℃，P 80次/分，R 18次/分，BP 120/80mmHg，腹软，上腹部压痛。该患者最可能的诊断是",
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
	}]`, nil
}

// recordingChecker 记录被触发的自动检查题目 ID。
type recordingChecker struct {
	mu  sync.Mutex
	ids []string
}

func (c *recordingChecker) CheckAsync(ids ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, ids...)
}

func (c *recordingChecker) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

// 生成落库后应自动触发 AI 检查（DraftChecker 接口）。
func TestGenerateTriggersDraftCheck(t *testing.T) {
	store := testutil.NewMemoryStore()
	checker := &recordingChecker{}
	p := New(generator.NewService(&fakeLLM{}), evaluator.NewService(), nil, nil, store)
	p.SetDraftChecker(checker)

	questions, err := p.Generate(context.Background(), domain.GenerationRequest{
		Subject: "消化",
		KnowledgePoints: []domain.KnowledgePoint{{
			ID:          "110.4.3.1.1",
			Subject:     "消化",
			Topic:       "消化性溃疡的诊断与治疗",
			OutlineCode: "110.4.3.1.1",
		}},
		Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	triggered := checker.recorded()
	if len(triggered) != len(questions) {
		t.Fatalf("应触发 %d 次检查，实际 %d", len(questions), len(triggered))
	}
	for i, q := range questions {
		if triggered[i] != q.ID {
			t.Fatalf("检查应覆盖生成的题目 %s，实际 %s", q.ID, triggered[i])
		}
	}

	// 未注入 checker 时不应 panic（CLI 路径）
	p2 := New(generator.NewService(&fakeLLM{}), evaluator.NewService(), nil, nil, store)
	if _, err := p2.Generate(context.Background(), domain.GenerationRequest{
		Subject: "消化",
		KnowledgePoints: []domain.KnowledgePoint{{
			ID:          "110.4.3.1.1",
			Subject:     "消化",
			Topic:       "消化性溃疡的诊断与治疗",
			OutlineCode: "110.4.3.1.1",
		}},
		Count: 1,
	}); err != nil {
		t.Fatalf("未注入 checker 时生成应正常: %v", err)
	}
}
