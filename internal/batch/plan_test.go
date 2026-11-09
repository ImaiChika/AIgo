package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

func randomCandidates(n int) []domain.KnowledgePoint {
	points := make([]domain.KnowledgePoint, 0, n)
	for i := 0; i < n; i++ {
		points = append(points, domain.KnowledgePoint{
			ID: fmt.Sprintf("kp-%03d", i), VersionID: "v1", Topic: fmt.Sprintf("考点%03d", i),
			OutlineCode: fmt.Sprintf("110.%d", i), Category: "临床综合", Subject: "一、呼吸系统",
		})
	}
	return points
}

func TestDrawRandomPlanWithInjectedSequence(t *testing.T) {
	restore := randomIndex
	defer func() { randomIndex = restore }()
	calls := 0
	// 5 个候选，抽 6 次：序列 0,1,0,2,1,0 → 点0出现3次、点1两次、点2一次、点3/4 不出现。
	seq := []int{0, 1, 0, 2, 1, 0}
	randomIndex = func(n int) int {
		v := seq[calls]
		calls++
		if v >= n {
			t.Fatalf("注入序列越界: %d >= %d", v, n)
		}
		return v
	}
	plan, err := DrawRandomPlan(randomCandidates(5), "v1", 6)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != domain.BatchModeRandomTotal || plan.RequestedTotal != 6 || plan.CandidateCount != 5 {
		t.Fatalf("计划摘要不符: %+v", plan)
	}
	wantIDs := []string{"kp-000", "kp-001", "kp-000", "kp-002", "kp-001", "kp-000"}
	wantSeq := []int{1, 1, 2, 1, 2, 3}
	for i, u := range plan.Units {
		if u.KnowledgePoint.ID != wantIDs[i] || u.PointSequence != wantSeq[i] || u.Index != i {
			t.Fatalf("单元 %d 不符: %+v", i, u)
		}
	}
	if len(plan.UniquePlanPoints()) != 3 {
		t.Fatalf("去重点数应为 3，实际 %d", len(plan.UniquePlanPoints()))
	}
	if calls != 6 {
		t.Fatalf("应恰好抽样 6 次，实际 %d", calls)
	}
}

func TestDrawRandomPlanRejectsInvalidTotal(t *testing.T) {
	candidates := randomCandidates(3)
	if _, err := DrawRandomPlan(candidates, "v1", 0); err == nil {
		t.Fatal("N=0 应拒绝")
	}
	if _, err := DrawRandomPlan(nil, "v1", 3); err == nil {
		t.Fatal("空候选应拒绝")
	}
	// N 大于候选数合法（有放回重复抽）。
	if _, err := DrawRandomPlan(candidates, "v1", 10); err != nil {
		t.Fatal(err)
	}
}

func TestPerPointPlanMatchesLegacyFlatten(t *testing.T) {
	points := randomCandidates(3)
	plan, err := PerPointPlan(points, "v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	units := plan.PlanUnitsAsPoints()
	legacy := flattenUnits(points, 2)
	if len(units) != len(legacy) {
		t.Fatalf("与旧摊平结果长度不一致: %d vs %d", len(units), len(legacy))
	}
	for i := range units {
		if units[i].ID != legacy[i].ID {
			t.Fatalf("单元 %d 与旧摊平不一致", i)
		}
	}
	if plan.Units[1].PointSequence != 2 || plan.Units[2].PointSequence != 1 {
		t.Fatalf("每点出现序号不符: %+v", plan.Units[:3])
	}
}

// 显式计划优先于旧摊平推导：随机任务的单元在恢复/重跑时绝不重算。
func TestBatchUnitsFromRecordPrefersPlan(t *testing.T) {
	candidates := randomCandidates(4)
	plan, err := PerPointPlan(candidates[:2], "v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 故意让 PointsJSON 与计划不同（模拟随机任务只存去重点）。
	unique := plan.UniquePlanPoints()
	pointsJSON, _ := json.Marshal(unique)
	planJSON, _ := json.Marshal(plan)
	record := storage.BatchJobRecord{
		PointsJSON: string(pointsJSON),
		PlanJSON:   string(planJSON),
		TotalCount: plan.RequestedTotal,
	}
	units, err := BatchUnitsFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != len(plan.Units) || units[0].ID != plan.Units[0].KnowledgePoint.ID {
		t.Fatalf("应按计划解码: %+v", units)
	}
}

func TestBatchUnitsFromRecordLegacyFallbackAndCorruption(t *testing.T) {
	points := randomCandidates(3)
	pointsJSON, _ := json.Marshal(points)
	// 旧任务无计划：按 总数/点数 摊平。
	record := storage.BatchJobRecord{PointsJSON: string(pointsJSON), TotalCount: 6}
	units, err := BatchUnitsFromRecord(record)
	if err != nil || len(units) != 6 {
		t.Fatalf("旧任务摊平失败: %d %v", len(units), err)
	}
	// 总数不能整除点数：拒绝，不猜测。
	record.TotalCount = 7
	if _, err = BatchUnitsFromRecord(record); err == nil {
		t.Fatal("不整除总数应拒绝")
	}
	// 计划损坏：拒绝。
	record.TotalCount = 6
	record.PlanJSON = `{"version":99,"mode":"random_total"}`
	if _, err = BatchUnitsFromRecord(record); err == nil {
		t.Fatal("损坏计划应拒绝")
	}
}

func TestBatchPlanFromRecordRejectsConflictingTotal(t *testing.T) {
	t.Parallel()
	plan, err := PerPointPlan(randomCandidates(2), "v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, total := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			t.Parallel()
			if _, err := BatchPlanFromRecord(storage.BatchJobRecord{PlanJSON: string(raw), TotalCount: total}); err == nil {
				t.Fatalf("record total %d conflicts with frozen plan of 2 units", total)
			}
		})
	}
}

func TestDrawRandomPlanFreezesEntireUniqueCandidatePool(t *testing.T) {
	restore := randomIndex
	t.Cleanup(func() { randomIndex = restore })
	randomIndex = func(n int) int {
		if n != 2 {
			t.Fatalf("duplicate identity changed sampling weight: candidate count %d", n)
		}
		return 0
	}
	points := randomCandidates(2)
	points[0].Revision, points[1].Revision = 2, 3
	plan, err := DrawRandomPlan(append(points, points[0]), "v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Candidates []struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	if plan.CandidateCount != 2 || len(frozen.Candidates) != 2 || frozen.Candidates[1].ID != points[1].ID || frozen.Candidates[1].Revision != 3 {
		t.Fatalf("unselected candidate identity/revision was not frozen: %s", raw)
	}
}

func TestDrawRandomPlanCodeOnlyOccurrenceSequence(t *testing.T) {
	restore := randomIndex
	t.Cleanup(func() { randomIndex = restore })
	sequence := []int{0, 1, 0}
	randomIndex = func(int) int {
		v := sequence[0]
		sequence = sequence[1:]
		return v
	}
	points := randomCandidates(2)
	points[0].ID, points[1].ID = "", ""
	plan, err := DrawRandomPlan(points, "v1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Units[1].PointSequence != 1 || plan.Units[2].PointSequence != 2 {
		t.Fatalf("code-only point occurrence counters were combined: %+v", plan.Units)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Units []struct {
			TargetOffset *int `json:"target_offset"`
		} `json:"units"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{0, 0, 1} {
		if frozen.Units[i].TargetOffset == nil || *frozen.Units[i].TargetOffset != want {
			t.Fatalf("unit %d did not freeze target offset %d: %s", i, want, raw)
		}
	}
}

func TestPlanBuildersRejectConflictingCandidateSnapshots(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.BatchModePerPoint, domain.BatchModeRandomTotal} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			for _, mismatch := range []string{"version", "revision", "content"} {
				t.Run(mismatch, func(t *testing.T) {
					t.Parallel()
					points := randomCandidates(1)
					other := points[0]
					switch mismatch {
					case "version":
						other.VersionID = "v2"
					case "revision":
						other.Revision++
					case "content":
						other.Topic = "修改后的内容"
					}
					points = append(points, other)
					var err error
					if mode == domain.BatchModePerPoint {
						_, err = PerPointPlan(points, "v1", 1)
					} else {
						_, err = DrawRandomPlan(points, "v1", 1)
					}
					if err == nil {
						t.Fatal("conflicting candidate snapshots must not be silently deduplicated")
					}
				})
			}
		})
	}
}

func TestPerPointPlanFreezesOffsetsAndDetachesInput(t *testing.T) {
	t.Parallel()
	points := randomCandidates(2)
	points[0].Keywords = []string{"提交时关键词"}
	plan, err := PerPointPlan(points, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	points[0].Keywords[0] = "后来修改的关键词"
	if plan.VersionID != "v1" || plan.Units[0].KnowledgePoint.Keywords[0] != "提交时关键词" {
		t.Fatalf("plan must freeze version and detach mutable inputs: %+v", plan)
	}
	for i, want := range []int{0, 1, 0, 1} {
		if plan.Units[i].TargetOffset == nil || *plan.Units[i].TargetOffset != want {
			t.Fatalf("unit %d: target offset = %v, want %d", i, plan.Units[i].TargetOffset, want)
		}
	}
}

func TestBatchPlanFromRecordKeepsLegacyTargetOffsetsAbsent(t *testing.T) {
	t.Parallel()
	const raw = `{"version":1,"mode":"random_total","requested_total":3,"version_id":"v1","candidate_count":2,"units":[{"index":0,"knowledge_point":{"id":"a","version_id":"v1"},"point_sequence":1},{"index":1,"knowledge_point":{"id":"b","version_id":"v1"},"point_sequence":1},{"index":2,"knowledge_point":{"id":"a","version_id":"v1"},"point_sequence":2}]}`
	plan, err := BatchPlanFromRecord(storage.BatchJobRecord{PlanJSON: raw, TotalCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range plan.Units {
		if unit.TargetOffset != nil {
			t.Fatal("legacy accepted plan acquired a new per-point offset")
		}
	}
}

// 随机任务提交后：任务记录包含冻结计划；重启恢复（resumePending）使用同一单元
// 序列，不重新抽样（以注入随机源调用次数为证）。
func TestSubmitPlanPersistsFrozenRandomUnits(t *testing.T) {
	restore := randomIndex
	defer func() { randomIndex = restore }()
	seq := []int{2, 0, 2}
	randomIndex = func(n int) int {
		v := seq[0]
		seq = seq[1:]
		return v
	}
	gen := generator.NewService(localTestLLM{})
	executor := NewLocalExecutor(gen, testutil.NewMemoryStore(), &localBatchStore{}, "test")
	t.Cleanup(executor.Close)
	points := randomCandidates(3)
	plan, err := DrawRandomPlan(points, "v1", 3)
	if err != nil {
		t.Fatal(err)
	}
	jobID, count, err := executor.SubmitPlan(t.Context(), PlanSubmission{Plan: plan, JobName: "随机冻结"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("计划题数应为 3，实际 %d", count)
	}
	stored := executor.batchJobStore.(*localBatchStore)
	record, err := stored.GetBatchJob(t.Context(), jobID)
	if err != nil || record == nil {
		t.Fatal("任务未持久化")
	}
	units, err := BatchUnitsFromRecord(*record)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kp-002", "kp-000", "kp-002"}
	for i, u := range units {
		if u.ID != want[i] {
			t.Fatalf("冻结单元 %d 应为 %s，实际 %s", i, want[i], u.ID)
		}
	}
	if len(seq) != 0 {
		t.Fatalf("抽样应只发生一次，剩余注入 %v", seq)
	}
}

// R08：随机任务提交后重启执行器：恢复扫描按冻结计划执行同一单元序列，
// 不重新抽样（注入随机源只在 DrawRandomPlan 时消耗，恢复后调用数为零）。
func TestRandomPlanSurvivesExecutorRestartWithoutResampling(t *testing.T) {
	restore := randomIndex
	defer func() { randomIndex = restore }()
	drawCalls := 0
	randomIndex = func(n int) int {
		drawCalls++
		return 1 // 全部抽第 2 个候选
	}
	store := &localBatchStore{}
	questions := testutil.NewMemoryStore()
	gen := generator.NewService(localTestLLM{})
	ctx, stop := context.WithCancel(context.Background())
	executor1 := NewLocalExecutorWithConfig(gen, questions, store, "restart-a", LocalConfig{Concurrency: 1, Context: ctx})
	points := randomCandidates(3)
	plan, err := DrawRandomPlan(points, "v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _, err := executor1.SubmitPlan(context.Background(), PlanSubmission{Plan: plan, JobName: "重启随机"})
	if err != nil {
		t.Fatal(err)
	}
	stop() // 模拟进程中断：执行器关闭，任务留在存储里
	executor1.Close()
	if drawCalls != 2 {
		t.Fatalf("抽样应只在提交时发生 2 次，实际 %d", drawCalls)
	}

	// 新执行器接管：恢复扫描应按计划单元（全部为 kp-001）继续执行。
	executor2 := NewLocalExecutorWithConfig(gen, questions, store, "restart-b", LocalConfig{Concurrency: 1, Context: context.Background(), RecoveryInterval: 10 * time.Millisecond})
	defer executor2.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := executor2.GetJobStatus(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "completed" {
			if job.Completed != 2 {
				t.Fatalf("恢复后应完成 2 题，实际 %d", job.Completed)
			}
			for _, item := range job.Items {
				if item.OutlineCode != points[1].OutlineCode {
					t.Fatalf("恢复单元应保持冻结考点 %s，实际 %s", points[1].OutlineCode, item.OutlineCode)
				}
			}
			if drawCalls != 2 {
				t.Fatalf("恢复过程不得重新抽样，调用数变为 %d", drawCalls)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("恢复未在时限内完成")
}
