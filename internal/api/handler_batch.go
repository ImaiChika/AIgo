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
		status := http.StatusInternalServerError
		if errors.Is(err, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, "提交失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"job_id":  jobID,
		"count":   count,
		"message": capabilities.Message,
	})
}

// handleBatchStatus 查询批量任务状态。
func (s *Server) handleBatchStatus(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, 200, job)
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

	writeJSON(w, 200, map[string]any{
		"jobs":  jobs,
		"total": len(jobs),
	})
}

// handleBatchDownload 下载批量任务结果并导入题库。
func (s *Server) handleBatchDownload(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusForbidden, "无权导入其他用户的批量任务")
		return
	}

	// 执行器按统一 job ID 导入结果；具体 provider 文件 ID 留在适配器内部。
	owner, ownerErr := s.authSvc.GetUserByID(job.OwnerID)
	if ownerErr != nil || owner == nil || !owner.Enabled {
		writeError(w, http.StatusConflict, "批量任务原所有者不存在或已停用，不能导入")
		return
	}
	// 导入归属固定使用任务提交时的所有者，不能因管理员查看而改变。
	importCtx := storage.WithQuestionChange(r.Context(), storage.QuestionChange{
		Actor:      owner.Username,
		OwnerID:    owner.ID,
		ChangeType: "batch_generate",
	})
	result, err := s.batchSvc.ImportResults(importCtx, jobID, nil)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, batch.ErrUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, batch.ErrNotReady):
			status = http.StatusConflict
		}
		writeError(w, status, "导入失败: "+err.Error())
		return
	}

	// 导入的草稿自动提交 AI 质量检查（后台异步执行）
	if s.aiCheckSvc != nil && len(result.QuestionIDs) > 0 {
		s.aiCheckSvc.CheckAsync(result.QuestionIDs...)
	}

	writeJSON(w, 200, result)
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
