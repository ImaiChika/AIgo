package api

import (
	"net/http"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/review"
)

// handleSubmitReview 将题目提交到指定的审核流程。
// 请求：{"question_id": "xxx", "flow_id": "flow-a2-2round"}
// 提交后题目状态变为 reviewing，生成审核任务。
func (s *Server) handleSubmitReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"` // 题目 ID
		FlowID     string `json:"flow_id"`     // 审核流程 ID
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	task, err := s.reviewSvc.SubmitQuestion(r.Context(), req.QuestionID, req.FlowID)
	if err != nil {
		writeError(w, 500, "提交审核失败: "+err.Error())
		return
	}
	writeJSON(w, 200, task)
}

// handleReviewAction 执行审核操作（通过/驳回/需修改）。
// admin 角色可以审核任意轮次，其他角色只能审核分配给自己的轮次。
// expert_id 强制使用当前登录用户 ID（admin 除外，admin 可指定任意审核人）。
// 请求：{"task_id": "xxx", "action": "approved", "opinion": "通过"}
func (s *Server) handleReviewAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID   string `json:"task_id"`   // 审核任务 ID
		ExpertID string `json:"expert_id"` // 审核人 ID（admin 可指定，其他角色忽略此字段）
		Action   string `json:"action"`    // approved / rejected / revision_required
		Opinion  string `json:"opinion"`   // 审核意见
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	// 获取当前用户信息
	role := auth.GetRole(r.Context())
	currentUserID := auth.GetUserID(r.Context())

	// 非 admin 用户：强制使用当前登录用户的 ID，防止冒名
	expertID := currentUserID
	if role == "admin" && req.ExpertID != "" {
		expertID = req.ExpertID // admin 可指定任意审核人
	}

	err := s.reviewSvc.Review(r.Context(), review.ReviewRequest{
		TaskID:   req.TaskID,
		ExpertID: expertID,
		Action:   domain.QuestionStatus(req.Action),
		Opinion:  req.Opinion,
		Role:     role,
	})
	if err != nil {
		writeError(w, 500, "审核失败: "+err.Error())
		return
	}

	// 记录审核日志（从任务中获取题目ID）
	task, _ := s.reviewSvc.GetTask(r.Context(), req.TaskID)
	if task != nil {
		s.auditSvc.LogReview(r.Context(), task.QuestionID, expertID, req.Action, req.Opinion)
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleGetReviewTask 根据任务 ID 获取审核任务详情。
func (s *Server) handleGetReviewTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.reviewSvc.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeError(w, 404, "审核任务不存在")
		return
	}
	writeJSON(w, 200, task)
}

// handleGetTaskByQuestion 根据题目 ID 获取关联的审核任务。
// 用于前端选中题目后自动加载审核状态。
func (s *Server) handleGetTaskByQuestion(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	task, err := s.reviewSvc.GetTaskByQuestionID(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeJSON(w, 200, nil) // 没有审核任务返回 null
		return
	}
	writeJSON(w, 200, task)
}

// handleListFlows 列出所有审核流程配置。
func (s *Server) handleListFlows(w http.ResponseWriter, r *http.Request) {
	flows, err := s.reviewSvc.ListFlows(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"flows": flows, "total": len(flows)})
}

// handleCreateFlow 创建审核流程。
func (s *Server) handleCreateFlow(w http.ResponseWriter, r *http.Request) {
	var flow domain.ReviewFlowConfig
	if err := readJSON(r, &flow); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if err := s.reviewSvc.CreateFlow(r.Context(), flow); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, flow)
}

// handleDeleteFlow 删除审核流程。
func (s *Server) handleDeleteFlow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reviewSvc.DeleteFlow(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handleReviewRecords 获取某审核任务的所有审核记录。
func (s *Server) handleReviewRecords(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	records, err := s.reviewSvc.ListRecords(r.Context(), taskId)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"records": records, "total": len(records)})
}
