package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/exporter"
	"aigo/internal/importer"
	"aigo/internal/storage"
)

// questionExportRequest 描述一次题目导出的范围。
// 指定 QuestionIDs 时导出跨页勾选题目；ExportAll=true 时按当前页面筛选条件导出全部匹配结果。
type questionExportRequest struct {
	QuestionIDs []string `json:"question_ids"`
	ExportAll   bool     `json:"export_all"`
	Scope       string   `json:"scope"` // personal / global；空值兼容旧客户端
	Keyword     string   `json:"q"`
	Status      string   `json:"status"`
	BankID      string   `json:"bank_id"`
	Professions []string `json:"professions"`
}

// handleExportXlsx 导出题目为 Excel 文件。
// 导出是正式题库的出口：数据源仅限已通过（published）题目，
// 且调用者需要「查看正式题库」权限（question:view_formal）。
// 请求：{"question_ids": ["id1", "id2"]} 或 {"export_all": true}
// 返回文件下载。
func (s *Server) handleExportXlsx(w http.ResponseWriter, r *http.Request) {
	if !s.requireFormalBankAccess(w, r) {
		return
	}
	var req questionExportRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	questions, err := s.getQuestionsToExport(r, req)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if len(questions) == 0 {
		writeError(w, 400, "没有可导出的题目")
		return
	}

	// 生成文件
	outputDir := "output/exports"
	os.MkdirAll(outputDir, 0755)
	filename := newExportFilename("xlsx")
	path := filepath.Join(outputDir, filename)

	if err := importer.ExportToXlsx(questions, path); err != nil {
		writeError(w, 500, "导出失败: "+err.Error())
		return
	}
	s.logExport(r, "Excel", len(questions), req.ExportAll)

	writeJSON(w, 200, map[string]any{
		"path":     path,
		"filename": filename,
		"count":    len(questions),
	})
}

// handleExportDocx 导出题目为 Word 文件。数据源与权限要求同 handleExportXlsx。
func (s *Server) handleExportDocx(w http.ResponseWriter, r *http.Request) {
	if !s.requireFormalBankAccess(w, r) {
		return
	}
	var req questionExportRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	questions, err := s.getQuestionsToExport(r, req)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if len(questions) == 0 {
		writeError(w, 400, "没有可导出的题目")
		return
	}

	outputDir := "output/exports"
	os.MkdirAll(outputDir, 0755)
	filename := newExportFilename("docx")
	path := filepath.Join(outputDir, filename)

	if err := exporter.ExportToDocx(questions, path); err != nil {
		writeError(w, 500, "导出失败: "+err.Error())
		return
	}
	s.logExport(r, "Word", len(questions), req.ExportAll)

	writeJSON(w, 200, map[string]any{
		"path":     path,
		"filename": filename,
		"count":    len(questions),
	})
}

// handleDownloadExport 下载导出的文件。
func (s *Server) handleDownloadExport(w http.ResponseWriter, r *http.Request) {
	if !s.requireFormalBankAccess(w, r) {
		return
	}
	filename := r.PathValue("filename")
	if filename == "" {
		writeError(w, 400, "缺少文件名")
		return
	}

	// 安全检查：拒绝路径穿越
	if filename != filepath.Base(filename) {
		writeError(w, 400, "非法文件名")
		return
	}

	// 安全检查：只允许下载 output/exports 目录下的文件
	path := filepath.Join("output/exports", filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeError(w, 404, "文件不存在")
		return
	}

	// 设置下载头
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	http.ServeFile(w, r, path)
}

// getQuestionsToExport 获取要导出的题目列表（按下载权限的题库范围过滤）。
func (s *Server) getQuestionsToExport(r *http.Request, req questionExportRequest) ([]domain.A2Question, error) {
	if req.Scope == "global" && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		return nil, fmt.Errorf("无权导出全局题库")
	}
	if req.Scope == "personal" && !s.hasPermission(r, domain.PermQuestionView) {
		return nil, fmt.Errorf("无权导出个人题库")
	}
	if req.ExportAll {
		questions, err := s.questionStore.ListQuestions(r.Context())
		if err != nil {
			return nil, err
		}
		var allowed []domain.A2Question
		for _, q := range questions {
			if isExportableQuestion(q) && s.matchesQuestionExportScope(r, &q, req.Scope) && matchesQuestionExportFilter(q, req) && s.questionInScope(r, &q, domain.PermQuestionDownload) {
				allowed = append(allowed, q)
			}
		}
		return allowed, nil
	}

	if len(req.QuestionIDs) > 0 {
		var questions []domain.A2Question
		seen := make(map[string]bool, len(req.QuestionIDs))
		for _, id := range req.QuestionIDs {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			q, err := s.questionStore.GetQuestion(r.Context(), id)
			if err != nil {
				return nil, err
			}
			if q != nil && isExportableQuestion(*q) && s.matchesQuestionExportScope(r, q, req.Scope) && s.questionInScope(r, q, domain.PermQuestionDownload) {
				questions = append(questions, *q)
			}
		}
		return questions, nil
	}

	return nil, nil
}

func (s *Server) matchesQuestionExportScope(r *http.Request, q *domain.A2Question, scope string) bool {
	if q == nil || scope == "" {
		return true
	}
	if scope == "personal" {
		return q.OwnerID == auth.GetUserID(r.Context())
	}
	if scope == "global" {
		share := s.questionShare(r, q.ID)
		return share != nil && share.Status == domain.QuestionShareApproved
	}
	return false
}

// matchesQuestionExportFilter 与题库页面的搜索语义保持一致。
func matchesQuestionExportFilter(q domain.A2Question, req questionExportRequest) bool {
	if req.Status != "" && string(q.Status) != req.Status {
		return false
	}
	if req.BankID != "" {
		matched := false
		for _, bankID := range q.BankIDs {
			if bankID == req.BankID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(req.Professions) > 0 {
		matched := false
		for _, profession := range req.Professions {
			if q.Profession == strings.TrimSpace(profession) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if !storage.QuestionMatchesKeyword(q, req.Keyword) {
		return false
	}
	return true
}

// newExportFilename 使用纳秒级时间避免同一秒内并发导出互相覆盖。
func newExportFilename(extension string) string {
	now := time.Now()
	return fmt.Sprintf("题目_%s_%d.%s", now.Format("20060102_150405"), now.UnixNano(), extension)
}

func (s *Server) logExport(r *http.Request, format string, count int, allFiltered bool) {
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	scope := "勾选题目"
	if allFiltered {
		scope = "全部筛选结果"
	}
	_ = s.auditSvc.Log(r.Context(), "", "export", actor, fmt.Sprintf("导出%s：%s，共 %d 道题", format, scope, count))
}

// requireFormalBankAccess 导出入口统一校验：调用者必须能查看正式题库。
// 导出是正式题库唯一的文档出口，其余分层的题目一律不可导出。
func (s *Server) requireFormalBankAccess(w http.ResponseWriter, r *http.Request) bool {
	if !s.canViewTier(r, domain.TierFormal) {
		writeError(w, 403, "题目导出仅限正式题库，且需要「查看正式题库」权限")
		return false
	}
	return true
}

// isExportableQuestion 只允许正式题库（published 定稿）的题目离开系统。
func isExportableQuestion(q domain.A2Question) bool {
	return q.Status == domain.StatusPublished
}
