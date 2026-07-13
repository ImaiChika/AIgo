package api

import (
	"net/http"

	"aigo/internal/domain"
)

// handleImagePrompt 为题目生成结构化生图提示词。
// 调用千问分析题干内容，输出图片用途、类型、主题、必须出现/不能出现的要素等。
// 请求：{"question_id": "xxx"}
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

// handleImageGenerate 根据提示词生成候选图。
// 当前使用 Mock 实现（生成占位文件），后续接入真实生图模型。
// 请求：{"question_id": "xxx", "count": 4}
func (s *Server) handleImageGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		Count      int    `json:"count"` // 生成数量，默认 3-5 张
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Count < 3 {
		req.Count = 3 // 至少生成 3 张
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

// handleListImages 列出题目的所有候选图。
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
// 请求：{"image_id": "xxx", "expert_id": "E001", "action": "approved", "opinion": "配图准确"}
func (s *Server) handleImageReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImageID  string `json:"image_id"`  // 候选图 ID
		ExpertID string `json:"expert_id"` // 审核人
		Action   string `json:"action"`    // approved / rejected
		Opinion  string `json:"opinion"`   // 审核意见
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
