package api

import (
	"net/http"

	"aigo/internal/domain"
)

// handleImagePrompt 为题目生成结构化生图提示词。
func (s *Server) handleImagePrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	prompt, err := s.imgSvc.GeneratePrompt(r.Context(), req.QuestionID)
	if err != nil {
		writeError(w, 500, "生成提示词失败: "+err.Error())
		return
	}
	writeJSON(w, 200, prompt)
}

// handleImageGenerate 为题目生成候选图。
func (s *Server) handleImageGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		Count      int    `json:"count"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Count < 3 {
		req.Count = 3
	}

	images, err := s.imgSvc.GenerateImages(r.Context(), req.QuestionID, req.Count)
	if err != nil {
		writeError(w, 500, "生成图片失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"images": images,
		"count":  len(images),
	})
}

// handleListImages 列出题目的候选图。
func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	images, err := s.imgSvc.ListImages(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"images": images,
		"count":  len(images),
	})
}

// handleImageReview 审核候选图（通过/驳回）。
func (s *Server) handleImageReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImageID  string `json:"image_id"`
		ExpertID string `json:"expert_id"`
		Action   string `json:"action"`
		Opinion  string `json:"opinion"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	err := s.imgSvc.ReviewImage(r.Context(), req.ImageID, req.ExpertID, domain.ImageStatus(req.Action), req.Opinion)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
