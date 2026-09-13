package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aigo/internal/audit"

	"aigo/internal/bank"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/knowledge"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
)

// generationHandlerTestServer 在 authHandlerTestServer 基础上补齐命题链路依赖
// （pipeline + fake LLM + 题库/审计），用于验证异步提交 → worker 执行 → 轮询恢复。
func generationHandlerTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	server, store, cleanup := authHandlerTestServer(t)
	pipe := pipeline.New(generator.NewService(&fakeAPILLM{}), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	pipe.SetBankAssigner(bank.NewService(store, store))
	pipe.SetGenerationAuditor(audit.NewService(store))
	server.pipe = pipe
	server.bankSvc = bank.NewService(store, store)
	server.auditSvc = audit.NewService(store)
	server.kpSvc = knowledge.NewService(store)
	server.generationRunStore = store
	return server, cleanup
}

// fakeAPILLM 返回固定单题生成结果（与 pipeline 测试的 fakeLLM 内容一致）。
type fakeAPILLM struct{}

func (f *fakeAPILLM) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
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
		"explanation": "正确答案为 A。该患者反复上腹痛，结合疼痛特点考虑胃溃疡。B 项疼痛规律更符合十二指肠溃疡；C 项缺乏消瘦等警示表现；D 项缺乏消瘦等警示表现；E 项应在排除器质性疾病后考虑。故选 A。",
		"difficulty": "0.65",
		"cognitive_level": "简单应用",
		"exam_points": "诊断与鉴别诊断，临床表现"
	}]`, nil
}

func serveGenerateJSON(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return serveAuthJSON(t, handler, method, path, token, "198.51.100.7", body)
}

// TestGenerateAsyncSubmitAndWorkerCompletion 验证：
// 1) 提交命题立即返回 pending 运行且不含题目；
// 2) 同一 run_id 幂等返回已有运行；
// 3) worker 执行后 GET 运行返回 succeeded 与已落库题目；
// 4) 题目以 ai_draft 草稿保存。
func TestGenerateAsyncSubmitAndWorkerCompletion(t *testing.T) {
	server, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	token := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.7")

	// 准备已发布大纲版本与知识点（与真实出题路径一致）
	ctxSeed := context.Background()
	version, err := server.kpSvc.CreateVersion(ctxSeed, "测试大纲", 2026, "")
	if err != nil {
		t.Fatal(err)
	}
	kp, err := server.kpSvc.CreatePoint(ctxSeed, domain.KnowledgePoint{
		VersionID: version.ID, Category: "临床综合", Subject: "消化", Unit: "胃疾病", SubItem: "溃疡",
		Topic: "消化性溃疡", OutlineCode: "110.4.3.1.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.kpSvc.PublishVersion(ctxSeed, version.ID); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{
		"run_id":             "gen-api-async-0001",
		"subject":            "消化",
		"topic":              "消化性溃疡",
		"outline_code":       kp.OutlineCode,
		"knowledge_point_id": kp.ID,
		"version_id":         version.ID,
		"count":              1,
	}
	resp := serveGenerateJSON(t, handler, http.MethodPost, "/api/questions/generate", token, payload)
	if resp.Code != http.StatusOK {
		t.Fatalf("提交命题应返回 200，实际 %d body=%s", resp.Code, resp.Body.String())
	}
	var submitted struct {
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &submitted); err != nil {
		t.Fatalf("解析提交响应失败: %v body=%s", err, resp.Body.String())
	}
	if submitted.Run.ID != "gen-api-async-0001" || submitted.Run.Status != "pending" || submitted.Count != 0 {
		t.Fatalf("提交应返回 pending 运行且无题目: %+v", submitted)
	}

	// 幂等：同 run_id 重复提交返回同一运行，不重复执行
	dup := serveGenerateJSON(t, handler, http.MethodPost, "/api/questions/generate", token, payload)
	if dup.Code != http.StatusOK || !strings.Contains(dup.Body.String(), `"gen-api-async-0001"`) {
		t.Fatalf("重复提交应返回同一运行: status=%d body=%s", dup.Code, dup.Body.String())
	}

	// worker 执行至终态
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.pipe.StartGenerationWorkers(ctx, 1)

	deadline := time.Now().Add(15 * time.Second)
	var final struct {
		Run struct {
			ID     string   `json:"id"`
			Status string   `json:"status"`
			Error  string   `json:"error"`
			QIDs   []string `json:"question_ids"`
		} `json:"run"`
		Questions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"questions"`
	}
	for {
		r := serveGenerateJSON(t, handler, http.MethodGet, "/api/generation-runs/gen-api-async-0001", token, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("查询运行失败: status=%d body=%s", r.Code, r.Body.String())
		}
		if err := json.Unmarshal(r.Body.Bytes(), &final); err != nil {
			t.Fatalf("解析运行响应失败: %v", err)
		}
		if final.Run.Status == "succeeded" || final.Run.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待运行终态超时: %+v", final)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final.Run.Status != "succeeded" {
		t.Fatalf("worker 执行应成功，实际 %s（%s）", final.Run.Status, final.Run.Error)
	}
	if len(final.Questions) != 1 || len(final.Run.QIDs) != 1 {
		t.Fatalf("运行应包含 1 道题: run=%+v", final.Run)
	}
	q := final.Questions[0]
	if q.ID != final.Run.QIDs[0] || q.Status != "ai_draft" {
		t.Fatalf("题目应以 ai_draft 草稿保存: %+v", q)
	}
}
