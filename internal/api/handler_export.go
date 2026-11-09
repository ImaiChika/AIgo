package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	TaskKind    string   `json:"task_kind"`
	TaskID      string   `json:"task_id"`
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
	var req questionExportRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if !s.requireExportAccess(w, r, req) {
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
	outputDir := exportDirectory(auth.GetUserID(r.Context()))
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		writeError(w, 500, "创建导出目录失败")
		return
	}
	filename := newExportFilename("xlsx")
	path := filepath.Join(outputDir, filename)

	if err := importer.ExportToXlsx(questions, path); err != nil {
		writeError(w, 500, "导出失败: "+err.Error())
		return
	}
	if err := s.saveExportManifest(r, path, req.Scope, questions); err != nil {
		_ = os.Remove(path)
		writeError(w, 500, "保存导出范围记录失败，请重新导出")
		return
	}
	s.logExport(r, "Excel", questions, req, filename)

	writeJSON(w, 200, map[string]any{
		"path":     path,
		"filename": filename,
		"count":    len(questions),
	})
}

// handleExportDocx 导出题目为 Word 文件。数据源与权限要求同 handleExportXlsx。
func (s *Server) handleExportDocx(w http.ResponseWriter, r *http.Request) {
	var req questionExportRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if !s.requireExportAccess(w, r, req) {
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

	outputDir := exportDirectory(auth.GetUserID(r.Context()))
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		writeError(w, 500, "创建导出目录失败")
		return
	}
	filename := newExportFilename("docx")
	path := filepath.Join(outputDir, filename)

	if err := exporter.ExportToDocx(questions, path); err != nil {
		writeError(w, 500, "导出失败: "+err.Error())
		return
	}
	if err := s.saveExportManifest(r, path, req.Scope, questions); err != nil {
		_ = os.Remove(path)
		writeError(w, 500, "保存导出范围记录失败，请重新导出")
		return
	}
	s.logExport(r, "Word", questions, req, filename)

	writeJSON(w, 200, map[string]any{
		"path":     path,
		"filename": filename,
		"count":    len(questions),
	})
}

// handleDownloadExport 下载导出的文件。
func (s *Server) handleDownloadExport(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if filename == "" {
		writeError(w, 400, "缺少文件名")
		return
	}

	// 安全检查：拒绝路径穿越
	if filename != filepath.Base(filename) || (filepath.Ext(filename) != ".xlsx" && filepath.Ext(filename) != ".docx") {
		writeError(w, 400, "非法文件名")
		return
	}

	// 下载与创建均限定当前账号，文件名本身不授予读取他人导出的权限。
	path := filepath.Join(exportDirectory(auth.GetUserID(r.Context())), filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeError(w, 404, "文件不存在")
		return
	}

	if !s.validateExportManifest(w, r, path) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	// 设置下载头
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	http.ServeFile(w, r, path)
}

// 使用稳定账号 ID 的哈希作目录名，身份切换不改变文件归属，历史 ID 中的
// 特殊字符也无法构成路径穿越。旧共享目录不作为下载回退入口。
func exportDirectory(userID string) string {
	digest := sha256.Sum256([]byte(userID))
	return filepath.Join("output", "exports", hex.EncodeToString(digest[:]))
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
		taskIDs, err := s.exportAuthoringTask(r, req)
		if err != nil {
			return nil, err
		}
		filter := storage.QuestionFilter{IDs: taskIDs, Keyword: req.Keyword, Status: req.Status, Professions: req.Professions, BankID: req.BankID, ExcludeDeleted: true, NoActiveReviewTask: true}
		if req.BankID == "__unclassified__" {
			filter.BankID = ""
			filter.Unclassified = true
		}
		if req.Scope == "personal" {
			filter.OwnerID = auth.GetUserID(r.Context())
			filter.LibraryStates = []string{string(domain.LibraryActive)}
		} else {
			if req.Status != "" && req.Status != string(domain.StatusPublished) {
				return nil, nil
			}
			filter.Status = string(domain.StatusPublished)
			if req.Scope == "global" {
				filter.GlobalOnly = true
				filter.NoActiveReviewTask = false
				filter.GlobalStatuses = []string{string(domain.QuestionShareApproved)}
				filter.BankScope, filter.ScopeRestricted = s.questionBankScope(r, domain.PermQuestionViewFormal)
			}
		}
		var allowed []domain.A2Question
		seen := map[string]bool{}
		for page := 1; ; page++ {
			questions, total, err := s.questionStore.SearchQuestions(r.Context(), filter, page, 200)
			if err != nil {
				return nil, err
			}
			for _, q := range questions {
				if !seen[q.ID] && s.currentlyExportable(r, &q, req.Scope) && matchesQuestionExportFilter(q, req) {
					allowed = append(allowed, q)
				}
				seen[q.ID] = true
			}
			if len(questions) == 0 || page*200 >= total {
				break
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
			var q *domain.A2Question
			var err error
			if req.Scope == "global" {
				q, _, _, err = s.loadSharedQuestion(r, id)
			} else {
				q, err = s.questionStore.GetQuestion(r.Context(), id)
			}
			if err != nil {
				return nil, err
			}
			if s.currentlyExportable(r, q, req.Scope) {
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
	if req.BankID == "__unclassified__" && len(q.BankIDs) > 0 {
		return false
	}
	if req.BankID != "" && req.BankID != "__unclassified__" {
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

func (s *Server) logExport(r *http.Request, format string, questions []domain.A2Question, req questionExportRequest, filename string) {
	if s.auditSvc == nil {
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	items := make([]map[string]any, 0, len(questions))
	for _, q := range questions {
		items = append(items, map[string]any{"id": q.ID, "version": q.Version})
	}
	detail, _ := json.Marshal(map[string]any{"format": format, "scope": req.Scope, "filename": filename, "count": len(questions), "all_filtered": req.ExportAll, "questions": items})
	_ = s.auditSvc.Log(r.Context(), "", "export", actor, string(detail))
}

// requireFormalBankAccess 导出入口统一校验：调用者必须能查看正式题库。
// 导出是正式题库唯一的文档出口，其余分层的题目一律不可导出。
// requireExportAccess 导出访问入口（C3）：scope=personal 按 question:download
// + question:view 走个人普通题库判据；其余（全局/默认）保持正式题库权限与
// published 口径，旧客户端省略 scope 时仍要求正式库权限。
func (s *Server) requireExportAccess(w http.ResponseWriter, r *http.Request, req questionExportRequest) bool {
	if _, err := s.exportAuthoringTask(r, req); err != nil {
		writeJourneyError(w, err)
		return false
	}
	if req.Scope != "" && req.Scope != "personal" && req.Scope != "global" {
		writeError(w, 400, "无效的导出范围")
		return false
	}
	if req.Scope == "global" && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, 403, "无权导出全局题库")
		return false
	}
	if req.Scope == "personal" {
		if !s.hasPermission(r, domain.PermQuestionDownload) || !s.hasPermission(r, domain.PermQuestionView) {
			writeError(w, 403, "个人题库导出需要「题库下载」权限")
			return false
		}
		return true
	}
	return s.requireFormalBankAccess(w, r)
}

func (s *Server) requireFormalBankAccess(w http.ResponseWriter, r *http.Request) bool {
	if !s.canViewTier(r, domain.TierFormal) {
		writeError(w, 403, "题目导出仅限正式题库，且需要「查看正式题库」权限")
		return false
	}
	return true
}

// isExportableQuestion 只允许正式题库（published 定稿）的题目离开系统；
// 个人轴（scope=personal）按 C3 普通题库判据：本人确认的当前版本
// （library_state=active 且 confirmed_version==version，含 legacy published）。
func isExportableQuestion(q domain.A2Question) bool {
	return q.Status == domain.StatusPublished
}

// exportableInScope 按范围选择导出判据。
func (s *Server) exportableInScope(q domain.A2Question, scope string) bool {
	if scope == "personal" {
		return isPersonallyExportableQuestion(q)
	}
	return isExportableQuestion(q)
}

// isPersonallyExportableQuestion 个人普通题库导出判据。
func isPersonallyExportableQuestion(q domain.A2Question) bool {
	return q.PersonallyExportable()
}
