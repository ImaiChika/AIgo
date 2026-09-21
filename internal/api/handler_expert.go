package api

import (
	"fmt"
	"net/http"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// handleListExperts 列出所有专家。
func (s *Server) handleListExperts(w http.ResponseWriter, r *http.Request) {
	experts, err := s.reviewSvc.ListExperts(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"experts": experts, "total": len(experts)})
}

// handleCreateExpert 创建专家。
// 请求：{"name": "张三", "department": "内科", "title": "主任医师", "specialties": ["肺炎"]}
func (s *Server) handleCreateExpert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID         string   `json:"id"`          // 可选，不填自动生成
		Name       string   `json:"name"`        // 姓名（必填）
		Department string   `json:"department"`  // 科室
		Title      string   `json:"title"`       // 职称
		Specialties []string `json:"specialties"` // 擅长知识点
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.Name == "" {
		writeError(w, 400, "专家姓名不能为空")
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("E%03d", time.Now().UnixNano()%10000)
	}

	expert := domain.Expert{
		ID:          req.ID,
		Name:        req.Name,
		Department:  req.Department,
		Title:       req.Title,
		Specialties: req.Specialties,
		Enabled:     true,
	}
	if err := s.reviewSvc.CreateExpert(r.Context(), expert); err != nil {
		writeError(w, 500, "创建失败: "+err.Error())
		return
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.LogExpert(r.Context(), expert.ID, auth.GetUsername(r.Context()), "create",
			fmt.Sprintf("新增专家「%s」（%s %s）", expert.Name, expert.Department, expert.Title))
	}
	writeJSON(w, 201, expert)
}

// handleUpdateExpert 更新专家信息（姓名、科室、职称、擅长、启用/停用）。
func (s *Server) handleUpdateExpert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.reviewSvc.GetExpert(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeError(w, 404, "专家不存在")
		return
	}

	var req struct {
		Name       string   `json:"name"`
		Department string   `json:"department"`
		Title      string   `json:"title"`
		Specialties []string `json:"specialties"`
		Enabled    *bool    `json:"enabled"` // 用指区分"未传"和"传false"
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	// 按字段更新
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Department != "" {
		existing.Department = req.Department
	}
	if req.Title != "" {
		existing.Title = req.Title
	}
	if req.Specialties != nil {
		existing.Specialties = req.Specialties
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if err := s.reviewSvc.UpdateExpert(r.Context(), *existing); err != nil {
		writeError(w, 500, "更新失败: "+err.Error())
		return
	}
	if s.auditSvc != nil {
		enabled := map[bool]string{true: "启用", false: "停用"}[existing.Enabled]
		_ = s.auditSvc.LogExpert(r.Context(), existing.ID, auth.GetUsername(r.Context()), "update",
			fmt.Sprintf("更新专家「%s」（%s %s，状态：%s）", existing.Name, existing.Department, existing.Title, enabled))
	}
	writeJSON(w, 200, existing)
}

// handleDeleteExpert 删除专家。
func (s *Server) handleDeleteExpert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reviewSvc.DeleteExpert(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.LogExpert(r.Context(), id, auth.GetUsername(r.Context()), "delete", "删除专家")
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}
