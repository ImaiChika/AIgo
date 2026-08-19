package review

import (
	"context"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage/testutil"
)

// fakeResolver 测试用的审核人解析器。
type fakeResolver struct {
	reviewers  map[string][]string // bankID -> 审核人ID列表
	finalRight map[string]bool     // userID -> 是否有最终把关权限
}

func (f *fakeResolver) ListReviewers(_ context.Context, bankID string) ([]string, error) {
	return f.reviewers[bankID], nil
}

func (f *fakeResolver) ListFallbackReviewers(_ context.Context) ([]string, error) {
	return nil, nil
}

func (f *fakeResolver) HasFinalRight(_ context.Context, userID string) (bool, error) {
	return f.finalRight[userID], nil
}

func (f *fakeResolver) ListReviewCandidates(_ context.Context) ([]Candidate, error) {
	seen := make(map[string]bool)
	var out []Candidate
	for _, ids := range f.reviewers {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, Candidate{ID: id, Username: id, DisplayName: id})
			}
		}
	}
	return out, nil
}

func newTestService(reviewers map[string][]string, finalRight map[string]bool) (*Service, *testutil.MemoryReviewStore, *testutil.MemoryStore) {
	reviewStore := testutil.NewMemoryReviewStore()
	questionStore := testutil.NewMemoryStore()
	expertStore := testutil.NewMemoryExpertStore()
	svc := NewService(expertStore, reviewStore, questionStore, &fakeResolver{reviewers: reviewers, finalRight: finalRight})
	return svc, reviewStore, questionStore
}

func testQuestion(bankID string) *domain.A2Question {
	return &domain.A2Question{
		ID:           "q1",
		ClinicalStem: "患者，男，50岁，突发胸痛2小时……",
		Options: []domain.Option{
			{Label: "A", Text: "急性心肌梗死"},
			{Label: "B", Text: "主动脉夹层"},
			{Label: "C", Text: "肺栓塞"},
		},
		Answer:  "A",
		Status:   domain.StatusAIDraft,
		Version:  1,
		BankIDs:  []string{bankID},
	}
}

func testFlow() domain.ReviewFlowConfig {
	return domain.ReviewFlowConfig{
		ID:               "flow-test",
		Name:             "测试流程",
		FinalReviewerIDs: []string{"admin1"},
		Rounds: []domain.RoundConfig{
			{
				RoundNumber:   1,
				Name:          "专家组审核",
				ExpertIDs:     []string{}, // 自动匹配
				RequiredCount: 0,          // 全部通过
			},
		},
	}
}

// TestConflictFlow 测试：3位审题人投票，2通过1驳回 → 全审完 → conflict。
func TestConflictFlow(t *testing.T) {
	reviewers := map[string][]string{
		"bank-neike": {"r1", "r2", "r3"},
	}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatalf("提交审核失败: %v", err)
	}
	if len(task.AssignedTo) != 3 {
		t.Fatalf("自动匹配审核人失败：期望3人，实际 %d", len(task.AssignedTo))
	}

	// r1 通过
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("r1 审核失败: %v", err)
	}
	// r2 通过
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("r2 审核失败: %v", err)
	}
	// 此时未全部审完，任务应仍在 reviewing
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusReviewing {
		t.Fatalf("未审完时状态应为 reviewing，实际 %s", task.Status)
	}
	// r3 驳回 → 全审完 → 冲突
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r3", Action: domain.StatusRejected, Opinion: "答案有争议"}); err != nil {
		t.Fatalf("r3 审核失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusConflict {
		t.Fatalf("票数冲突时应为 conflict，实际 %s", task.Status)
	}
	round := task.RoundResults[0]
	if round.ApprovedCount != 2 || round.RejectedCount != 1 {
		t.Fatalf("票数统计错误: 通过%d 驳回%d", round.ApprovedCount, round.RejectedCount)
	}
	// 题目状态保持 reviewing
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusReviewing {
		t.Fatalf("冲突时题目状态应为 reviewing，实际 %s", q.Status)
	}
}

// TestFinalizeApprove 测试：冲突后把关人决断通过 → 任务与题目直接入库（published）。
func TestFinalizeApprove(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected, Opinion: "需要再确认"})

	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusConflict {
		t.Fatalf("应为 conflict，实际 %s", task.Status)
	}

	// 不在名单内的用户决断 → 拒绝
	err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "stranger", Action: domain.StatusApproved})
	if err == nil {
		t.Fatal("不在把关人名单内的用户决断应被拒绝")
	}

	// 把关人决断通过 → 直接入库（无需再手动发布）
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved, Opinion: "经复核，答案正确"}); err != nil {
		t.Fatalf("决断失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusPublished {
		t.Fatalf("决断通过后任务应直接入库 published，实际 %s", task.Status)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusPublished {
		t.Fatalf("题目应直接入库 published，实际 %s", q.Status)
	}
	if task.FinalDecision == nil || task.FinalDecision.ExpertID != "admin1" {
		t.Fatal("缺少决断记录")
	}
}

// TestFinalizeReject 测试：把关人决断驳回 → 任务与题目 rejected。
func TestFinalizeReject(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected})

	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRejected, Opinion: "科学性问题，驳回"}); err != nil {
		t.Fatalf("决断失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusRejected {
		t.Fatalf("决断驳回后任务应为 rejected，实际 %s", task.Status)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusRejected {
		t.Fatalf("题目应为 rejected，实际 %s", q.Status)
	}

	// 重新提交 → 回到第 1 轮重新审核
	task2, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatalf("重新提交失败: %v", err)
	}
	if task2.Status != domain.StatusReviewing || task2.CurrentRound != 1 {
		t.Fatalf("重提后应为第1轮审核中，实际 round=%d status=%s", task2.CurrentRound, task2.Status)
	}
}

// TestFinalizeRevision 测试：把关人决断退回修改 → revision_required，重提后本轮票清零。
func TestFinalizeRevision(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRevisionRequired, Opinion: "题干缺少诱因"})

	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRevisionRequired}); err != nil {
		t.Fatalf("决断失败: %v", err)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusRevisionRequired {
		t.Fatalf("题目应为 revision_required，实际 %s", q.Status)
	}

	// 修改后重新提交
	q.Status = domain.StatusRevisionRequired
	questionStore.SaveQuestion(ctx, *q)
	task2, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatalf("重新提交失败: %v", err)
	}
	if task2.Status != domain.StatusReviewing {
		t.Fatalf("重提后应为 reviewing，实际 %s", task2.Status)
	}
	round := task2.RoundResults[0]
	if len(round.Reviews) != 0 || round.ApprovedCount != 0 || round.RejectedCount != 0 || round.RevisionCount != 0 {
		t.Fatal("退回修改重提后本轮票数应清零")
	}
}

// TestRejectThenPassAdvances 测试用户报告的 bug：通过票数=1 时，
// 有人投驳回、有人投通过 → 达到通过票数即推进到下一轮（不再卡住等待全员）。
func TestRejectThenPassAdvances(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2", "r3"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := domain.ReviewFlowConfig{
		ID:               "flow-2r",
		Name:             "两轮流程",
		FinalReviewerIDs: []string{"admin1"},
		Rounds: []domain.RoundConfig{
			{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1", "r2", "r3", "admin1"}, RequiredCount: 1},
			{RoundNumber: 2, Name: "复审", ExpertIDs: []string{"r1", "r2", "r3", "admin1"}, RequiredCount: 1},
		},
	}
	svc.reviewStore.SaveFlowConfig(ctx, flow)

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-2r")

	// admin1（把关人兼审核人）投驳回
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "admin1", Action: domain.StatusRejected, Opinion: "admin 驳回", HasFinalRight: true}); err != nil {
		t.Fatalf("admin 驳回失败: %v", err)
	}
	// r1 投通过（达到通过票数 1）→ 应推进到第 2 轮（无视反对票）
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("r1 通过失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusReviewing || task.CurrentRound != 2 {
		t.Fatalf("有通过票即应推进到第2轮，实际 status=%s round=%d", task.Status, task.CurrentRound)
	}
	// 反对票已记录（最终决断参考）
	round1 := task.RoundResults[0]
	if round1.RejectedCount != 1 || round1.ApprovedCount != 1 {
		t.Fatalf("第1轮票数应记录驳回1通过1，实际 通过%d 驳回%d", round1.ApprovedCount, round1.RejectedCount)
	}
	// r1 和 admin1 都应在第2轮待我审核再次看到该题
	tasks, _ := svc.MyTasks(ctx, "r1", false)
	if len(tasks) != 1 || tasks[0].RoundIndex != 2 {
		t.Fatalf("r1 第2轮应再次看到，实际 %d 项", len(tasks))
	}
	tasksAdmin, _ := svc.MyTasks(ctx, "admin1", true)
	if len(tasksAdmin) != 1 || tasksAdmin[0].RoundIndex != 2 {
		t.Fatalf("admin 第2轮应再次看到，实际 %d 项", len(tasksAdmin))
	}
	// 第2轮：r2 投通过 → 最终轮 → 待决断 → admin1 决断（能看到第1轮的驳回票）
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved})
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusConflict {
		t.Fatalf("第2轮完成后应进入最终待决断，实际 %s", task.Status)
	}
	decisions, _ := svc.MyDecisions(ctx, "admin1")
	if len(decisions) != 1 {
		t.Fatalf("把关人应有 1 项待决断，实际 %d", len(decisions))
	}
	// 决断时可见第1轮驳回票
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved, Opinion: "综合各轮票数，通过"}); err != nil {
		t.Fatalf("决断失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusPublished {
		t.Fatalf("决断通过应入库，实际 %s", task.Status)
	}
}

// TestVetoRule 测试：一票否决流程——任一驳回直接驳回；全员通过才过轮。
func TestVetoRule(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2", "r3"}}
	svc, _, questionStore := newTestService(reviewers, map[string]bool{})
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := domain.ReviewFlowConfig{
		ID:       "flow-veto",
		Name:     "一票否决流程",
		VoteRule: "veto",
		Rounds: []domain.RoundConfig{
			{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 0},
		},
	}
	svc.reviewStore.SaveFlowConfig(ctx, flow)

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-veto")
	// r1 驳回 → 直接驳回（无需等其他人）
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected, Opinion: "一票否决"}); err != nil {
		t.Fatalf("r1 驳回失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusRejected {
		t.Fatalf("一票否决应直接驳回，实际 %s", task.Status)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusRejected {
		t.Fatalf("题目应驳回，实际 %s", q.Status)
	}
}

// TestVetoAllPass 测试：一票否决流程全员通过 → 过轮 → 最终待决断。
func TestVetoAllPass(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2", "r3"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := domain.ReviewFlowConfig{
		ID:               "flow-veto",
		Name:             "一票否决流程",
		VoteRule:         "veto",
		FinalReviewerIDs: []string{"admin1"},
		Rounds: []domain.RoundConfig{
			{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 0},
		},
	}
	svc.reviewStore.SaveFlowConfig(ctx, flow)

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-veto")
	// 全员通过 → 过轮 → 最终待决断
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r3", Action: domain.StatusApproved})
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusConflict {
		t.Fatalf("全员通过后应进入最终待决断，实际 %s", task.Status)
	}
}

// TestMyTasksAndDecisions 测试：待我审核只含自己参与的轮次；待决断任务走 MyDecisions；
// 第1轮投完进入第2轮后，该题再次出现在待我审核。
func TestMyTasksAndDecisions(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2", "r3"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := domain.ReviewFlowConfig{
		ID:               "flow-2r",
		Name:             "两轮流程",
		FinalReviewerIDs: []string{"admin1"},
		Rounds: []domain.RoundConfig{
			{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 1},
			{RoundNumber: 2, Name: "复审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 1},
		},
	}
	svc.reviewStore.SaveFlowConfig(ctx, flow)

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-2r")

	// r1 第1轮看到任务
	tasks, _ := svc.MyTasks(ctx, "r1", false)
	if len(tasks) != 1 || tasks[0].RoundIndex != 1 {
		t.Fatalf("r1 第1轮应在待我审核，实际 %d 项", len(tasks))
	}
	// r1 投第1轮通过
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	// 任务进入第2轮，r1 再次在待我审核看到（当前轮=2）
	tasks, _ = svc.MyTasks(ctx, "r1", false)
	if len(tasks) != 1 || tasks[0].RoundIndex != 2 {
		round := 0
		if len(tasks) > 0 {
			round = tasks[0].RoundIndex
		}
		t.Fatalf("第1轮投完进入第2轮后 r1 应再次看到（round=2），实际 %d 项 round=%d", len(tasks), round)
	}
	// 把关人 admin1 不在第2轮 assigned（仅 r1/r2/r3）→ 待我审核为空
	tasksAdmin, _ := svc.MyTasks(ctx, "admin1", true)
	if len(tasksAdmin) != 0 {
		t.Fatalf("把关人未参与该轮，待我审核应为空，实际 %d 项", len(tasksAdmin))
	}

	// r2 投第2轮 → 最终轮完成 → 待决断（admin1 在把关名单）
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved})
	// r1/r2 的待我审核：任务已进入待决断 → 不再显示
	tasks, _ = svc.MyTasks(ctx, "r1", false)
	if len(tasks) != 0 {
		t.Fatalf("进入待决断后 r1 待我审核应为空，实际 %d 项", len(tasks))
	}
	// admin1 的待决断列表应包含该任务
	decisions, _ := svc.MyDecisions(ctx, "admin1")
	if len(decisions) != 1 || !decisions[0].NeedsDecision {
		t.Fatalf("把关人待决断应有 1 项，实际 %d 项", len(decisions))
	}
	// 非把关人看不到待决断（MyDecisions 只给把关人用；此处验证名单校验：admin2 不在名单）
	decisions2, _ := svc.MyDecisions(ctx, "r1")
	if len(decisions2) != 0 {
		t.Fatalf("非把关人不应有待决断任务，实际 %d 项", len(decisions2))
	}
}

// TestTwoRoundsFinalDecision 测试用户场景：2轮流程，每轮1人通过即可，
// 两轮都通过后 → 才进入最终待决断 → 把关人决断通过 → 直接入库。
// 中途轮次通过时把关人不应介入（无待决断）。
func TestTwoRoundsFinalDecision(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2", "r3"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := domain.ReviewFlowConfig{
		ID:               "flow-2r",
		Name:             "两轮流程",
		FinalReviewerIDs: []string{"admin1"},
		Rounds: []domain.RoundConfig{
			{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 1},
			{RoundNumber: 2, Name: "复审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 1},
		},
	}
	svc.reviewStore.SaveFlowConfig(ctx, flow)

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-2r")
	if len(task.AssignedTo) != 3 {
		t.Fatalf("第1轮应3人，实际 %d", len(task.AssignedTo))
	}

	// 第1轮：r1 1票通过（required=1）→ 提前通过进第2轮，不进入待决断
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("r1 第1轮审核失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusReviewing || task.CurrentRound != 2 {
		t.Fatalf("第1轮通过后应进入第2轮 reviewing，实际 status=%s round=%d", task.Status, task.CurrentRound)
	}

	// 第2轮：r2 1票通过 → 所有轮次完成 → 进入最终待决断（此刻把关人才出现）
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("r2 第2轮审核失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusConflict {
		t.Fatalf("两轮都通过后应进入最终待决断 conflict，实际 %s", task.Status)
	}

	// 把关人决断通过 → 直接入库
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved, Opinion: "终审通过"}); err != nil {
		t.Fatalf("最终决断失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusPublished {
		t.Fatalf("最终决断通过应直接入库 published，实际 %s", task.Status)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusPublished {
		t.Fatalf("题目应已入库 published，实际 %s", q.Status)
	}
	// 两轮都标记通过
	if !task.RoundResults[0].Passed || !task.RoundResults[1].Passed {
		t.Fatal("两轮都应标记为通过")
	}
}

// TestTwoRoundsDisagreement 测试：中途轮次出现分歧（全员投完仍有反对票）→ 该轮进入待决断，
// 把关人决断通过后进入下一轮。（本场景门槛=全员，1人通过不会提前过轮）
func TestTwoRoundsDisagreement(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2", "r3"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := domain.ReviewFlowConfig{
		ID:               "flow-2r",
		Name:             "两轮流程",
		FinalReviewerIDs: []string{"admin1"},
		Rounds: []domain.RoundConfig{
			{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 3},
			{RoundNumber: 2, Name: "复审", ExpertIDs: []string{"r1", "r2", "r3"}, RequiredCount: 1},
		},
	}
	svc.reviewStore.SaveFlowConfig(ctx, flow)

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-2r")
	// 第1轮：r1 通过、r2 驳回、r3 通过 → 全员投完有反对票 → 待决断
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected, Opinion: "有异议"})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r3", Action: domain.StatusApproved})
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusConflict {
		t.Fatalf("第1轮分歧应进入待决断，实际 %s", task.Status)
	}
	// 把关人决断通过 → 进入第2轮
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("决断失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusReviewing || task.CurrentRound != 2 {
		t.Fatalf("决断通过后应进入第2轮，实际 status=%s round=%d", task.Status, task.CurrentRound)
	}
}

// TestAllVoteGoesConflict 测试：单轮流程全员一致通过（最终轮完成）→ 进入最终待决断，
// 把关人决断通过后才入库。
func TestAllVoteGoesConflict(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("r2 审核失败: %v", err)
	}
	// 全员投完 → 即使全票通过也进入待决断
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusConflict {
		t.Fatalf("全员投票完成后应为待决断 conflict（等待把关人），实际 %s", task.Status)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusReviewing {
		t.Fatalf("待决断时题目应保持 reviewing，实际 %s", q.Status)
	}
	// 把关人决断通过 → 直接入库
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved}); err != nil {
		t.Fatalf("决断失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusPublished {
		t.Fatalf("决断通过后应直接入库 published，实际 %s", task.Status)
	}
}

// TestSingleOppositionNoImmediateReturn 测试：单人反对不再立即退回。
func TestSingleOppositionNoImmediateReturn(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2", "r3"}}
	svc, _, questionStore := newTestService(reviewers, map[string]bool{})
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected}); err != nil {
		t.Fatalf("r1 审核失败: %v", err)
	}
	task, _ = svc.reviewStore.GetTask(ctx, task.ID)
	if task.Status != domain.StatusReviewing {
		t.Fatalf("单人反对不应立即退回，实际 %s", task.Status)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusReviewing {
		t.Fatalf("题目应保持 reviewing，实际 %s", q.Status)
	}
}

// TestNoReviewers 测试：题库没有审题人时提交报错。
func TestNoReviewers(t *testing.T) {
	svc, _, questionStore := newTestService(map[string][]string{}, map[string]bool{})
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-waike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil {
		t.Fatal("无审题人时应报错")
	}
}

// TestDuplicateVote 测试：同一审核人不能重复投票。
func TestDuplicateVote(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	svc, _, questionStore := newTestService(reviewers, map[string]bool{})
	ctx := context.Background()

	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err == nil {
		t.Fatal("重复投票应报错")
	}
}

// TestRevokeFlow 测试：撤销流程下未完成的审核任务，题目恢复提交前状态。
func TestRevokeFlow(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	finalRight := map[string]bool{"admin1": true}
	svc, _, questionStore := newTestService(reviewers, finalRight)
	ctx := context.Background()

	q := testQuestion("bank-neike")
	q.Status = domain.StatusAutoChecked // 提交前状态
	questionStore.SaveQuestion(ctx, *q)
	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if task.QuestionPrevStatus != domain.StatusAutoChecked {
		t.Fatalf("任务应记录提交前状态 auto_checked，实际 %s", task.QuestionPrevStatus)
	}
	// r1 投了一票（未完成）
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})

	// 撤销
	res, err := svc.RevokeFlow(ctx, "flow-test")
	if err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if res.Revoked != 1 || res.Restored != 1 {
		t.Fatalf("应撤销1恢复1，实际 revoked=%d restored=%d", res.Revoked, res.Restored)
	}
	// 题目恢复
	q2, _ := questionStore.GetQuestion(ctx, "q1")
	if q2.Status != domain.StatusAutoChecked {
		t.Fatalf("题目应恢复 auto_checked，实际 %s", q2.Status)
	}
	// 任务已删除
	if _, err := svc.reviewStore.GetTask(ctx, task.ID); err != nil || true {
		gone, _ := svc.reviewStore.GetTask(ctx, task.ID)
		if gone != nil {
			t.Fatal("任务应已被删除")
		}
	}
	// 撤销后可重新提交
	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err != nil {
		t.Fatalf("撤销后应可重新提交: %v", err)
	}
}

// TestRevokeFlowKeepsTerminal 测试：撤销不影响终态任务（审核已结束的题目不变）。
func TestRevokeFlowKeepsTerminal(t *testing.T) {
	reviewers := map[string][]string{"bank-neike": {"r1", "r2"}}
	svc, _, questionStore := newTestService(reviewers, map[string]bool{})
	ctx := context.Background()

	// 题目A：已审核通过（终态）
	qa := testQuestion("bank-neike")
	qa.ID = "q-approved"
	qa.Status = domain.StatusApproved
	questionStore.SaveQuestion(ctx, *qa)
	// 题目B：审核中（非终态）
	qb := testQuestion("bank-neike")
	qb.ID = "q-reviewing"
	qb.Status = domain.StatusAutoChecked
	questionStore.SaveQuestion(ctx, *qb)

	svc.reviewStore.SaveFlowConfig(ctx, testFlow())

	// 模拟A的终态任务：直接构造 approved 任务
	ta := domain.ReviewTask{
		ID:                 "task-a",
		QuestionID:         "q-approved",
		FlowID:             "flow-test",
		CurrentRound:       1,
		Status:             domain.StatusApproved,
		QuestionPrevStatus: domain.StatusAIDraft,
		RoundResults:       []domain.RoundResult{{RoundNumber: 1, Passed: true}},
	}
	svc.reviewStore.SaveTask(ctx, ta)
	// B 提交审核（非终态）
	tb, _ := svc.SubmitQuestion(ctx, "q-reviewing", "flow-test")

	res, err := svc.RevokeFlow(ctx, "flow-test")
	if err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if res.Revoked != 1 || res.Kept != 1 {
		t.Fatalf("应撤销1保留1，实际 revoked=%d kept=%d", res.Revoked, res.Kept)
	}
	// 终态任务保留、题目不变
	ta2, _ := svc.reviewStore.GetTask(ctx, "task-a")
	if ta2 == nil || ta2.Status != domain.StatusApproved {
		t.Fatal("终态任务应保留")
	}
	qa2, _ := questionStore.GetQuestion(ctx, "q-approved")
	if qa2.Status != domain.StatusApproved {
		t.Fatalf("终态题目不应变，实际 %s", qa2.Status)
	}
	// 非终态题目恢复
	qb2, _ := questionStore.GetQuestion(ctx, "q-reviewing")
	if qb2.Status != domain.StatusAutoChecked {
		t.Fatalf("非终态题目应恢复 auto_checked，实际 %s", qb2.Status)
	}
	// 非终态任务删除
	if gone, _ := svc.reviewStore.GetTask(ctx, tb.ID); gone != nil {
		t.Fatal("非终态任务应被删除")
	}
}
