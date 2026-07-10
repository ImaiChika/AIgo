package api

import (
	"net/http"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/review"
)

// handleSubmitReview 提交题目到审核流程。
func (s *Server) handleSubmitReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		FlowID     string `json:"flow_id"`
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
func (s *Server) handleReviewAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID   string `json:"task_id"`
		ExpertID string `json:"expert_id"`
		Action   string `json:"action"`
		Opinion  string `json:"opinion"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	role := auth.GetRole(r.Context())

	err := s.reviewSvc.Review(r.Context(), review.ReviewRequest{
		TaskID:   req.TaskID,
		ExpertID: req.ExpertID,
		Action:   domain.QuestionStatus(req.Action),
		Opinion:  req.Opinion,
		Role:     role,
	})
	if err != nil {
		writeError(w, 500, "审核失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleGetReviewTask 获取审核任务详情。
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

// handleGetTaskByQuestion 根据题目 ID 获取审核任务。
func (s *Server) handleGetTaskByQuestion(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	task, err := s.reviewSvc.GetTaskByQuestionID(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeJSON(w, 200, nil)
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

// handleReviewRecords 获取审核记录。
func (s *Server) handleReviewRecords(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	records, err := s.reviewSvc.ListRecords(r.Context(), taskId)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"records": records, "total": len(records)})
}
