package api

import (
	"errors"
	"net/http"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

func (s *Server) handleGetGenerationQuota(w http.ResponseWriter, r *http.Request) {
	if !generationQuotaAdmin(r) {
		writeError(w, http.StatusForbidden, "仅管理员可查看生成任务配额")
		return
	}
	if s.quotaStore == nil {
		writeError(w, http.StatusServiceUnavailable, "生成任务配额存储不可用")
		return
	}
	quota, usage, err := s.quotaStore.GetGenerationQuota(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取生成任务配额失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quota": quota, "usage": usage})
}

func (s *Server) handlePutGenerationQuota(w http.ResponseWriter, r *http.Request) {
	if !generationQuotaAdmin(r) {
		writeError(w, http.StatusForbidden, "仅管理员可修改生成任务配额")
		return
	}
	if s.quotaStore == nil {
		writeError(w, http.StatusServiceUnavailable, "生成任务配额存储不可用")
		return
	}
	var quota storage.GenerationQuota
	if err := readJSON(r, &quota); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	if err := quota.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.quotaStore.SaveGenerationQuota(r.Context(), quota); err != nil {
		writeError(w, http.StatusInternalServerError, "保存生成任务配额失败")
		return
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "update_generation_quota", auth.GetUsername(r.Context()), "调整生成任务准入配额")
	}
	s.handleGetGenerationQuota(w, r)
}

func generationQuotaAdmin(r *http.Request) bool {
	role := auth.GetRole(r.Context())
	return role == domain.RoleSuperAdmin || role == domain.RoleAdmin
}

func writeGenerationQuotaError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, storage.ErrGenerationQuotaExceeded) {
		return false
	}
	w.Header().Set("Retry-After", "10")
	writeError(w, http.StatusTooManyRequests, err.Error())
	return true
}
