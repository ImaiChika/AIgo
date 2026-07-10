package api

import (
	"fmt"
	"net/http"
	"time"

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
func (s *Server) handleCreateExpert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Department string   `json:"department"`
		Title      string   `json:"title"`
		Specialties []string `json:"specialties"`
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
	writeJSON(w, 201, expert)
}

// handleUpdateExpert 更新专家信息。
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
		Enabled    *bool    `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
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
	writeJSON(w, 200, existing)
}

// handleDeleteExpert 删除专家。
func (s *Server) handleDeleteExpert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reviewSvc.DeleteExpert(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}
