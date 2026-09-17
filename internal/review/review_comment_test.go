package review

import (
	"context"
	"strings"
	"testing"

	"aigo/internal/domain"
)

// ===== 结构化评语基础行为 =====

func TestReviewCommentIsEmpty(t *testing.T) {
	cases := []struct {
		name string
		c    *domain.ReviewComment
		want bool
	}{
		{"nil", nil, true},
		{"全空", &domain.ReviewComment{}, true},
		{"空白字符", &domain.ReviewComment{Stem: "  ", Other: "\n\t"}, true},
		{"题干有内容", &domain.ReviewComment{Stem: "题干缺少关键体征"}, false},
	}
	for _, tc := range cases {
		if got := tc.c.IsEmpty(); got != tc.want {
			t.Fatalf("%s: IsEmpty()=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestReviewCommentFlatten(t *testing.T) {
	c := &domain.ReviewComment{Stem: "题干完整", Answer: "答案 B 有争议"}
	flat := c.Flatten()
	if !strings.Contains(flat, "【题干】题干完整") || !strings.Contains(flat, "【答案与解析】答案 B 有争议") {
		t.Fatalf("Flatten 未包含已填栏目: %q", flat)
	}
	if strings.Contains(flat, "【选项】") || strings.Contains(flat, "【其他】") {
		t.Fatalf("Flatten 不应包含空栏目: %q", flat)
	}
	if (&domain.ReviewComment{}).Flatten() != "" {
		t.Fatal("空评语 Flatten 应为空串")
	}
}

// ===== 评语校验：通过可选，非通过必填 =====

func TestReviewCommentRequiredForNonApproval(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds[0].ExpertIDs = []string{"r1", "r2"}
	reviewStore.SaveFlowConfig(ctx, flow)
	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatal(err)
	}

	// 通过：评语可空
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("通过时不应强制评语: %v", err)
	}

	// 驳回：无任何评语 → 拒绝
	err = svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected})
	if err == nil || !strings.Contains(err.Error(), "必须填写评语") {
		t.Fatalf("驳回无评语应被拒绝，实际: %v", err)
	}

	// 驳回：结构化评语全空白 → 拒绝
	err = svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected,
		Comment: &domain.ReviewComment{Stem: "  ", Options: "\n"}})
	if err == nil || !strings.Contains(err.Error(), "必须填写评语") {
		t.Fatalf("驳回全空白评语应被拒绝，实际: %v", err)
	}

	// 驳回：仅一栏有内容 → 通过
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected,
		Comment: &domain.ReviewComment{Options: "选项 C 与题干矛盾"}}); err != nil {
		t.Fatalf("驳回有一栏评语应通过: %v", err)
	}
}

func TestLegacyOpinionCountsAsComment(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds[0].ExpertIDs = []string{"r1", "r2"}
	reviewStore.SaveFlowConfig(ctx, flow)
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")

	// 旧客户端只传 opinion → 视为「其他」栏，非通过也应放行
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRevisionRequired, Opinion: "数值单位需核对"}); err != nil {
		t.Fatalf("旧版自由文本意见应被视为评语: %v", err)
	}
	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 1 || records[0].Comment == nil || records[0].Comment.Other != "数值单位需核对" {
		t.Fatalf("旧意见应映射为结构化评语的「其他」栏: %+v", records)
	}
}

// ===== 评语落库：结构化内容 + 专家名快照 + 拼接文本 =====

func TestStructuredCommentPersistedWithExpertName(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	reviewStore.SaveFlowConfig(ctx, testFlow())
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")

	comment := &domain.ReviewComment{Stem: "题干清晰", Answer: "答案应为 C"}
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved, Comment: comment}); err != nil {
		t.Fatal(err)
	}

	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 1 {
		t.Fatalf("应有 1 条审核记录，实际 %d", len(records))
	}
	rec := records[0]
	if rec.Comment == nil || rec.Comment.Stem != "题干清晰" || rec.Comment.Answer != "答案应为 C" {
		t.Fatalf("记录应包含结构化评语: %+v", rec)
	}
	if !strings.Contains(rec.Opinion, "【题干】题干清晰") {
		t.Fatalf("拼接文本应写入 opinion 兼容旧展示: %q", rec.Opinion)
	}
	// fakeResolver 的 DisplayName 即 ID 本身
	if rec.ExpertName != "r1" {
		t.Fatalf("记录应包含审核人显示名快照，实际 %q", rec.ExpertName)
	}
	// 任务内嵌快照同样保存（供任务详情历史追溯）
	stored, _ := reviewStore.GetTask(ctx, task.ID)
	rv := stored.RoundResults[0].Reviews[0]
	if rv.Comment == nil || rv.ExpertName != "r1" || rv.Opinion != rec.Opinion {
		t.Fatalf("任务内审核快照应包含评语与专家名: %+v", rv)
	}
}

// ===== 决断评语校验 =====

func TestFinalizeCommentRequiredForNonApproval(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds[0].RequiredCount = 1
	reviewStore.SaveFlowConfig(ctx, flow)
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected, Opinion: "科学性错误"})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved})

	// 决断驳回：无评语 → 拒绝
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRejected}); err == nil {
		t.Fatal("决断驳回无评语应被拒绝")
	}
	// 决断通过：评语可空
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Other: "题干与答案矛盾，整体驳回"}}); err != nil {
		t.Fatalf("决断驳回有一栏评语应通过: %v", err)
	}
	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	last := records[len(records)-1]
	if last.Comment == nil || last.Comment.Other == "" {
		t.Fatalf("决断记录应包含结构化评语: %+v", last)
	}
}

// ===== 评语可见性：同轮隔离、跨轮摘要、把关人全量 =====

func TestRecordsForViewerIsolation(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2", "r3", "r4"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds[0].ExpertIDs = []string{"r1", "r2", "r3", "r4"}
	flow.Rounds[0].RequiredCount = 3
	reviewStore.SaveFlowConfig(ctx, flow)
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")

	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "r1 意见"}})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "r2 意见"}})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r3", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Stem: "r3 意见"}})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r4", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "r4 意见"}})

	// 普通审核人：只能看到自己的记录（任务进行中）
	own, err := svc.RecordsForViewer(ctx, task.ID, "r1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].ExpertID != "r1" {
		t.Fatalf("普通审核人只应看到自己的记录: %+v", own)
	}
	// 把关人：全量可见
	all, err := svc.RecordsForViewer(ctx, task.ID, "admin1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("把关人应看到全部 4 条记录，实际 %d", len(all))
	}

	// 任务进入轮外最终决断后，普通审核人依旧只见自己的；
	// 决断驳回为终态后，历史档案全量可见
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Other: "维持驳回"}}); err != nil {
		t.Fatal(err)
	}
	ownAfter, err := svc.RecordsForViewer(ctx, task.ID, "r1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownAfter) != 5 {
		t.Fatalf("终态后历史记录应全量可见（4条审核+1条决断），实际 %d", len(ownAfter))
	}
}

func TestMyTasksHidesPeerVotesForReviewer(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2", "r3"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds[0].ExpertIDs = []string{"r1", "r2", "r3"}
	reviewStore.SaveFlowConfig(ctx, flow)
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")

	// r1 已投通过、r2 已投驳回（各带评语），r3 未投
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "r1 意见"}})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Stem: "r2 意见"}})

	// 普通审核人 r3：看不到他人票数与评语
	items, err := svc.MyTasks(ctx, "r3", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("r3 应看到 1 个待审任务，实际 %d", len(items))
	}
	item := items[0]
	if item.Voted != 0 || item.Approved != 0 || item.Rejected != 0 || item.Revision != 0 {
		t.Fatalf("普通审核人不应看到实时票数: %+v", item)
	}
	for _, rr := range item.Task.RoundResults {
		if len(rr.Reviews) != 0 {
			t.Fatalf("普通审核人不应看到评语明细: 第%d轮 %d 条", rr.RoundNumber, len(rr.Reviews))
		}
	}
	if item.Task.FinalDecision != nil {
		t.Fatal("普通审核人不应看到决断意见")
	}

	// 把关人视图：保留完整票数与评语快照
	gateItems, err := svc.MyTasks(ctx, "admin1", true)
	if err != nil {
		t.Fatal(err)
	}
	// admin1 不在第 1 轮分配名单内，正常不产生待审卡片；直接用记录接口验证全量
	// （把关人的任务级全量在 MyDecisions 中验证）
	if len(gateItems) != 0 {
		t.Fatalf("admin1 未被分配第 1 轮，不应有待审卡片: %+v", gateItems)
	}
	all, _ := svc.RecordsForViewer(ctx, task.ID, "admin1", true)
	if len(all) != 2 {
		t.Fatalf("把关人应看到全部 2 条已提交记录，实际 %d", len(all))
	}
}

func TestRoundAdvanceKeepsPreviousRoundSummaryForReviewer(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	svc, reviewStore, questionStore := newTestService(reviewers, map[string]bool{"admin1": true})
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds = append(flow.Rounds, domain.RoundConfig{RoundNumber: 2, Name: "复审", ExpertIDs: []string{"r1", "r2"}, RequiredCount: 0})
	reviewStore.SaveFlowConfig(ctx, flow)
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")

	// 第 1 轮 2 票通过 → 过轮进入第 2 轮
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "r1 第1轮"}})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "r2 第1轮"}})

	// r1 现在进入第 2 轮：能看到第 1 轮的脱敏票数汇总，但看不到评语明细
	items, err := svc.MyTasks(ctx, "r1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RoundIndex != 2 {
		t.Fatalf("r1 应在第 2 轮待审: %+v", items)
	}
	rr1 := items[0].Task.RoundResults[0]
	if rr1.ApprovedCount != 2 || rr1.Passed != true {
		t.Fatalf("上一轮应保留脱敏票数汇总: %+v", rr1)
	}
	if len(rr1.Reviews) != 0 {
		t.Fatalf("上一轮评语明细对普通审核人不可见: %+v", rr1.Reviews)
	}
}

func TestTaskForViewerSanitized(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	reviewStore.SaveFlowConfig(ctx, testFlow())
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Stem: "r1 意见"}})

	// 普通审核人视角：任务详情不含评语与票数
	sanitized, err := svc.TaskForViewer(ctx, task.ID, "r2", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, rr := range sanitized.RoundResults {
		if len(rr.Reviews) != 0 || rr.RejectedCount != 0 {
			t.Fatalf("裁剪任务不应含评语/票数: %+v", rr)
		}
	}
	// 全量视角：保留
	full, err := svc.TaskForViewer(ctx, task.ID, "admin1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.RoundResults[0].Reviews) != 1 || full.RoundResults[0].RejectedCount != 1 {
		t.Fatalf("全量任务应含评语与票数: %+v", full.RoundResults[0])
	}
}

func TestListResultsForViewerIsolation(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds[0].RequiredCount = 1
	reviewStore.SaveFlowConfig(ctx, flow)
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Stem: "r1 意见"}})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "r2 意见"}})

	// 轮外最终决断中的任务：普通审核人只见自己的记录
	items, _, err := svc.ListResultsForViewer(ctx, "r1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("应有 1 个结果项，实际 %d", len(items))
	}
	if err := svc.AttachRecordsForItems(ctx, items, "r1", false); err != nil {
		t.Fatal(err)
	}
	item := items[0]
	if len(item.Records) != 1 || item.Records[0].ExpertID != "r1" {
		t.Fatalf("进行中任务对普通审核人只暴露自己的记录: %+v", item.Records)
	}
	for _, rr := range item.Task.RoundResults {
		if len(rr.Reviews) != 0 {
			t.Fatalf("进行中任务对普通审核人不暴露评语明细: %+v", rr)
		}
	}

	// 把关人：全量
	gateItems, _, err := svc.ListResultsForViewer(ctx, "admin1", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AttachRecordsForItems(ctx, gateItems, "admin1", true); err != nil {
		t.Fatal(err)
	}
	if len(gateItems[0].Records) != 2 {
		t.Fatalf("把关人应看到全部评语: %+v", gateItems[0].Records)
	}

	// 任务结束后（驳回为终态）：历史档案全量可见
	svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Other: "维持驳回"}})
	items, _, err = svc.ListResultsForViewer(ctx, "r1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AttachRecordsForItems(ctx, items, "r1", false); err != nil {
		t.Fatal(err)
	}
	if len(items[0].Records) != 3 {
		t.Fatalf("终态任务的历史记录应全量可见，实际 %d 条", len(items[0].Records))
	}
}

// ===== 提交批次：重提后决断对比只见当前批次，历史批次留痕 =====

func TestResubmitStartsNewAttempt(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.Rounds[0].RequiredCount = 1
	reviewStore.SaveFlowConfig(ctx, flow)
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if task.Attempt != 1 {
		t.Fatalf("首提批次应为 1，实际 %d", task.Attempt)
	}

	// 第 1 批：达到通过门槛后进入轮外决断 → 决断退回修改
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected, Comment: &domain.ReviewComment{Stem: "第一批驳回意见"}})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "第一批通过意见"}})
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRevisionRequired, Comment: &domain.ReviewComment{Other: "退回修改"}}); err != nil {
		t.Fatal(err)
	}

	// 修改后重提 → 版本 +1、批次 +1，本轮票清零
	q, _ := questionStore.GetQuestion(ctx, "q1")
	q.ClinicalStem += "（第二批修订）"
	q.Version++
	q.Status = domain.StatusAIReviewed
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	task2, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatal(err)
	}
	if task2.Attempt != 2 {
		t.Fatalf("重提后批次应为 2，实际 %d", task2.Attempt)
	}
	if len(task2.RoundResults[0].Reviews) != 0 {
		t.Fatalf("重提后本轮票应清零，实际 %d 条", len(task2.RoundResults[0].Reviews))
	}

	// 第 2 批重新投票
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved, Comment: &domain.ReviewComment{Stem: "已修正，第二批通过"}}); err != nil {
		t.Fatal(err)
	}

	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 4 { // 2 投票 + 1 决断（第1批） + 1 投票（第2批）
		t.Fatalf("历史批次留痕：应共 4 条记录，实际 %d", len(records))
	}
	current := 0
	for _, r := range records {
		if r.Attempt == 2 {
			current++
			if r.Comment == nil || r.Comment.Stem != "已修正，第二批通过" {
				t.Fatalf("第 2 批记录评语不符: %+v", r)
			}
		}
	}
	if current != 1 {
		t.Fatalf("第 2 批应只有 1 条记录，实际 %d", current)
	}
}
