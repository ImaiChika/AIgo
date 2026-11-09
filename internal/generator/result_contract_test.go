package generator

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"aigo/internal/domain"
)

// Requested cardinality is a task contract. Invalid members cannot silently
// disappear while the task is reported as successfully completed.
func TestDirectGenerationRequiresAllRequestedQuestions(t *testing.T) {
	_, item := workflowFixture()
	delete(item, "knowledge_basis")
	for _, tc := range []struct {
		name  string
		count int
		items []any
	}{
		{"too few", 2, []any{item}},
		{"too many", 1, []any{item, item}},
		{"empty result", 1, []any{}},
		{"null member", 2, []any{item, nil}},
		{"invalid member", 2, []any{item, map[string]any{"clinical_stem": "损坏题目", "answer": "Z"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.items)
			client := &workflowTestClient{response: string(raw)}
			request, _ := workflowFixture()
			request.Workflow = nil
			request.Count = tc.count
			questions, err := NewService(client).Generate(t.Context(), request)
			if err == nil || len(questions) != 0 || client.calls != 1 {
				t.Fatal("partial or extra result accepted", len(questions), err, client.calls)
			}
		})
	}
}

func TestRetryReminderPreservesFrozenInputsAndFirstAttempt(t *testing.T) {
	req, item := clinicalWorkflowFixture()
	raw, _ := json.Marshal([]any{item})
	before, _ := json.Marshal(req)
	client := &workflowTestClient{response: string(raw)}
	svc := NewService(client)
	if _, err := svc.Generate(WithGenerationAttempt(t.Context(), 1), req); err != nil {
		t.Fatal(err)
	}
	first := client.messages[1].Content
	if strings.Contains(first, "【原任务自动重试") {
		t.Fatal("first attempt changed")
	}
	if _, err := svc.Generate(WithGenerationAttempt(t.Context(), 2), req); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(client.messages[1].Content, first) || !strings.Contains(client.messages[1].Content, "第2次尝试") {
		t.Fatal("retry replaced frozen input")
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) || client.calls != 2 {
		t.Fatal("snapshot mutation or hidden call")
	}
}

func TestGenerationFailureLogKeepsPayloadPrivate(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(previous)
	req, _ := workflowFixture()
	req.Workflow = nil
	_, err := NewService(&workflowTestClient{response: `[{"private":"secret-patient-marker"]`}).Generate(t.Context(), req)
	if err == nil {
		t.Fatal("broken JSON accepted")
	}
	if !strings.Contains(buf.String(), "generation.response_invalid") || !strings.Contains(buf.String(), "response_sha256") || strings.Contains(buf.String(), "secret-patient-marker") {
		t.Fatal("unsafe/missing failure diagnostics", buf.String())
	}
}

func TestKnowledgeFailureLogIdentifiesContractWithoutPrivateText(t *testing.T) {
	for _, tc := range []struct{ name, reason, code string }{
		{"answer reference", "知识工作流结构校验失败: 正确选项须引用目标卡的医学事实", "answer_target_reference_missing"},
		{"unselected reference", "知识工作流结构校验失败: 选项知识引用不属于本题已选事实", "option_fact_reference_invalid"},
		{"case reference", "知识工作流结构校验失败: 选项引用不存在或重复的病例事实", "option_case_reference_invalid"},
		{"unknown reference", "知识工作流结构校验失败: 事实编号不存在或重复", "basis_fact_reference_invalid"},
		{"unexpected private error", "知识工作流结构校验失败: secret-patient-marker", "unspecified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
			defer slog.SetDefault(previous)
			logInvalidGenerationResponse(t.Context(), "secret-patient-marker", 1, "knowledge_basis", errors.New(tc.reason))
			var row map[string]any
			if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			if row["reason_code"] != "knowledge_basis" || row["contract_reason"] != tc.code || strings.Contains(buf.String(), "secret-patient-marker") {
				t.Fatal("missing or unsafe contract diagnostic", buf.String())
			}
		})
	}
}

func TestKnowledgeReferenceFailureStillRejectsAndLogsSpecificCause(t *testing.T) {
	req, item := aliasFixture(t)
	basis := item["knowledge_basis"].(domain.KnowledgeBasis)
	basis.FactRefs = []string{"k1"}
	basis.CasePlan.OptionEvidence[0].FactRefs = nil
	item["knowledge_basis"] = basis
	raw, _ := json.Marshal([]any{item})
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(previous)
	client := &workflowTestClient{response: string(raw)}
	questions, err := NewService(client).Generate(t.Context(), req)
	if err == nil || len(questions) != 0 || client.calls != 1 {
		t.Fatal("missing evidence accepted or hidden retry", questions, err, client.calls)
	}
	if !strings.Contains(buf.String(), `"contract_reason":"answer_target_reference_missing"`) || strings.Contains(buf.String(), basis.AnswerSupport) {
		t.Fatal("validator cause missing or private evidence logged", buf.String())
	}
}

func TestGenerationRejectsAmbiguousJSONFields(t *testing.T) {
	for _, raw := range []string{
		`[{"answer":"A","answer":"B"}]`,
		`[{"answer":"A","Answer":"B"}]`,
		`[{"options":[{"label":"A","text":"one","text":"two"}]}]`,
	} {
		if _, err := parseQuestions(raw); err == nil {
			t.Fatal("duplicate JSON fields accepted")
		}
	}
}

func TestGenerationArrayWrapperKeepsWholeStrictPayload(t *testing.T) {
	valid := `[{"clinical_stem":"括号[仅为文字]，引号\"正常\"","answer":"A"}]`
	for name, raw := range map[string]string{
		"plain": valid,
		"prose": "以下是生成的题目：\n" + valid + "\n以上为本次结果。",
		"fence": "```json\n" + valid + "\n```",
	} {
		t.Run(name, func(t *testing.T) {
			items, err := parseQuestions(raw)
			if err != nil || len(items) != 1 || items[0]["answer"] != "A" {
				t.Fatal("complete wrapped payload rejected", items, err)
			}
		})
	}
	for name, raw := range map[string]string{
		"duplicate":                "题目：" + `[{"answer":"A","answer":"B"}]`,
		"object wrapper":           `{"questions":` + valid + `}`,
		"truncated object wrapper": `{"questions":` + valid,
		"extra member":             valid + `,{"answer":"B"}`,
		"extra array":              valid + valid,
		"extra scalar":             valid + ` true`,
		"trailing comma":           valid + `,`,
		"fence with extra object":  "```json\n" + valid + "\n```\n" + `{"answer":"B"}`,
		"fence in object wrapper":  "{\"questions\":```json\n" + valid + "\n```}",
		"broken member":            `[{"answer":"A"},{"answer":]`,
		"missing array end":        `[{"answer":"A"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseQuestions(raw); err == nil {
				t.Fatal("ambiguous or partial payload accepted", raw)
			}
		})
	}
}

func TestDirectGenerationReturnsExactValidArray(t *testing.T) {
	req, item := workflowFixture()
	req.Workflow = nil
	req.Count = 2
	other := map[string]any{}
	for k, v := range item {
		other[k] = v
	}
	other["clinical_stem"] = "患者，男，46岁。反复上腹痛3年，加重2天。该患者最可能的诊断是"
	delete(item, "knowledge_basis")
	delete(other, "knowledge_basis")
	raw, _ := json.Marshal([]any{item, other})
	questions, err := NewService(&workflowTestClient{response: string(raw)}).Generate(t.Context(), req)
	if err != nil || len(questions) != 2 || questions[0].Status != domain.StatusAIDraft {
		t.Fatal(questions, err)
	}
}
