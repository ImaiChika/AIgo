package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// TestReviewTodoQuerySemantics 在真实 PostgreSQL 上验证审核个人待办预过滤语义：
// 按题去重取最新任务（去重发生在全部任务上，最新任务为终态时旧 reviewing 任务不复活）、
// 当前轮分配、把关人名单（空名单=任意把关人；FinalAny 不过滤）。
// 本轮已投票、题目出题人归属等细粒度判断由 review 服务在 Go 侧完成，不在本查询范围。
func TestReviewTodoQuerySemantics(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	now := time.Now()

	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "todo-flow", Name: "待办测试流程", CreatedAt: now,
		Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "第一轮"}},
	}); err != nil {
		t.Fatal(err)
	}

	saveTask := func(t *testing.T, task domain.ReviewTask) {
		t.Helper()
		if task.CreatedAt.IsZero() {
			task.CreatedAt = now
		}
		task.UpdatedAt = now
		if task.QuestionVersion < 1 {
			task.QuestionVersion = 1
		}
		if task.FlowID == "" {
			task.FlowID = "todo-flow"
		}
		// 任务必须绑定有效题目版本：为每个任务落一道对应题目
		q := domain.A2Question{
			ID: task.QuestionID, ClinicalStem: "待办测试题干 " + task.QuestionID,
			Options: []domain.Option{{Label: "A", Text: "选项"}}, Answer: "A",
			Status: domain.StatusReviewing, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatalf("保存题目 %s 失败: %v", task.QuestionID, err)
		}
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatalf("保存任务 %s 失败: %v", task.ID, err)
		}
	}

	// t1：reviewing，分配给 r1，未投票（results 为空轮）→ 模式 A 命中
	saveTask(t, domain.ReviewTask{
		ID: "todo-t1", QuestionID: "todo-q1", Status: domain.StatusReviewing,
		CurrentRound: 1, AssignedTo: []string{"r1"}, Attempt: 1,
		RoundResults: []domain.RoundResult{{RoundNumber: 1, Reviews: []domain.ExpertReview{}}},
	})
	// t2：reviewing，分配给 r2 → 模式 A 不命中（r1 视角）
	saveTask(t, domain.ReviewTask{
		ID: "todo-t2", QuestionID: "todo-q2", Status: domain.StatusReviewing,
		CurrentRound: 1, AssignedTo: []string{"r2"}, Attempt: 1,
		RoundResults: []domain.RoundResult{{RoundNumber: 1}},
	})
	// t3：reviewing，分配给 r1，r1 已投过本轮 → SQL 仍返回（投票过滤在 Go 侧）
	saveTask(t, domain.ReviewTask{
		ID: "todo-t3", QuestionID: "todo-q3", Status: domain.StatusReviewing,
		CurrentRound: 1, AssignedTo: []string{"r1"}, Attempt: 1,
		RoundResults: []domain.RoundResult{{RoundNumber: 1, Reviews: []domain.ExpertReview{
			{ExpertID: "r1", Conclusion: domain.StatusApproved, ReviewedAt: now},
		}}},
	})
	// t4 旧：同一题目的较早 reviewing 任务；t4 新：终态 published。
	// 去重发生在全部任务上 → 最新任务为终态，旧 reviewing 任务不得复活。
	saveTask(t, domain.ReviewTask{
		ID: "todo-t4-old", QuestionID: "todo-q4", Status: domain.StatusReviewing,
		CurrentRound: 1, AssignedTo: []string{"r1"}, Attempt: 1,
		CreatedAt: now.Add(-2 * time.Hour),
		RoundResults: []domain.RoundResult{{RoundNumber: 1}},
	})
	saveTask(t, domain.ReviewTask{
		ID: "todo-t4-new", QuestionID: "todo-q4", Status: domain.StatusPublished,
		CurrentRound: 1, AssignedTo: []string{"r1"}, Attempt: 1,
		CreatedAt: now.Add(-1 * time.Hour),
		RoundResults: []domain.RoundResult{{RoundNumber: 1}},
	})
	// t5：conflict，把关人名单为空（任意把关人）→ 两种模式都命中
	saveTask(t, domain.ReviewTask{
		ID: "todo-t5", QuestionID: "todo-q5", Status: domain.StatusConflict,
		CurrentRound: 1, AssignedTo: []string{"r1"}, FinalReviewerIDs: []string{}, Attempt: 1,
		RoundResults: []domain.RoundResult{{RoundNumber: 1}},
	})
	// t6：conflict，把关人名单=[r2] → FinalReviewer=r1 不命中；FinalAny 命中
	saveTask(t, domain.ReviewTask{
		ID: "todo-t6", QuestionID: "todo-q6", Status: domain.StatusConflict,
		CurrentRound: 1, AssignedTo: []string{"r2"}, FinalReviewerIDs: []string{"r2"}, Attempt: 1,
		RoundResults: []domain.RoundResult{{RoundNumber: 1}},
	})
	// t7：revision_required → 模式 C 命中
	saveTask(t, domain.ReviewTask{
		ID: "todo-t7", QuestionID: "todo-q7", Status: domain.StatusRevisionRequired,
		CurrentRound: 1, Attempt: 1,
	})

	taskIDs := func(tasks []domain.ReviewTask) map[string]bool {
		out := map[string]bool{}
		for _, t := range tasks {
			out[t.ID] = true
		}
		return out
	}

	// 模式 A：待我审核（reviewing + 分配给我，按题去重）
	tasks, err := store.ListReviewTodoTasks(ctx, storage.ReviewTodoQuery{
		Statuses:        []string{string(domain.StatusReviewing)},
		AssignedTo:      "r1",
		DedupByQuestion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := taskIDs(tasks)
	if !got["todo-t1"] || !got["todo-t3"] {
		t.Fatalf("模式 A 应命中 t1/t3，实际 %v", got)
	}
	if got["todo-t2"] || got["todo-t4-old"] || got["todo-t4-new"] {
		t.Fatalf("模式 A 不应命中他人任务或被终态覆盖的旧任务，实际 %v", got)
	}

	// 模式 B：待我决断（conflict + 把关人名单含我或名单为空）
	tasks, err = store.ListReviewTodoTasks(ctx, storage.ReviewTodoQuery{
		Statuses:        []string{string(domain.StatusConflict)},
		FinalReviewer:   "r1",
		DedupByQuestion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got = taskIDs(tasks)
	if !got["todo-t5"] || got["todo-t6"] {
		t.Fatalf("模式 B 应命中空名单 t5、排除他人名单 t6，实际 %v", got)
	}

	// 模式 B'：系统管理员（FinalAny）→ 全部 conflict
	tasks, err = store.ListReviewTodoTasks(ctx, storage.ReviewTodoQuery{
		Statuses:        []string{string(domain.StatusConflict)},
		FinalAny:        true,
		DedupByQuestion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got = taskIDs(tasks)
	if !got["todo-t5"] || !got["todo-t6"] {
		t.Fatalf("模式 B' 应命中全部 conflict，实际 %v", got)
	}

	// 模式 C：待我修改（revision_required，无去重）
	tasks, err = store.ListReviewTodoTasks(ctx, storage.ReviewTodoQuery{
		Statuses: []string{string(domain.StatusRevisionRequired)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got = taskIDs(tasks)
	if !got["todo-t7"] {
		t.Fatalf("模式 C 应命中 t7，实际 %v", got)
	}
}


// TestWithTransactionAuditAtomicity 验证通用事务能力：业务写入与审计写入
// 要么同时提交、要么一起回滚（关键配置操作的审计一致性口径）。
func TestWithTransactionAuditAtomicity(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)

	// 成功路径：题库写入 + 审计写入同事务提交
	err := store.WithTransaction(ctx, func(txCtx context.Context) error {
		if err := store.SaveBank(txCtx, domain.QuestionBank{ID: "tx-bank-ok", Name: "事务题库", CreatedAt: time.Now()}); err != nil {
			return err
		}
		return store.SaveLog(txCtx, domain.AuditLog{ID: "tx-log-ok", Action: "bank_create", Actor: "tester", CreatedAt: time.Now()})
	})
	if err != nil {
		t.Fatalf("事务提交失败: %v", err)
	}
	if bank, _ := store.GetBank(ctx, "tx-bank-ok"); bank == nil {
		t.Fatal("业务写入应已提交")
	}
	counts, _ := store.ListLogs(ctx, 10)
	sawLog := false
	for _, l := range counts {
		if l.ID == "tx-log-ok" {
			sawLog = true
		}
	}
	if !sawLog {
		t.Fatal("审计写入应已提交")
	}

	// 回滚路径：审计步骤返回错误 → 业务写入一并回滚
	err = store.WithTransaction(ctx, func(txCtx context.Context) error {
		if err := store.SaveBank(txCtx, domain.QuestionBank{ID: "tx-bank-rollback", Name: "回滚题库", CreatedAt: time.Now()}); err != nil {
			return err
		}
		return fmt.Errorf("注入的审计失败")
	})
	if err == nil {
		t.Fatal("注入错误应导致事务失败")
	}
	if bank, _ := store.GetBank(ctx, "tx-bank-rollback"); bank != nil {
		t.Fatal("事务回滚后业务写入不得残留")
	}
}
