package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aigo/internal/domain"
)

// handleBatchSubmit 提交批量任务到 DashScope 云端。
// 请求：{"limit": 100, "skip_existing": true, "count": 1, "from_code": "", "to_code": "", "job_name": "任务名"}
func (s *Server) handleBatchSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Limit        int    `json:"limit"`
		SkipExisting bool   `json:"skip_existing"`
		Count        int    `json:"count"`
		FromCode     string `json:"from_code"`
		ToCode       string `json:"to_code"`
		OutlineCodes string `json:"outline_codes"` // 逗号分隔的知识点代码
		JobName      string `json:"job_name"`      // 自定义任务名称
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	if req.Count <= 0 {
		req.Count = 1
	}

	// 获取知识点
	var points []domain.KnowledgePoint
	if req.OutlineCodes != "" {
		for _, code := range splitAndTrim(req.OutlineCodes) {
			if p, err := s.kpSvc.GetByID(r.Context(), code); err == nil && p != nil {
				points = append(points, *p)
			}
		}
	} else {
		all, err := s.kpSvc.ListAll(r.Context())
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		points = all
	}

	// 跳过已有题目
	if req.SkipExisting {
		questions, _ := s.questionStore.ListQuestions(r.Context())
		existing := make(map[string]bool)
		for _, q := range questions {
			if q.OutlineCode != "" {
				existing[q.OutlineCode] = true
			}
		}
		var filtered []domain.KnowledgePoint
		for _, p := range points {
			if !existing[p.OutlineCode] {
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

	// 提交任务
	jobID, count, err := s.batchSvc.GenerateAndSubmit(r.Context(), points, req.Count, jobName)
	if err != nil {
		writeError(w, 500, "提交失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"job_id":  jobID,
		"count":   count,
		"message": "任务已提交到 DashScope 云端，使用 batch-status 查询状态",
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
		writeError(w, 500, "查询失败: "+err.Error())
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

	// 查询任务状态
	job, err := s.batchSvc.GetJobStatus(r.Context(), jobID)
	if err != nil {
		writeError(w, 500, "查询失败: "+err.Error())
		return
	}

	// DashScope 文档写的成功状态是 "completed"，但实际可能返回 "complete"
	// 兼容处理两种写法
	fmt.Printf("[batch-download] jobID=%s, status=%q\n", jobID, job.Status)
	if job.Status != "completed" && job.Status != "complete" {
		writeError(w, 400, "任务尚未完成，当前状态: "+job.Status)
		return
	}

	if job.OutputFileID == "" {
		writeError(w, 400, "任务没有输出文件")
		return
	}

	// 获取知识点
	allKPs, err := s.kpSvc.ListAll(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// 下载并导入
	result, err := s.batchSvc.DownloadAndImport(r.Context(), job.OutputFileID, allKPs)
	if err != nil {
		writeError(w, 500, "导入失败: "+err.Error())
		return
	}

	writeJSON(w, 200, result)
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
