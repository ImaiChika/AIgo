package review

import (
	"context"
	"testing"
	"time"

	"aigo/internal/domain"
)

// 配置级引用守卫的归档语义：流程删除（归档）后不再派生新任务，
// 其轮审人/把关人引用不应再锁定账号收权（否则界面上已看不到该流程，
// 引用永远无法解除）；已提交任务的审核人引用来自任务自身快照，不受影响。
func TestAssignmentReferencesSkipArchivedFlows(t *testing.T) {
	svc, reviewStore, _ := newTestService(map[string][]string{"flow-a": {"rev-1"}}, map[string]bool{})
	ctx := context.Background()
	now := time.Now()

	flow := domain.ReviewFlowConfig{
		ID: "flow-a", Name: "流程A", CreatedAt: now,
		Rounds:           []domain.RoundConfig{{RoundNumber: 1, Name: "一审", ExpertIDs: []string{"rev-1"}, RequiredCount: 1}},
		FinalReviewerIDs: []string{"rev-1"},
	}
	if err := reviewStore.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}

	refs, err := svc.AssignmentReferencesByPermission(ctx, "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.FlowReviewDo) == 0 || len(refs.FlowReviewFinal) == 0 {
		t.Fatalf("未归档流程应产生配置级引用: %+v", refs)
	}

	// 流程删除 = 归档：配置引用随之解除。
	flow.Archived = true
	if err := reviewStore.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	refs, err = svc.AssignmentReferencesByPermission(ctx, "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.FlowReviewDo) != 0 || len(refs.FlowReviewFinal) != 0 {
		t.Fatalf("归档流程不应再产生配置级引用: %+v", refs)
	}

	// 在途任务引用与流程归档无关：仍必须报出（任务快照自带审核人）。
	task := domain.ReviewTask{
		ID: "task-arch-1", QuestionID: "q-arch", FlowID: "flow-a", Status: domain.StatusReviewing,
		CurrentRound: 1, AssignedTo: []string{"rev-1"}, QuestionVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := reviewStore.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	refs, err = svc.AssignmentReferencesByPermission(ctx, "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.ActiveReviewDo) == 0 {
		t.Fatalf("在途任务引用应保留: %+v", refs)
	}
}

func TestTemplateReferencesDoNotLockUsersButFutureRoundsDo(t *testing.T) {
	svc, rs, _ := newTestService(map[string][]string{"": {"r1", "r2"}}, map[string]bool{"final": true})
	ctx := context.Background()
	flow := domain.ReviewFlowConfig{ID: "future-flow", Name: "未来轮次", FinalReviewerIDs: []string{"final"}, Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{"r1"}}, {RoundNumber: 2, ExpertIDs: []string{"r2"}}}, CreatedAt: time.Now()}
	if err := rs.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	refs, err := svc.UserAssignmentReferences(ctx, "r2")
	if err != nil || len(refs) != 0 {
		t.Fatalf("template alone must not permanently lock user: %+v %v", refs, err)
	}
	task := domain.ReviewTask{ID: "future-task", QuestionVersion: 1, QuestionID: "q", FlowID: flow.ID, CurrentRound: 1, Status: domain.StatusReviewing, AssignedTo: []string{"r1"}, FinalReviewerIDs: []string{"final"}, CreatedAt: time.Now()}
	if err := rs.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	future, err := svc.AssignmentReferencesByPermission(ctx, "r2")
	if err != nil || len(future.ActiveReviewDo) == 0 {
		t.Fatalf("future round reviewer must remain protected: %+v %v", future, err)
	}
}
