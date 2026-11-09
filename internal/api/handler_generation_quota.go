package api

import (
	"errors"
	"net/http"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
)

func (s *Server) handleGetGenerationQuota(w http.ResponseWriter, r *http.Request) {
	if !generationQuotaAdmin(r) {
		writeError(w, http.StatusForbidden, "仅超级管理员可查看生成任务配额")
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
	response := map[string]any{"quota": quota, "usage": usage, "runtime_available": s.concurrencyRuntime != nil, "learning_runtime_available": s.learningSvc != nil}
	if s.concurrencyRuntime != nil {
		for attempt := 0; attempt < 3; attempt++ {
			if _, err := s.concurrencyRuntime.Apply(quota); err != nil {
				writeError(w, 503, "并发配置同步失败")
				return
			}
			snapshot := s.concurrencyRuntime.Snapshot()
			if snapshot.Revision == quota.Revision {
				response["quota"] = quota
				response["usage"] = usage
				response["runtime"] = map[string]any{"revision": snapshot.Revision, "generation": snapshot.Generation, "batch": snapshot.Batch, "ai_check": snapshot.AICheck, "learning_analysis": storage.ConcurrencySnapshot{Limit: quota.LearningConcurrency, Active: usage.RunningLearningJobs, Waiting: usage.PendingLearningJobs}}
				writeJSON(w, 200, response)
				return
			}
			quota, usage, err = s.quotaStore.GetGenerationQuota(r.Context())
			if err != nil {
				writeError(w, 503, "读取最新配额失败")
				return
			}
		}
		writeError(w, 503, "配置正在同步，请刷新后重试")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handlePutGenerationQuota(w http.ResponseWriter, r *http.Request) {
	if !generationQuotaAdmin(r) {
		writeError(w, http.StatusForbidden, "仅超级管理员可修改生成任务配额")
		return
	}
	if s.quotaStore == nil {
		writeError(w, http.StatusServiceUnavailable, "生成任务配额存储不可用")
		return
	}
	var req struct {
		LearningConcurrency    *int              `json:"learning_concurrency"`
		ModelTools             *llm.NetworkTools `json:"model_tools"`
		SingleMaxQuestions     int               `json:"single_max_questions"`
		BatchMaxQuestions      int               `json:"batch_max_questions"`
		SingleActivePerUser    int               `json:"single_active_per_user"`
		BatchActivePerUser     int               `json:"batch_active_per_user"`
		GlobalPendingQuestions int               `json:"global_pending_questions"`
		GenerationConcurrency  *int              `json:"generation_concurrency"`
		BatchConcurrency       *int              `json:"batch_concurrency"`
		AICheckConcurrency     *int              `json:"ai_check_concurrency"`
		Revision               *int64            `json:"revision"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	quota := storage.GenerationQuota{SingleMaxQuestions: req.SingleMaxQuestions, BatchMaxQuestions: req.BatchMaxQuestions,
		SingleActivePerUser: req.SingleActivePerUser, BatchActivePerUser: req.BatchActivePerUser, GlobalPendingQuestions: req.GlobalPendingQuestions, ModelTools: req.ModelTools}
	if req.LearningConcurrency != nil {
		if *req.LearningConcurrency < 1 || *req.LearningConcurrency > 8 {
			writeError(w, 400, "AI总结并发应为1—8")
			return
		}
		quota.LearningConcurrency = *req.LearningConcurrency
	}
	validate := quota
	for _, field := range []struct {
		value      *int
		dst, probe *int
	}{{req.GenerationConcurrency, &quota.GenerationConcurrency, &validate.GenerationConcurrency}, {req.BatchConcurrency, &quota.BatchConcurrency, &validate.BatchConcurrency}, {req.AICheckConcurrency, &quota.AICheckConcurrency, &validate.AICheckConcurrency}} {
		if field.value != nil {
			*field.dst = *field.value
			*field.probe = *field.value
		} else {
			*field.probe = 2
		}
	}
	if (req.GenerationConcurrency != nil || req.BatchConcurrency != nil || req.AICheckConcurrency != nil || req.ModelTools != nil || req.LearningConcurrency != nil) && req.Revision == nil {
		writeError(w, 400, "修改执行设置需要提供当前配置版本revision，请先读取最新配置")
		return
	}
	if req.Revision != nil && *req.Revision < 1 {
		writeError(w, 400, "配置版本revision必须为正整数")
		return
	}
	if err := validate.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.concurrencyRuntime == nil && (req.GenerationConcurrency != nil || req.BatchConcurrency != nil || req.AICheckConcurrency != nil) {
		current, _, err := s.quotaStore.GetGenerationQuota(r.Context())
		if err != nil {
			writeError(w, 503, "读取并发配置失败")
			return
		}
		if (req.GenerationConcurrency != nil && *req.GenerationConcurrency != current.GenerationConcurrency) || (req.BatchConcurrency != nil && *req.BatchConcurrency != current.BatchConcurrency) || (req.AICheckConcurrency != nil && *req.AICheckConcurrency != current.AICheckConcurrency) {
			writeError(w, 503, "并发执行器未就绪，请恢复服务后重试")
			return
		}
	}
	if req.LearningConcurrency != nil && s.learningSvc == nil {
		current, _, err := s.quotaStore.GetGenerationQuota(r.Context())
		if err != nil {
			writeError(w, 503, "读取总结并发失败")
			return
		}
		if current.LearningConcurrency != *req.LearningConcurrency {
			writeError(w, 503, "AI总结执行器未就绪")
			return
		}
	}
	var saveErr error
	audited := false
	if auditStore, ok := s.quotaStore.(storage.GenerationQuotaAuditStore); ok {
		var revision int64
		if req.Revision != nil {
			revision = *req.Revision
		}
		saveErr = auditStore.SaveGenerationQuotaWithAudit(r.Context(), quota, revision, auth.GetUsername(r.Context()))
		audited = true
	} else if req.Revision != nil {
		versionStore, ok := s.quotaStore.(storage.GenerationQuotaVersionStore)
		if !ok {
			writeError(w, 503, "配置版本存储不可用")
			return
		}
		saveErr = versionStore.SaveGenerationQuotaVersion(r.Context(), quota, *req.Revision)
	} else {
		saveErr = s.quotaStore.SaveGenerationQuota(r.Context(), quota)
	}
	if errors.Is(saveErr, storage.ErrGenerationQuotaConflict) {
		writeError(w, 409, saveErr.Error())
		return
	}
	if saveErr != nil {
		writeError(w, http.StatusInternalServerError, "保存生成任务配额失败")
		return
	}
	if !audited && s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "update_generation_quota", auth.GetUsername(r.Context()), "调整生成任务配额、执行并发及模型联网工具")
	}
	s.handleGetGenerationQuota(w, r)
}

func generationQuotaAdmin(r *http.Request) bool {
	role := auth.GetRole(r.Context())
	return role == domain.RoleSuperAdmin
}

func writeGenerationQuotaError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, storage.ErrGenerationQuotaExceeded) {
		return false
	}
	w.Header().Set("Retry-After", "10")
	writeError(w, http.StatusTooManyRequests, err.Error())
	return true
}
