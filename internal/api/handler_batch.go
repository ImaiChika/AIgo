package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/knowledge"
	"aigo/internal/storage"
)

// withBatchOwnerName 回填任务归属人用户名，供任务卡片展示"由谁生成"。
// 查不到（如账号已注销）时保持为空，前端回退为"其他用户"。
func (s *Server) withBatchOwnerName(job *batch.BatchJob) *batch.BatchJob {
	if job == nil || job.OwnerID == "" {
		return job
	}
	if owner, err := s.authSvc.GetUserByID(job.OwnerID); err == nil && owner != nil {
		job.OwnerName = owner.Username
	}
	return job
}

// handleBatchCapabilities 返回脱敏后的批量执行器状态。
func (s *Server) handleBatchCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.batchSvc.Capabilities())
}

// handleBatchSubmit 向当前配置的批量执行器提交任务。
// 请求：{"limit": 100, "skip_existing": true, "count": 1, "from_code": "", "to_code": "", "job_name": "任务名"}
func (s *Server) handleBatchSubmit(w http.ResponseWriter, r *http.Request) {
	if s.aiCheckSvc == nil || !s.aiCheckSvc.AutomaticReady() {
		writeError(w, http.StatusServiceUnavailable, "AI 质量检查服务未就绪，已停止批量出题；请联系管理员恢复后再试")
		return
	}
	var req struct {
		VersionID         string   `json:"version_id"`
		KnowledgePointIDs []string `json:"knowledge_point_ids"`
		Limit             int      `json:"limit"`
		SkipExisting      bool     `json:"skip_existing"`
		Count             int      `json:"count"`
		FromCode          string   `json:"from_code"`
		ToCode            string   `json:"to_code"`
		OutlineCodes      string   `json:"outline_codes"` // 逗号分隔的知识点代码
		JobName           string   `json:"job_name"`      // 自定义任务名称
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 20 {
		writeError(w, http.StatusBadRequest, "每个知识点最多生成 20 道题")
		return
	}

	capabilities := s.batchSvc.Capabilities()
	if !capabilities.Available {
		writeError(w, http.StatusServiceUnavailable, capabilities.Message)
		return
	}

	// Pin this batch to one syllabus and fail on missing/deleted selections.
	points := []domain.KnowledgePoint{}
	if len(req.KnowledgePointIDs) > 0 || req.OutlineCodes != "" {
		ids := req.KnowledgePointIDs
		codes := false
		if len(ids) == 0 {
			ids = splitAndTrim(req.OutlineCodes)
			codes = true
		}
		seen := map[string]bool{}
		for _, value := range ids {
			id, code := value, ""
			if codes {
				id = ""
				code = value
			}
			p, err := s.kpSvc.ResolveForGeneration(r.Context(), req.VersionID, id, code)
			if err != nil {
				kpError(w, err)
				return
			}
			req.VersionID = p.VersionID
			if !seen[p.ID] {
				seen[p.ID] = true
				points = append(points, *p)
			}
		}
	} else {
		v, err := s.kpSvc.ResolveVersion(r.Context(), req.VersionID)
		if err != nil {
			kpError(w, err)
			return
		}
		if v.Status != "published" {
			writeError(w, 400, "请先启用大纲版本")
			return
		}
		all, err := s.kpSvc.SearchFiltered(r.Context(), knowledge.KPSearchOptions{VersionID: v.ID})
		if err != nil {
			kpError(w, err)
			return
		}
		points = all
	}

	// 跳过已有题目
	if req.SkipExisting {
		questions, err := s.questionStore.ListQuestions(r.Context())
		if err != nil {
			writeError(w, 500, "读取已有题目失败")
			return
		}
		ownerID := auth.GetUserID(r.Context())
		personalQuestions := make([]domain.A2Question, 0, len(questions))
		for _, question := range questions {
			if question.OwnerID == ownerID {
				personalQuestions = append(personalQuestions, question)
			}
		}
		existing := domain.ExistingKnowledgeKeys(personalQuestions)
		var filtered []domain.KnowledgePoint
		for _, p := range points {
			if !existing[domain.KnowledgePointKey(p)] {
				filtered = append(filtered, p)
			}
		}
		points = filtered
	}

	// 按范围筛选
	if req.FromCode != "" || req.ToCode != "" {
		var filtered []domain.KnowledgePoint
		for _, kp := range points {
			if req.FromCode != "" && kp.OutlineCode < req.FromCode {
				continue
			}
			if req.ToCode != "" && kp.OutlineCode > req.ToCode {
				continue
			}
			filtered = append(filtered, kp)
		}
		points = filtered
	}

	// 限制数量
	if req.Limit > 0 && req.Limit < len(points) {
		points = points[:req.Limit]
	}

	if len(points) == 0 {
		writeError(w, 400, "没有符合条件的知识点")
		return
	}
	if s.quotaStore != nil {
		quota, _, err := s.quotaStore.GetGenerationQuota(r.Context())
		if err != nil {
			writeError(w, 503, "读取生成任务配额失败")
			return
		}
		if len(points)*req.Count > quota.BatchMaxQuestions {
			writeGenerationQuotaError(w, &storage.GenerationQuotaError{Message: fmt.Sprintf("批量任务最多生成 %d 道题，请减少要点或每点题数", quota.BatchMaxQuestions)})
			return
		}
	} else if len(points)*req.Count > storage.DefaultGenerationQuota().BatchMaxQuestions {
		writeGenerationQuotaError(w, &storage.GenerationQuotaError{Message: "批量任务最多生成 100 道题，请减少要点或每点题数"})
		return
	}

	// 生成任务名称
	jobName := req.JobName
	if jobName == "" {
		jobName = fmt.Sprintf("批量生成 %d题 %s", len(points), time.Now().Format("01-02 15:04"))
	}

	// 提交任务：把当前用户归属写入批量任务，后续状态查询/结果导入必须按任务拥有者隔离。
	batchCtx := storage.WithQuestionChange(r.Context(), storage.QuestionChange{
		Actor:      auth.GetUsername(r.Context()),
		OwnerID:    auth.GetUserID(r.Context()),
		ChangeType: "batch_generate",
		ChangeNote: "单题 API 批量生成任务",
	})
	jobID, count, err := s.batchSvc.GenerateAndSubmit(batchCtx, points, req.Count, jobName)
	if err != nil {
		if writeGenerationQuotaError(w, err) {
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, "提交失败: "+err.Error())
		return
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "generate_batch", auth.GetUsername(r.Context()),
			fmt.Sprintf("提交批量生成任务「%s」：%d 个知识点 × 每点 %d 题，共 %d 题（任务 %s）", jobName, len(points), req.Count, count, jobID))
	}

	writeJSON(w, 200, map[string]any{
		"job_id":  jobID,
		"count":   count,
		"message": capabilities.Message,
	})
}

// handleBatchStatus 查询批量任务状态。
func (s *Server) handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermBatchRun) && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看批量任务")
		return
	}
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, 400, "缺少 job_id")
		return
	}

	job, err := s.batchSvc.GetJobStatus(r.Context(), jobID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, "查询失败: "+err.Error())
		return
	}
	if !s.canAccessBatchJob(r, job) {
		writeError(w, http.StatusForbidden, "无权查看其他用户的批量任务")
		return
	}

	writeJSON(w, 200, s.withBatchOwnerName(job))
}

// handleBatchHistory 为历史页提供归属过滤和服务端分页。
func (s *Server) handleBatchHistory(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermBatchRun) && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看批量任务历史")
		return
	}
	global := r.URL.Query().Get("scope") == "global"
	if global && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看其他用户的批量任务")
		return
	}
	ownerID := auth.GetUserID(r.Context())
	if global {
		ownerID = ""
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 || page > 1000000 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 50 {
		pageSize = 12
	}
	pager, ok := s.batchSvc.(batch.HistoryPager)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "批量任务历史暂不可用")
		return
	}
	jobs, total, err := pager.ListJobsPage(r.Context(), ownerID, r.URL.Query().Get("name"), r.URL.Query().Get("status"), pageSize, (page-1)*pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询批量任务历史失败: "+err.Error())
		return
	}
	for i := range jobs {
		s.withBatchOwnerName(&jobs[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "total": total, "page": page, "page_size": pageSize})
}

// handleBatchList 查询批量任务列表（支持按名称搜索）。
// 查询参数：name=任务名称关键词，status=状态筛选，limit=数量限制
func (s *Server) handleBatchList(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	status := r.URL.Query().Get("status")
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	jobs, err := s.batchSvc.ListJobs(r.Context(), name, status, limit)
	if err != nil {
		writeError(w, 500, "查询失败: "+err.Error())
		return
	}
	if !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		viewerID := auth.GetUserID(r.Context())
		filtered := jobs[:0]
		for _, job := range jobs {
			if job.OwnerID == viewerID {
				filtered = append(filtered, job)
			}
		}
		jobs = filtered
	}
	for i := range jobs {
		s.withBatchOwnerName(&jobs[i])
	}

	writeJSON(w, 200, map[string]any{
		"jobs":  jobs,
		"total": len(jobs),
	})
}

// handleBatchDownload 下载批量任务结果并导入题库。
func (s *Server) handleBatchDownload(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermBatchRun) && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看批量任务结果")
		return
	}
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, 400, "缺少 job_id")
		return
	}
	job, statusErr := s.batchSvc.GetJobStatus(r.Context(), jobID)
	if statusErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(statusErr, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, "查询批量任务失败: "+statusErr.Error())
		return
	}
	// 执行器按统一 job ID 导入结果；具体 provider 文件 ID 留在适配器内部。
	// 访问规则：任务归属人全权导入/重放；全局查看权限的管理员仅可回放
	// 已导入任务（ImportResults 对已导入任务只重放存量结果、不再入库），
	// 解决角色拆分后存量任务的原主失去批量权限时无人能看结果的问题。
	if job == nil || job.OwnerID == "" || job.OwnerID != auth.GetUserID(r.Context()) {
		if !(job != nil && s.hasPermission(r, domain.PermQuestionViewGlobal) && job.ImportedAt != "") {
			writeError(w, http.StatusForbidden, "无权导入其他用户的批量任务")
			return
		}
	}
	if job.ImportedAt == "" && !s.hasPermission(r, domain.PermBatchRun) {
		writeError(w, http.StatusForbidden, "当前身份无权导入批量任务结果")
		return
	}
	owner, ownerErr := s.authSvc.GetUserByID(job.OwnerID)
	if job.ImportedAt == "" && (ownerErr != nil || owner == nil || !owner.Enabled) {
		writeError(w, http.StatusConflict, "批量任务原所有者不存在或已停用，不能导入")
		return
	}
	ownerName := "历史账号"
	if owner != nil {
		ownerName = owner.Username
	}
	// 导入归属固定使用任务提交时的所有者，不能因管理员查看而改变。
	importCtx := storage.WithQuestionChange(r.Context(), storage.QuestionChange{
		Actor:      ownerName,
		OwnerID:    job.OwnerID,
		ChangeType: "batch_generate",
	})
	result, err := s.batchSvc.ImportResults(importCtx, jobID, nil)
	if err != nil {
		if writeGenerationQuotaError(w, err) {
			return
		}
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, batch.ErrUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, batch.ErrNotReady):
			status = http.StatusConflict
		case errors.Is(err, storage.ErrBatchJobAlreadyActive):
			status = http.StatusConflict
		}
		writeError(w, status, "导入失败: "+err.Error())
		return
	}

	// 导入的草稿自动提交 AI 质量检查（后台异步执行）
	if s.aiCheckSvc != nil && job.ImportedAt == "" && len(result.QuestionIDs) > 0 {
		s.aiCheckSvc.CheckAsync(result.QuestionIDs...)
	}
	// 已导入任务的 ImportResults 只重放历史结果，切换任务查看不属于导入操作。
	if s.auditSvc != nil && job.ImportedAt == "" {
		_ = s.auditSvc.Log(r.Context(), "", "batch_import", auth.GetUsername(r.Context()),
			fmt.Sprintf("导入批量任务结果「%s」：%d 道题（任务 %s，归属 %s）", job.JobName, len(result.QuestionIDs), jobID, ownerName))
	}

	writeJSON(w, 200, result)
}

// handleBatchRetryFailed 重跑任务中的失败生成单元。
// 仅任务所有者可在结果导入前调用；任务转回 in_progress 后按既有轮询与
// 自动导入流程继续。受全局并发上限约束，与正常执行共享同一队列。
func (s *Server) handleBatchRetryFailed(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, 400, "缺少 job_id")
		return
	}
	job, statusErr := s.batchSvc.GetJobStatus(r.Context(), jobID)
	if statusErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(statusErr, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, "查询批量任务失败: "+statusErr.Error())
		return
	}
	if job == nil || job.OwnerID == "" || job.OwnerID != auth.GetUserID(r.Context()) {
		writeError(w, http.StatusForbidden, "无权重跑其他用户的批量任务")
		return
	}
	retried, err := s.batchSvc.RetryFailed(r.Context(), jobID)
	if err != nil {
		if writeGenerationQuotaError(w, err) {
			return
		}
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, batch.ErrUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, batch.ErrNotReady):
			status = http.StatusConflict
		}
		writeError(w, status, "重跑失败: "+err.Error())
		return
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "batch_retry", auth.GetUsername(r.Context()),
			fmt.Sprintf("重跑批量任务「%s」的失败生成单元（任务 %s，失败 %d 题）", job.JobName, jobID, job.Failed))
	}
	writeJSON(w, 200, s.withBatchOwnerName(retried))
}

func (s *Server) canAccessBatchJob(r *http.Request, job *batch.BatchJob) bool {
	if job == nil {
		return false
	}
	if s.hasPermission(r, domain.PermQuestionViewGlobal) {
		return true
	}
	return job.OwnerID != "" && job.OwnerID == auth.GetUserID(r.Context())
}

// splitAndTrim 按逗号分割并去除空白。
func splitAndTrim(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
