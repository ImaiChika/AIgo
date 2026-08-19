package api

import (
	"net/http"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// handleImagePrompt 为题目生成结构化生图提示词。
// 调用千问分析题干内容，输出图片用途、类型、主题、必须出现/不能出现的要素等。
// 请求：{"question_id": "xxx"}。校验题库范围（question:view）。
func (s *Server) handleImagePrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	if _, status, err := s.loadScopedQuestion(r, req.QuestionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
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
// 请求：{"question_id": "xxx", "count": 4}。校验题库范围（question:view）。
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

	if _, status, err := s.loadScopedQuestion(r, req.QuestionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
		return
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

// handleListImages 列出题目的所有候选图。校验题库范围（question:view）。
func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	if _, status, err := s.loadScopedQuestion(r, questionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
		return
	}
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
// 请求：{"image_id": "xxx", "action": "approved", "opinion": "配图准确"}
// 审核人固定取当前登录用户（JWT），不接受请求体伪造 expert_id；
// 同时校验该图对应题目在审核人的题库范围内（question:view）。
func (s *Server) handleImageReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImageID string `json:"image_id"` // 候选图 ID
		Action  string `json:"action"`   // approved / rejected
		Opinion string `json:"opinion"`  // 审核意见
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	// 通过图片 ID 找到对应题目并校验题库范围
	img, err := s.imgSvc.GetImageByID(r.Context(), req.ImageID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if img == nil {
		writeError(w, 404, "图片不存在")
		return
	}
	if _, status, err := s.loadScopedQuestion(r, img.QuestionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
		return
	}

	// 审核人固定为当前登录用户，不接受请求体伪造
	expertID := auth.GetUserID(r.Context())

	err = s.imgSvc.ReviewImage(r.Context(), req.ImageID, expertID, domain.ImageStatus(req.Action), req.Opinion)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
