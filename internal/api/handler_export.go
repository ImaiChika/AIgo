package api

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"aigo/internal/domain"
	"aigo/internal/exporter"
	"aigo/internal/importer"
)

// handleExportXlsx 导出题目为 Excel 文件。
// 请求：{"question_ids": ["id1", "id2"]} 或 {"export_all": true}
// 返回文件下载。
func (s *Server) handleExportXlsx(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionIDs []string `json:"question_ids"` // 指定题目 ID 列表
		ExportAll   bool     `json:"export_all"`   // 导出全部
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	questions, err := s.getQuestionsToExport(r, req.QuestionIDs, req.ExportAll)
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
	filename := "题目_" + time.Now().Format("20060102_150405") + ".xlsx"
	path := filepath.Join(outputDir, filename)

	if err := importer.ExportToXlsx(questions, path); err != nil {
		writeError(w, 500, "导出失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"path":     path,
		"filename": filename,
		"count":    len(questions),
	})
}

// handleExportDocx 导出题目为 Word 文件。
func (s *Server) handleExportDocx(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionIDs []string `json:"question_ids"`
		ExportAll   bool     `json:"export_all"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	questions, err := s.getQuestionsToExport(r, req.QuestionIDs, req.ExportAll)
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
	filename := "题目_" + time.Now().Format("20060102_150405") + ".docx"
	path := filepath.Join(outputDir, filename)

	if err := exporter.ExportToDocx(questions, path); err != nil {
		writeError(w, 500, "导出失败: "+err.Error())
		return
	}

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

// getQuestionsToExport 获取要导出的题目列表。
func (s *Server) getQuestionsToExport(r *http.Request, ids []string, exportAll bool) ([]domain.A2Question, error) {
	if exportAll {
		return s.questionStore.ListQuestions(r.Context())
	}

	if len(ids) > 0 {
		var questions []domain.A2Question
		for _, id := range ids {
			q, err := s.questionStore.GetQuestion(r.Context(), id)
			if err != nil {
				return nil, err
			}
			if q != nil {
				questions = append(questions, *q)
			}
		}
		return questions, nil
	}

	return nil, nil
}
