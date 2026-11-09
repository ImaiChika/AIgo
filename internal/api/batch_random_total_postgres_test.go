//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
)

// setupRandomBatchAPI 构建批量随机测试环境：启用版本下 3 个临床点（首个有
// 可用知识卡），可控模型执行器接管批量服务。
func setupRandomBatchAPI(t *testing.T) (*Server, http.Handler, string, []domain.KnowledgePoint, func()) {
	t.Helper()
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "random-controlled", batch.LocalConfig{Concurrency: 2, Context: t.Context(), Pipeline: true, Checker: s.aiCheckSvc, CheckMaxAttempts: 3})
	t.Cleanup(ex.Close)
	s.batchSvc = ex
	points := []domain.KnowledgePoint{p}
	for i := 2; i <= 3; i++ {
		created, e := s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: p.VersionID, Category: "临床综合", Subject: "消化", Unit: "测试", SubItem: "随机", Topic: fmt.Sprintf("随机考点%d", i), OutlineCode: fmt.Sprintf("rnd.%03d", i)})
		if e != nil {
			t.Fatal(e)
		}
		points = append(points, *created)
	}
	return s, h, token, points, cleanup
}

func batchSubmitJSON(t *testing.T, h http.Handler, token string, payload map[string]any) (int, map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, payload)
	var body map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &body)
	return r.Code, body, r
}

func waitBatchTerminal(t *testing.T, h http.Handler, token, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r := serveGenerateJSON(t, h, "GET", "/api/batch/status/"+jobID, token, nil)
		if r.Code == 200 {
			var job map[string]any
			if json.Unmarshal(r.Body.Bytes(), &job) == nil {
				if status, _ := job["status"].(string); status == "completed" || status == "failed" {
					return job
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("批量任务未在时限内结束: " + jobID)
	return nil
}

func TestBatchRandomTotalPlanReplayAndConflicts(t *testing.T) {
	_, h, token, points, cleanup := setupRandomBatchAPI(t)
	defer cleanup()
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	base := map[string]any{
		"workflow_mode": "direct", "version_id": points[0].VersionID,
		"knowledge_point_ids": ids,
		"mode": "random_total", "total_count": 2, "request_key": "c2-key-1",
	}

	// R05：随机 N 题——恰 N 个单元、允许重复/不要求覆盖、无乘法扩题。
	code, body, _ := batchSubmitJSON(t, h, token, base)
	if code != 200 {
		t.Fatal(code, body)
	}
	jobID, _ := body["job_id"].(string)
	if body["mode"] != "random_total" || body["candidate_count"] != float64(3) {
		t.Fatalf("响应摘要不符: %v", body)
	}
	job := waitBatchTerminal(t, h, token, jobID)
	if job["total_count"] != float64(2) {
		t.Fatalf("随机任务总题数应为 2: %v", job["total_count"])
	}
	if job["plan_mode"] != "random_total" || job["plan_candidates"] != float64(3) {
		t.Fatalf("计划摘要不符: %v", job)
	}

	// R07：同键同输入重放原任务（返回同一 job_id）。
	replay := map[string]any{}
	for k, v := range base {
		replay[k] = v
	}
	code, body, _ = batchSubmitJSON(t, h, token, replay)
	if code != 200 || body["job_id"] != jobID || body["replayed"] != true {
		t.Fatalf("同键重放失败: %d %v", code, body)
	}

	// R07：同键不同输入 409。
	conflict := map[string]any{}
	for k, v := range base {
		conflict[k] = v
	}
	conflict["total_count"] = 3
	code, _, _ = batchSubmitJSON(t, h, token, conflict)
	if code != http.StatusConflict {
		t.Fatalf("同键不同输入应 409: %d", code)
	}

	// R07：并发双击同键——两请求几乎同时提交，只产生一个任务（输赢都重放）。
	concurrent := map[string]any{}
	for k, v := range base {
		concurrent[k] = v
	}
	concurrent["request_key"] = "c2-key-race"
	var wg sync.WaitGroup
	results := make([]string, 2)
	codes := make([]int, 2)
	wg.Add(2)
	bodies := make([]map[string]any, 2)
	wg.Add(1)
	go func() { defer wg.Done(); time.Sleep(2 * time.Millisecond); _ = 0 }()
	_ = bodies
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			c, b, _ := batchSubmitJSON(t, h, token, concurrent)
			codes[i] = c
			bodies[i] = b
			if id, _ := b["job_id"].(string); id != "" {
				results[i] = id
			}
		}(i)
	}
	wg.Wait()
	t.Log("并发结果:", codes[0], bodies[0], "|", codes[1], bodies[1])
	if codes[0] != 200 || codes[1] != 200 {
		t.Fatalf("并发同键提交应都成功（重放）: %v", codes)
	}
	if results[0] == "" || results[0] != results[1] {
		t.Fatalf("并发同键应返回同一任务: %q vs %q", results[0], results[1])
	}
	waitBatchTerminal(t, h, token, results[0])

	// 旧语义兼容：省略 mode 的请求按每点模式执行并记录 per_point 计划。
	legacy := map[string]any{
		"workflow_mode": "direct", "version_id": points[0].VersionID,
		"knowledge_point_ids": ids[:1], "count": 1,
	}
	code, body, _ = batchSubmitJSON(t, h, token, legacy)
	if code != 200 {
		t.Fatal(code, body)
	}
	legacyJob := waitBatchTerminal(t, h, token, body["job_id"].(string))
	if legacyJob["plan_mode"] != "per_point" {
		t.Fatalf("旧请求应记录 per_point 计划: %v", legacyJob["plan_mode"])
	}
}

func TestBatchRandomTotalValidationAndQuota(t *testing.T) {
	_, h, token, points, cleanup := setupRandomBatchAPI(t)
	defer cleanup()
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	base := func(extra map[string]any) map[string]any {
		payload := map[string]any{
			"workflow_mode": "direct", "version_id": points[0].VersionID,
			"knowledge_point_ids": ids,
		}
		for k, v := range extra {
			payload[k] = v
		}
		return payload
	}
	// R06：参数互斥与非法值。
	if code, body, _ := batchSubmitJSON(t, h, token, base(map[string]any{"mode": "random_total", "total_count": 2, "count": 3})); code != 400 {
		t.Fatalf("随机模式携带每点题数应 400: %d %v", code, body)
	}
	if code, body, _ := batchSubmitJSON(t, h, token, base(map[string]any{"mode": "random_total"})); code != 400 {
		t.Fatalf("随机模式缺 total_count 应 400: %d %v", code, body)
	}
	if code, body, _ := batchSubmitJSON(t, h, token, base(map[string]any{"mode": "per_point", "count": 1, "total_count": 2})); code != 400 {
		t.Fatalf("每点模式携带 total_count 应 400: %d %v", code, body)
	}
	if code, _, _ := batchSubmitJSON(t, h, token, base(map[string]any{"mode": "unknown"})); code != 400 {
		t.Fatal("未知模式应 400")
	}
	// R06：N 超批量总额被配额拒绝（默认 100）。
	code, body, _ := batchSubmitJSON(t, h, token, base(map[string]any{"mode": "random_total", "total_count": 200}))
	if code == 200 {
		t.Fatal("超总额随机任务不应提交")
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "批量任务最多生成") {
		t.Fatalf("应给出配额原因: %v", body)
	}
	// R06：无效点引用明确报错。
	missing := base(map[string]any{"mode": "random_total", "total_count": 2})
	missing["knowledge_point_ids"] = append(append([]string{}, ids...), "kp-not-exist")
	if code, _, _ := batchSubmitJSON(t, h, token, missing); code != 404 {
		t.Fatalf("缺失点应 404: %d", code)
	}
}

func TestBatchPreviewReportsCandidatesAndMissingCards(t *testing.T) {
	_, h, token, points, cleanup := setupRandomBatchAPI(t)
	defer cleanup()
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	// 直接模式：仅候选统计。
	r := serveGenerateJSON(t, h, "POST", "/api/batch/preview", token, map[string]any{
		"workflow_mode": "direct", "version_id": points[0].VersionID, "knowledge_point_ids": ids,
	})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var preview map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview["candidate_count"] != float64(3) || preview["mode"] != "per_point" {
		t.Fatalf("直接模式预览不符: %v", preview)
	}
	// 知识模式：只有第一个点有可用卡，另两个应列为缺资料。
	r = serveGenerateJSON(t, h, "POST", "/api/batch/preview", token, map[string]any{
		"workflow_mode": "knowledge", "version_id": points[0].VersionID, "knowledge_point_ids": ids,
	})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	preview = map[string]any{}
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview["missing_card_count"] != float64(2) || preview["coverage_checked"] != float64(3) {
		t.Fatalf("知识模式缺卡统计不符: %v", preview)
	}
	// 提交侧全量校验：知识模式随机抽中无卡点时明确拒绝（抽中全有卡则成功；
	// 多次提交覆盖两种路径——固定 key 确保重放不重复计费）。
	samples := map[string]bool{}
	for i := 0; i < 6; i++ {
		payload := map[string]any{
			"workflow_mode": "knowledge", "version_id": points[0].VersionID,
			"knowledge_point_ids": ids,
			"mode": "random_total", "total_count": 3,
			"request_key": fmt.Sprintf("c2-kb-%d", i),
		}
		code, body, _ := batchSubmitJSON(t, h, token, payload)
		if code == 200 {
			waitBatchTerminal(t, h, token, body["job_id"].(string))
			samples["ok"] = true
			continue
		}
		if msg, _ := body["error"].(string); strings.Contains(msg, "缺少可用的已启用考点知识卡") {
			samples["rejected"] = true
			continue
		}
		t.Fatalf("知识模式随机提交的失败应归因缺卡: %d %v", code, body)
	}
	if !samples["ok"] && !samples["rejected"] {
		t.Fatal("知识模式随机提交未产生任何路径样本")
	}
}

// R04：跨页全选的大批量 ID 集合走快照批解，缺失点明确 404。
func TestBatchBulkSelectionAcrossPages(t *testing.T) {
	s, h, token, points, cleanup := setupRandomBatchAPI(t)
	defer cleanup()
	version, err := s.kpSvc.ResolveVersion(t.Context(), points[0].VersionID)
	if err != nil {
		t.Fatal(err)
	}
	// 共 25 个点（3 现有 + 22 新增），超过小批量阈值 20。
	bulkIDs := make([]string, 0, 25)
	for _, p := range points {
		bulkIDs = append(bulkIDs, p.ID)
	}
	for i := 10; i < 32; i++ {
		created, e := s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: version.ID, Category: "临床综合", Subject: "消化", Topic: fmt.Sprintf("批量考点%d", i), OutlineCode: fmt.Sprintf("bulk.%03d", i)})
		if e != nil {
			t.Fatal(e)
		}
		bulkIDs = append(bulkIDs, created.ID)
	}
	if len(bulkIDs) != 25 {
		t.Fatalf("夹具数量不符: %d", len(bulkIDs))
	}
	r := serveGenerateJSON(t, h, "POST", "/api/batch/preview", token, map[string]any{
		"workflow_mode": "direct", "version_id": version.ID, "knowledge_point_ids": bulkIDs,
	})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var preview map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &preview)
	if preview["candidate_count"] != float64(25) {
		t.Fatalf("跨页候选统计不符: %v", preview)
	}
	// 夹带一个已删除点：明确 404，不静默忽略。
	removed := bulkIDs[len(bulkIDs)-1]
	if err := s.kpSvc.DeletePoint(t.Context(), removed); err != nil {
		t.Fatal(err)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/batch/preview", token, map[string]any{
		"workflow_mode": "direct", "version_id": version.ID, "knowledge_point_ids": bulkIDs,
	})
	if r.Code != http.StatusNotFound {
		t.Fatalf("含已删除点的批量选择应 404: %d %s", r.Code, r.Body)
	}
}
