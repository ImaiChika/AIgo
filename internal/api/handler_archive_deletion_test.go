package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aigo/internal/bank"
	"aigo/internal/domain"
	"aigo/internal/review"
)

// TestArchiveDeletionPolicy 验证删除策略的归档语义：
//   - published / 有审核任务史的题目：删除改为归档（进淘汰题库），
//     版本快照与审核记录完整保留，各视图正确排除/收录；
//   - 无人工审核史的草稿：仍物理删除；
//   - 审核中/待决断：409（先撤销送审或完成决断）；
//   - 已淘汰终态（驳回锁定/已归档）：409（终态留档不可删除）；
//   - 已提交全局分享的题目：409（先完成或结束分享申请）。
func TestArchiveDeletionPolicy(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.bankSvc = bank.NewService(store, store)
	server.reviewSvc = review.NewService(store, store, store, server.authSvc)
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	ctx := context.Background()
	now := time.Now()
	adminID := "user-" + fmt.Sprint(now.UnixNano())

	saveQuestion := func(t *testing.T, id string, status domain.QuestionStatus) {
		t.Helper()
		q := domain.A2Question{
			ID: id, ClinicalStem: "归档测试题干 " + id,
			Options: []domain.Option{{Label: "A", Text: "选项"}}, Answer: "A",
			Status: status, Version: 1, OwnerID: adminID, CreatedBy: "admin",
			CreatedAt: now, UpdatedAt: now,
		}
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatalf("保存 %s: %v", id, err)
		}
	}

	saveQuestion(t, "del-pub", domain.StatusPublished)
	saveQuestion(t, "del-draft", domain.StatusAIDraft)
	saveQuestion(t, "del-revhist", domain.StatusAIReviewed)
	saveQuestion(t, "del-rejected", domain.StatusRejected)
	saveQuestion(t, "del-reviewing", domain.StatusReviewing)

	// 审核任务与记录（含一条已通过票），验证归档后不被级联清除
	saveTaskWithRecord := func(t *testing.T, questionID, taskID string, taskStatus domain.QuestionStatus) {
		t.Helper()
		task := domain.ReviewTask{
			ID: taskID, QuestionID: questionID, FlowID: "del-flow", Status: taskStatus,
			CurrentRound: 1, AssignedTo: []string{"reviewer-1"}, Attempt: 1, QuestionVersion: 1,
		}
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatalf("保存任务 %s: %v", taskID, err)
		}
		record := domain.ReviewRecord{
			ID: taskID + "-rec", TaskID: taskID, QuestionID: questionID,
			RoundNumber: 1, ExpertID: "reviewer-1", Conclusion: domain.StatusApproved,
			Comment: &domain.ReviewComment{Stem: "题干合格"}, CreatedAt: now,
		}
		if err := store.SaveRecord(ctx, record); err != nil {
			t.Fatalf("保存审核记录 %s: %v", taskID, err)
		}
	}
	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "del-flow", Name: "归档测试流程", CreatedAt: now,
		Rounds: []domain.RoundConfig{{RoundNumber: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	saveTaskWithRecord(t, "del-pub", "del-task-pub", domain.StatusPublished)
	saveTaskWithRecord(t, "del-revhist", "del-task-revhist", domain.StatusRevisionRequired)
	saveTaskWithRecord(t, "del-reviewing", "del-task-reviewing", domain.StatusReviewing)

	deleteQuestion := func(t *testing.T, id string) *httptest.ResponseRecorder {
		t.Helper()
		return serveAuthJSON(t, handler, http.MethodDelete, "/api/questions/"+id, adminToken, "198.51.100.1", nil)
	}

	// 1. published → 归档
	resp := deleteQuestion(t, "del-pub")
	if resp.Code != http.StatusOK {
		t.Fatalf("published 删除应 200，实际 %d body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Archived bool `json:"archived"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil || !payload.Archived {
		t.Fatalf("published 删除应返回 archived=true: %s", resp.Body.String())
	}
	archived, _ := store.GetQuestion(ctx, "del-pub")
	if archived == nil || archived.Status != domain.StatusArchived {
		t.Fatalf("published 删除后应保留为 archived，实际 %+v", archived)
	}
	if archived.Version != 1 {
		t.Fatalf("归档不应改变版本号，实际 %d", archived.Version)
	}
	task, _ := store.GetTask(ctx, "del-task-pub")
	if task == nil {
		t.Fatal("归档后审核任务应保留")
	}
	records, _ := store.ListRecordsByTaskID(ctx, "del-task-pub")
	if len(records) != 1 || records[0].Comment == nil || records[0].Comment.Stem != "题干合格" {
		t.Fatalf("归档后审核记录应完整保留: %+v", records)
	}

	// 归档题进入淘汰题库视图、从待审核视图消失
	eliminated := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=eliminated&scope=personal", adminToken, "198.51.100.1", nil).Body.String())
	sawEliminated := false
	for _, q := range eliminated.Questions {
		if q.ID == "del-pub" {
			sawEliminated = true
		}
	}
	if !sawEliminated {
		t.Fatal("归档题应出现在淘汰题库视图")
	}
	working := decodeQuestionPage(t, serveAuthJSON(t, handler, http.MethodGet, "/api/questions?tier=working&scope=personal", adminToken, "198.51.100.1", nil).Body.String())
	for _, q := range working.Questions {
		if q.ID == "del-pub" {
			t.Fatal("归档题不应再出现在待审核题库视图")
		}
	}

	// 2. 无审核史的草稿 → 暂存期（AI 检查未完成）对用户不可见，也不可经 API 删除；
	// 不合格草稿由 AI 检查自动淘汰，人工不接触暂存内容。
	resp = deleteQuestion(t, "del-draft")
	if resp.Code != http.StatusForbidden && resp.Code != http.StatusNotFound {
		t.Fatalf("暂存草稿删除应被拒绝，实际 %d", resp.Code)
	}
	if q, _ := store.GetQuestion(ctx, "del-draft"); q == nil {
		t.Fatal("暂存草稿不应被物理删除")
	}

	// 3. 有审核史的 ai_reviewed → 归档
	resp = deleteQuestion(t, "del-revhist")
	if resp.Code != http.StatusOK {
		t.Fatalf("ai_reviewed 删除应 200，实际 %d body=%s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil || !payload.Archived {
		t.Fatalf("有审核史的题目应归档: %s", resp.Body.String())
	}
	if q, _ := store.GetQuestion(ctx, "del-revhist"); q == nil || q.Status != domain.StatusArchived {
		t.Fatal("有审核史的题目删除后应保留为 archived")
	}

	// 4. 审核中 → 409
	if resp := deleteQuestion(t, "del-reviewing"); resp.Code != http.StatusConflict {
		t.Fatalf("审核中删除应 409，实际 %d", resp.Code)
	}

	// 5. 已驳回终态 → 409
	if resp := deleteQuestion(t, "del-rejected"); resp.Code != http.StatusConflict {
		t.Fatalf("驳回锁定题删除应 409，实际 %d", resp.Code)
	}

	// 6. 已提交全局分享的 published 题 → 409
	saveQuestion(t, "del-shared", domain.StatusPublished)
	if err := store.CreateQuestionShare(ctx, domain.QuestionShareRequest{
		ID: "del-share-1", QuestionID: "del-shared", OwnerID: adminID,
		Status: domain.QuestionSharePending, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if resp := deleteQuestion(t, "del-shared"); resp.Code != http.StatusConflict {
		t.Fatalf("已分享题目删除应 409，实际 %d body=%s", resp.Code, resp.Body.String())
	}
}
