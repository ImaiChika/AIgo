// 所有者自助删除（移入淘汰题库）验收测试：
// 无 question:delete 权限的出题人可以删除（归档）本人的"刚出未送审"（ai_reviewed）
// 与"流程完全结束"（published，含分享被否）的题；流程中的题（审核中/待决断/
// 需修改）与淘汰终态不可删；不能删他人的题；分享审批中/已入全局库不可删。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestOwnerSelfDeletePolicy(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	// 出题人：teacher 角色（无 question:delete / delete_formal）
	created := serveAuthJSON(t, handler, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "owner-teacher", "password": "owner-pass-1", "display_name": "出题人", "role": "teacher",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create owner user status=%d body=%s", created.Code, created.Body.String())
	}
	var createdUser struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdUser); err != nil || createdUser.ID == "" {
		t.Fatalf("decode created user: %v body=%s", err, created.Body.String())
	}
	ownerToken := loginForAuthTest(t, handler, "owner-teacher", "owner-pass-1", "198.51.100.2")
	ctx := context.Background()
	now := time.Now()

	saveQuestion := func(t *testing.T, id string, status domain.QuestionStatus) {
		t.Helper()
		q := domain.A2Question{
			ID: id, ClinicalStem: "所有者删除测试 " + id,
			Options: []domain.Option{{Label: "A", Text: "选项"}}, Answer: "A",
			Status: status, Version: 1, OwnerID: createdUser.ID, CreatedBy: "owner-teacher",
			CreatedAt: now, UpdatedAt: now,
		}
		if err := store.SaveQuestion(ctx, q); err != nil {
			t.Fatalf("保存 %s: %v", id, err)
		}
	}

	ownerDelete := func(t *testing.T, token, id string) int {
		t.Helper()
		return serveAuthJSON(t, handler, http.MethodDelete, "/api/questions/"+id, token, "198.51.100.2", nil).Code
	}

	// 1. 本人 ai_reviewed（刚出未送审）→ 归档成功
	saveQuestion(t, "own-ai-reviewed", domain.StatusAIReviewed)
	if code := ownerDelete(t, ownerToken, "own-ai-reviewed"); code != http.StatusOK {
		t.Fatalf("所有者删除 ai_reviewed 应 200，实际 %d", code)
	}
	if q, _ := store.GetQuestion(ctx, "own-ai-reviewed"); q == nil || q.Status != domain.StatusArchived {
		t.Fatal("ai_reviewed 删除后应为 archived（移入淘汰题库）")
	}

	// 2. 本人 published（流程完全结束）→ 归档成功
	saveQuestion(t, "own-published", domain.StatusPublished)
	if code := ownerDelete(t, ownerToken, "own-published"); code != http.StatusOK {
		t.Fatalf("所有者删除 published 应 200，实际 %d", code)
	}
	if q, _ := store.GetQuestion(ctx, "own-published"); q == nil || q.Status != domain.StatusArchived {
		t.Fatal("published 删除后应为 archived")
	}

	// 3. 本人 published + 分享被否 → 不阻塞删除
	saveQuestion(t, "own-share-rejected", domain.StatusPublished)
	if err := store.CreateQuestionShare(ctx, domain.QuestionShareRequest{
		ID: "own-share-rej", QuestionID: "own-share-rejected", OwnerID: createdUser.ID,
		Status: domain.QuestionShareRejected, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if code := ownerDelete(t, ownerToken, "own-share-rejected"); code != http.StatusOK {
		t.Fatalf("分享被否的 published 删除应 200，实际 %d", code)
	}

	// 4. 本人 published + 分享审批中 → 409
	saveQuestion(t, "own-share-pending", domain.StatusPublished)
	if err := store.CreateQuestionShare(ctx, domain.QuestionShareRequest{
		ID: "own-share-pend", QuestionID: "own-share-pending", OwnerID: createdUser.ID,
		Status: domain.QuestionSharePending, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if code := ownerDelete(t, ownerToken, "own-share-pending"); code != http.StatusConflict {
		t.Fatalf("分享审批中的题目删除应 409，实际 %d", code)
	}

	// 5. 流程中的题（reviewing/conflict/revision_required）→ 409
	for _, status := range []domain.QuestionStatus{domain.StatusReviewing, domain.StatusConflict, domain.StatusRevisionRequired} {
		id := "own-flow-" + string(status)
		saveQuestion(t, id, status)
		if code := ownerDelete(t, ownerToken, id); code != http.StatusConflict {
			t.Fatalf("流程中(%s)删除应 409，实际 %d", status, code)
		}
	}

	// 5b. 退回修改：题目本体状态已恢复 ai_reviewed，但存在审核历史 → 409。
	// 用户口径：退回需修改虽显示"AI已检查"，也算流程中，不可删除。
	saveQuestion(t, "own-returned", domain.StatusAIReviewed)
	if err := store.SaveFlowConfig(ctx, domain.ReviewFlowConfig{
		ID: "own-del-flow", Name: "所有者删除测试流程", CreatedAt: now,
		Rounds: []domain.RoundConfig{{RoundNumber: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(ctx, domain.ReviewTask{
		ID: "own-del-task", QuestionID: "own-returned", FlowID: "own-del-flow",
		Status: domain.StatusRevisionRequired, CurrentRound: 1,
		AssignedTo: []string{"reviewer-1"}, Attempt: 1, QuestionVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if code := ownerDelete(t, ownerToken, "own-returned"); code != http.StatusConflict {
		t.Fatalf("退回修改（ai_reviewed+审核历史）删除应 409，实际 %d", code)
	}
	// 同一道题对有权限者开放（管理员清理语义）
	if code := ownerDelete(t, adminToken, "own-returned"); code != http.StatusOK {
		t.Fatalf("有权限者删除退回修改题应 200，实际 %d", code)
	}

	// 6. 淘汰终态 → 409
	saveQuestion(t, "own-archived", domain.StatusArchived)
	if code := ownerDelete(t, ownerToken, "own-archived"); code != http.StatusConflict {
		t.Fatalf("已归档题删除应 409，实际 %d", code)
	}

	// 7. 他人的题 → 403（他人名下的题对所有者不可删）
	q := domain.A2Question{
		ID: "other-owned", ClinicalStem: "所有者删除测试 other-owned",
		Options: []domain.Option{{Label: "A", Text: "选项"}}, Answer: "A",
		Status: domain.StatusAIReviewed, Version: 1, OwnerID: "other-user-1", CreatedBy: "other-user",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if code := ownerDelete(t, ownerToken, "other-owned"); code != http.StatusForbidden {
		t.Fatalf("删除他人题目应 403，实际 %d", code)
	}
	// 反向：有权限的 admin 可以删除他人的题（全局题库清理语义）
	if code := ownerDelete(t, adminToken, "other-owned"); code != http.StatusOK {
		t.Fatalf("有权限者删除他人题目应 200，实际 %d", code)
	}
	if q, _ := store.GetQuestion(ctx, "other-owned"); q == nil || q.Status != domain.StatusArchived {
		t.Fatal("有权限者删除后应为 archived")
	}

	// 8. 个人视野严格仅本人：view_all 管理员的 scope=personal 列表不再出现
	// 他人题目（2026-09-19 产品口径）。
	saveQuestion(t, "own-visible", domain.StatusAIReviewed) // owner-teacher 名下
	otherQ := domain.A2Question{
		ID: "other-visible", ClinicalStem: "所有者删除测试 other-visible",
		Options: []domain.Option{{Label: "A", Text: "选项"}}, Answer: "A",
		Status: domain.StatusAIReviewed, Version: 1, OwnerID: "other-user-2", CreatedBy: "other-user",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(ctx, otherQ); err != nil {
		t.Fatal(err)
	}
	teacherList := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=personal&tier=working", ownerToken, "198.51.100.2", nil)
	if teacherList.Code != http.StatusOK {
		t.Fatalf("teacher personal list status=%d", teacherList.Code)
	}
	if !containsID(teacherList.Body.String(), "own-visible") || containsID(teacherList.Body.String(), "other-visible") {
		t.Fatalf("teacher 个人视野应只见本人题目: %s", teacherList.Body.String())
	}
	adminList := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=personal&tier=working", adminToken, "198.51.100.1", nil)
	if adminList.Code != http.StatusOK {
		t.Fatalf("admin personal list status=%d", adminList.Code)
	}
	if containsID(adminList.Body.String(), "own-visible") || containsID(adminList.Body.String(), "other-visible") {
		t.Fatalf("view_all 管理员个人视野不应出现他人题目: %s", adminList.Body.String())
	}
}

// containsID 粗判题目 ID 是否出现在分页响应中（ID 含唯一前缀，无误报风险）。
func containsID(body, id string) bool {
	return strings.Contains(body, `"`+id+`"`)
}
