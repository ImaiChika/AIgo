package review

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
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
			{Label: "D", Text: "气胸"},
		},
		Answer:  "A",
		Status:  domain.StatusAIDraft,
		Version: 1,
		BankIDs: []string{bankID},
	}
}

func TestWithQuestionMutationSerializesQuestionEdits(t *testing.T) {
	svc, _, _ := newTestService(map[string][]string{}, map[string]bool{})
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	secondEntered := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() {
		firstDone <- svc.WithQuestionMutation(ctx, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	go func() {
		secondDone <- svc.WithQuestionMutation(ctx, func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("第二个题目变更不应在第一个变更释放审核锁前进入")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("第一个题目变更失败: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("第二个题目变更失败: %v", err)
	}
}

func TestSubmitRejectsInvalidQuestion(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()

	q := testQuestion("bank-neike")
	q.Options = q.Options[:3]
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SubmitQuestion(ctx, q.ID, "flow-test"); !errors.Is(err, domain.ErrInvalidQuestion) {
		t.Fatalf("不合格题应被拒绝提交，实际错误: %v", err)
	}
	stored, _ := questionStore.GetQuestion(ctx, q.ID)
	if stored.Status != domain.StatusAIDraft {
		t.Fatalf("拒绝提交后题目状态不应变化，实际 %s", stored.Status)
	}
	task, _ := reviewStore.GetTaskByQuestionID(ctx, q.ID)
	if task != nil {
		t.Fatal("拒绝提交后不应创建审核任务")
	}
}

func TestFinalizeRejectsQuestionVersionChangedDuringReview(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	if err := questionStore.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected, Opinion: "答案与指南不符"}); err != nil {
		t.Fatal(err)
	}

	q, _ := questionStore.GetQuestion(ctx, "q1")
	q.ClinicalStem += "（绕过正常审核编辑产生的新内容版本）"
	q.Version++
	questionStore.ForceQuestionForTest(*q)
	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved}); !errors.Is(err, domain.ErrQuestionVersionConflict) {
		t.Fatalf("审核任务绑定旧版本时不应通过最终决断，实际错误: %v", err)
	}

	storedTask, _ := reviewStore.GetTask(ctx, task.ID)
	if storedTask.Status != domain.StatusConflict {
		t.Fatalf("版本冲突后任务应保持待决断，实际 %s", storedTask.Status)
	}
	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 2 {
		t.Fatalf("版本冲突不应写入最终决断记录，实际记录数 %d", len(records))
	}
}

func TestPublishRejectsInvalidQuestion(t *testing.T) {
	svc, _, questionStore := newTestService(nil, nil)
	ctx := context.Background()
	q := testQuestion("bank-neike")
	q.Status = domain.StatusPublished
	q.Options = q.Options[:3]
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}

	if err := svc.PublishQuestion(ctx, q.ID); !errors.Is(err, domain.ErrInvalidQuestion) {
		t.Fatalf("不合格题不应发布，实际错误: %v", err)
	}
	stored, _ := questionStore.GetQuestion(ctx, q.ID)
	if stored.Status != domain.StatusPublished {
		t.Fatalf("拒绝发布后状态不应变化，实际 %s", stored.Status)
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

func TestSubmitQuestionForOwnerAllowsUnclassifiedQuestion(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(nil, nil)
	ctx := context.Background()
	q := testQuestion("")
	q.Status = domain.StatusAIReviewed
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	flow := testFlow()
	flow.Rounds[0].ExpertIDs = []string{"reviewer-1"}
	if err := reviewStore.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}

	task, err := svc.SubmitQuestionForOwner(ctx, q.ID, flow.ID)
	if err != nil {
		t.Fatalf("命题老师提交未归类题目失败: %v", err)
	}
	if task.SubmissionBankID != "" || task.Status != domain.StatusReviewing {
		t.Fatalf("新送审任务不应强制分类子题库: %+v", task)
	}
	stored, _ := questionStore.GetQuestion(ctx, q.ID)
	if stored.Status != domain.StatusReviewing {
		t.Fatalf("送审后题目应进入审核中，实际 %s", stored.Status)
	}
}

func TestConcurrentSubmitCreatesSingleTask(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	if err := questionStore.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("并发送审只应成功一次，实际成功 %d 次", got)
	}
	tasks, err := reviewStore.ListAllTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("并发送审后只应存在一个任务，实际 %d 个", len(tasks))
	}
}

func TestConcurrentVotesDoNotOverwrite(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	if err := questionStore.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, reviewerID := range []string{"r1", "r2"} {
		reviewerID := reviewerID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.Review(ctx, ReviewRequest{
				TaskID:   task.ID,
				ExpertID: reviewerID,
				Action:   domain.StatusApproved,
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发投票失败: %v", err)
		}
	}

	stored, err := reviewStore.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	round := stored.RoundResults[0]
	if len(round.Reviews) != 2 || round.ApprovedCount != 2 {
		t.Fatalf("并发投票被覆盖: reviews=%d approved=%d", len(round.Reviews), round.ApprovedCount)
	}
	if stored.Status != domain.StatusConflict {
		t.Fatalf("最终轮全员通过后应待决断，实际 %s", stored.Status)
	}
	records, err := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("并发投票应保留两条记录，实际 %d", len(records))
	}
}

func TestReviewTaskRejectsNewerQuestionVersion(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	reviewStore.SaveFlowConfig(ctx, testFlow())
	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatal(err)
	}
	if task.QuestionVersion != 1 {
		t.Fatalf("审核任务应绑定版本1，实际 %d", task.QuestionVersion)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	q.ClinicalStem += " 新版本内容"
	q.Version++
	questionStore.ForceQuestionForTest(*q) // 模拟绕过应用层的直接 SQL/历史代码篡改。
	pending, _ := svc.MyTasks(ctx, "r1", false)
	if len(pending) != 1 || pending[0].CanVote || !pending[0].VersionMismatch {
		t.Fatalf("版本冲突任务应保留可见但禁止投票: %+v", pending)
	}
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); !errors.Is(err, domain.ErrQuestionVersionConflict) {
		t.Fatalf("旧版本审核任务必须拒绝审核新内容，实际 %v", err)
	}
	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 0 {
		t.Fatalf("版本冲突不应写入审核记录，实际 %d", len(records))
	}
}

func TestSubmitQuestionRollsBackWhenTaskSaveFails(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	if err := questionStore.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}
	reviewStore.FailNext("save_task", errors.New("injected task failure"))

	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil {
		t.Fatal("任务写入失败时送审应失败")
	}
	storedQuestion, _ := questionStore.GetQuestion(ctx, "q1")
	if storedQuestion.Status != domain.StatusAIDraft {
		t.Fatalf("送审回滚后题目应保持草稿，实际 %s", storedQuestion.Status)
	}
	tasks, _ := reviewStore.ListAllTasks(ctx)
	if len(tasks) != 0 {
		t.Fatalf("送审回滚后不应存在任务，实际 %d", len(tasks))
	}
}

func TestReviewRollsBackRecordWhenTaskUpdateFails(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	reviewStore.SaveFlowConfig(ctx, testFlow())
	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatal(err)
	}
	reviewStore.FailNext("update_task", errors.New("injected task update failure"))

	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err == nil {
		t.Fatal("任务更新失败时投票应失败")
	}
	storedTask, _ := reviewStore.GetTask(ctx, task.ID)
	if storedTask.Status != domain.StatusReviewing || len(storedTask.RoundResults[0].Reviews) != 0 || storedTask.RoundResults[0].ApprovedCount != 0 {
		t.Fatalf("投票回滚后任务不应包含本次票，status=%s reviews=%d approved=%d", storedTask.Status, len(storedTask.RoundResults[0].Reviews), storedTask.RoundResults[0].ApprovedCount)
	}
	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 0 {
		t.Fatalf("投票回滚后不应留下审核记录，实际 %d", len(records))
	}
}

func TestReviewRollsBackTaskAndRecordWhenQuestionSaveFails(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	flow := testFlow()
	flow.VoteRule = "veto"
	reviewStore.SaveFlowConfig(ctx, flow)
	task, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatal(err)
	}
	questionStore.FailNextSaveQuestion(errors.New("injected question failure"))

	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected}); err == nil {
		t.Fatal("题目状态写入失败时投票应失败")
	}
	storedTask, _ := reviewStore.GetTask(ctx, task.ID)
	if storedTask.Status != domain.StatusReviewing || len(storedTask.RoundResults[0].Reviews) != 0 {
		t.Fatalf("题目写入失败后任务应整体回滚，status=%s reviews=%d", storedTask.Status, len(storedTask.RoundResults[0].Reviews))
	}
	storedQuestion, _ := questionStore.GetQuestion(ctx, "q1")
	if storedQuestion.Status != domain.StatusReviewing {
		t.Fatalf("题目写入失败后应保持审核中，实际 %s", storedQuestion.Status)
	}
	records, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(records) != 0 {
		t.Fatalf("题目写入失败后不应留下审核记录，实际 %d", len(records))
	}
}

func TestFinalizeRollsBackPublishedQuestionWhenTaskUpdateFails(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	reviewStore.SaveFlowConfig(ctx, testFlow())
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved})
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected, Opinion: "选项设置有歧义"})
	beforeRecords, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	reviewStore.FailNext("update_task", errors.New("injected final task failure"))

	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved}); err == nil {
		t.Fatal("最终任务更新失败时决断应失败")
	}
	storedTask, _ := reviewStore.GetTask(ctx, task.ID)
	if storedTask.Status != domain.StatusConflict || storedTask.FinalDecision != nil {
		t.Fatalf("决断回滚后任务应保持待决断，status=%s decision=%v", storedTask.Status, storedTask.FinalDecision)
	}
	storedQuestion, _ := questionStore.GetQuestion(ctx, "q1")
	if storedQuestion.Status != domain.StatusReviewing {
		t.Fatalf("决断回滚后题目不能入库，实际 %s", storedQuestion.Status)
	}
	afterRecords, _ := reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if len(afterRecords) != len(beforeRecords) {
		t.Fatalf("决断回滚后记录数不应增加，之前 %d，之后 %d", len(beforeRecords), len(afterRecords))
	}
}

func TestResubmitRollsBackTaskWhenQuestionSaveFails(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-neike": {"r1", "r2"}},
		map[string]bool{"admin1": true},
	)
	ctx := context.Background()
	questionStore.SaveQuestion(ctx, *testQuestion("bank-neike"))
	reviewStore.SaveFlowConfig(ctx, testFlow())
	task, _ := svc.SubmitQuestion(ctx, "q1", "flow-test")
	task.Status = domain.StatusRevisionRequired
	task.RoundResults[0].Reviews = []domain.ExpertReview{{ExpertID: "r1", Conclusion: domain.StatusRevisionRequired}}
	task.RoundResults[0].RevisionCount = 1
	reviewStore.UpdateTask(ctx, *task)
	q, _ := questionStore.GetQuestion(ctx, "q1")
	// 模拟把关人退回修改后的状态切换；实际生产路径由 Finalize 负责写回 ai_reviewed。
	q.Status = domain.StatusAIReviewed
	questionStore.ForceQuestionForTest(*q)
	q.ClinicalStem += "（修改后保存）"
	q.Version++
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	questionStore.FailNextSaveQuestion(errors.New("injected resubmit question failure"))

	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil {
		t.Fatal("题目状态写入失败时重提应失败")
	}
	storedTask, _ := reviewStore.GetTask(ctx, task.ID)
	if storedTask.Status != domain.StatusRevisionRequired || storedTask.RoundResults[0].RevisionCount != 1 || len(storedTask.RoundResults[0].Reviews) != 1 {
		t.Fatalf("重提回滚后任务应保持退修票，status=%s revision=%d reviews=%d", storedTask.Status, storedTask.RoundResults[0].RevisionCount, len(storedTask.RoundResults[0].Reviews))
	}
	storedQuestion, _ := questionStore.GetQuestion(ctx, "q1")
	if storedQuestion.Status != domain.StatusAIReviewed || storedQuestion.Version != 2 {
		t.Fatalf("重提回滚后题目应保持已检查的新版本，实际 status=%s version=%d", storedQuestion.Status, storedQuestion.Version)
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
	// 决断通过即自动进入正式题库
	if q.Tier() != domain.TierFormal {
		t.Fatalf("决断通过的题目应进入正式题库，实际 %s", q.Tier())
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
	svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRejected, Opinion: "选项设置有歧义"})

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
	// 驳回为终态锁定：进入淘汰题库
	if q.Tier() != domain.TierEliminated {
		t.Fatalf("驳回的题目应进入淘汰题库，实际 %s", q.Tier())
	}

	// 驳回为终态锁定：不允许修改与重新提交
	if _, err := svc.SubmitQuestion(ctx, "q1", "flow-test"); err == nil {
		t.Fatal("驳回后的题目应禁止重新提交审核")
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

	if err := svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusRevisionRequired, Opinion: "题干信息不完整，退回补充"}); err != nil {
		t.Fatalf("决断失败: %v", err)
	}
	q, _ := questionStore.GetQuestion(ctx, "q1")
	if q.Status != domain.StatusAIReviewed {
		t.Fatalf("退回修改后题目应恢复 ai_reviewed，实际 %s", q.Status)
	}
	// 退回修改仍在过程题库流转
	if q.Tier() != domain.TierWorking {
		t.Fatalf("退回修改的题目应留在过程题库，实际 %s", q.Tier())
	}

	// 生成者按退修意见修改（版本递增，状态保持 ai_reviewed）
	q.ClinicalStem += "（按退修意见修改）"
	q.Version++
	questionStore.SaveQuestion(ctx, *q)
	task2, err := svc.SubmitQuestion(ctx, "q1", "flow-test")
	if err != nil {
		t.Fatalf("重新提交失败: %v", err)
	}
	if task2.Status != domain.StatusReviewing {
		t.Fatalf("重提后应为 reviewing，实际 %s", task2.Status)
	}
	if task2.QuestionVersion != q.Version {
		t.Fatalf("重提任务应绑定修改后的版本 %d，实际 %d", q.Version, task2.QuestionVersion)
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
	adminDecisions, _ := svc.MyDecisionsForViewer(ctx, "system-admin", true)
	if len(adminDecisions) != 1 {
		t.Fatalf("系统管理员应能看到全部待决断任务，实际 %d 项", len(adminDecisions))
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
		t.Fatalf("题目应已通过 published，实际 %s", q.Status)
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
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRejected, Opinion: "题干描述与考点不符"}); err != nil {
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

func TestSubmissionBankSnapshotControlsReviewerRouting(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-a": {"reviewer-a"}, "bank-b": {"reviewer-b"}},
		map[string]bool{},
	)
	ctx := context.Background()
	q := testQuestion("bank-a")
	q.BankIDs = []string{"bank-a", "bank-b"}
	q.Status = domain.StatusAIReviewed
	if err := questionStore.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	flow := testFlow()
	flow.Rounds = []domain.RoundConfig{
		{RoundNumber: 1, Name: "初审", RequiredCount: 1},
		{RoundNumber: 2, Name: "复审", RequiredCount: 1},
	}
	if err := reviewStore.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}

	task, err := svc.SubmitQuestionForBank(ctx, q.ID, flow.ID, "bank-b")
	if err != nil {
		t.Fatal(err)
	}
	if task.SubmissionBankID != "bank-b" || len(task.AssignedTo) != 1 || task.AssignedTo[0] != "reviewer-b" {
		t.Fatalf("送审应按显式题库 bank-b 固化并分配，实际 task=%+v", task)
	}

	// 审核期间即使题目当前分类发生变化，下一轮仍使用任务快照 bank-b。
	q.BankIDs = []string{"bank-a"}
	questionStore.ForceQuestionForTest(*q)
	if err := svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpertID: "reviewer-b", Action: domain.StatusApproved}); err != nil {
		t.Fatal(err)
	}
	updated, _ := reviewStore.GetTask(ctx, task.ID)
	if updated.CurrentRound != 2 || len(updated.AssignedTo) != 1 || updated.AssignedTo[0] != "reviewer-b" {
		t.Fatalf("下一轮审核人不应随当前分类漂移，实际 task=%+v", updated)
	}
}

func TestSubmissionBankMustBeExplicitForGenericMultiBankFlow(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(
		map[string][]string{"bank-a": {"reviewer-a"}, "bank-b": {"reviewer-b"}},
		map[string]bool{},
	)
	ctx := context.Background()
	q := testQuestion("bank-a")
	q.BankIDs = []string{"bank-a", "bank-b"}
	q.Status = domain.StatusAIReviewed
	questionStore.SaveQuestion(ctx, *q)
	reviewStore.SaveFlowConfig(ctx, testFlow())

	if _, err := svc.SubmitQuestion(ctx, q.ID, "flow-test"); err == nil || !strings.Contains(err.Error(), "明确选择") {
		t.Fatalf("多题库题目使用通用流程时应要求显式提交题库，实际错误=%v", err)
	}
}

func TestConflictTaskLocksFlowConfiguration(t *testing.T) {
	svc, reviewStore, _ := newTestService(map[string][]string{"bank-neike": {"r1"}}, map[string]bool{})
	ctx := context.Background()
	flow := testFlow()
	reviewStore.SaveFlowConfig(ctx, flow)
	if err := reviewStore.SaveTask(ctx, domain.ReviewTask{
		ID: "task-conflict", QuestionID: "q1", FlowID: flow.ID, SubmissionBankID: "bank-neike",
		Status: domain.StatusConflict, QuestionVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	flow.Name = "不应生效的新名称"
	if err := svc.UpdateFlow(ctx, flow); err == nil || !strings.Contains(err.Error(), "进行中的审核任务") {
		t.Fatalf("待决断任务存在时不应允许修改流程，实际错误=%v", err)
	}
}

func TestFinalStatusUsesRevisionTaskWhenQuestionReturnedToAIReviewed(t *testing.T) {
	q := domain.A2Question{Status: domain.StatusAIReviewed}
	task := &domain.ReviewTask{Status: domain.StatusRevisionRequired}
	if got := finalStatusOf(q, task); got != "revision_required" {
		t.Fatalf("退回修改题的汇总状态=%s，want revision_required", got)
	}
}

func TestSearchResultsForViewerPaginatesAndUsesScopedStats(t *testing.T) {
	svc, reviewStore, questionStore := newTestService(map[string][]string{}, map[string]bool{})
	ctx := context.Background()
	for _, q := range []domain.A2Question{
		{ID: "q-a", Status: domain.StatusAIReviewed, Version: 1, BankIDs: []string{"bank-a"}, ClinicalStem: "胸痛"},
		{ID: "q-b", Status: domain.StatusPublished, Version: 1, BankIDs: []string{"bank-b"}, ClinicalStem: "腹痛"},
		{ID: "q-c", Status: domain.StatusAIReviewed, Version: 1, BankIDs: []string{"bank-a"}, ClinicalStem: "咳嗽"},
	} {
		questionStore.SaveQuestion(ctx, q)
	}
	reviewStore.SaveTask(ctx, domain.ReviewTask{
		ID: "task-a", QuestionID: "q-a", FlowID: "flow", Status: domain.StatusRevisionRequired,
		QuestionVersion: 1, SubmissionBankID: "bank-a",
	})
	filter := storage.QuestionFilter{
		Tiers: []string{"working"}, BankScope: []string{"bank-a"}, ScopeRestricted: true,
	}
	items, total, stats, err := svc.SearchResultsForViewer(ctx, storage.ReviewResultQuery{
		Filter: filter, StatsFilter: filter, Page: 1, PageSize: 1,
	}, "viewer", false)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 1 || stats.Total != 2 || stats.RevisionRequired != 1 || stats.Published != 0 {
		t.Fatalf("unexpected paged review result: total=%d items=%+v stats=%+v", total, items, stats)
	}
}
