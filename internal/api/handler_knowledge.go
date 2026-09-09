package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/importer"
	"aigo/internal/knowledge"
	"aigo/internal/storage"
)

func kpError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, storage.ErrKnowledgeNotFound) {
		status = 404
	}
	if errors.Is(err, storage.ErrKnowledgeConflict) {
		status = 409
	}
	writeError(w, status, err.Error())
}
func (s *Server) logKnowledge(r *http.Request, action, detail string) {
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "knowledge_"+action, auth.GetUsername(r.Context()), detail)
	}
}
func (s *Server) handleListKP(w http.ResponseWriter, r *http.Request) { s.handleSearchKP(w, r) }
func (s *Server) handleSearchKP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var path []string
	if value := q.Get("path"); value != "" {
		if err := json.Unmarshal([]byte(value), &path); err != nil || len(path) > 4 {
			writeError(w, 400, "目录路径无效")
			return
		}
	}
	v, err := s.kpSvc.ResolveVersion(r.Context(), q.Get("version_id"))
	if errors.Is(err, storage.ErrKnowledgeNoDefault) {
		size, _ := strconv.Atoi(q.Get("page_size"))
		if size < 1 || size > 200 {
			size = 50
		}
		writeJSON(w, 200, map[string]any{"points": []domain.KnowledgePoint{}, "total": 0, "page": 1, "page_size": size, "has_more": false, "version": nil})
		return
	}
	if err != nil {
		kpError(w, err)
		return
	}
	points, err := s.kpSvc.SearchFiltered(r.Context(), knowledge.KPSearchOptions{VersionID: v.ID, Keyword: q.Get("q"), Subject: q.Get("subject"), Category: q.Get("category"), OutlineCode: q.Get("outline_code"), Path: path})
	if err != nil {
		kpError(w, err)
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("page_size"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	// Clamp before multiplying to avoid integer overflow on hostile page numbers.
	pages := (len(points) + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	end := min(start+size, len(points))
	writeJSON(w, 200, map[string]any{"points": points[start:end], "total": len(points), "page": page, "page_size": size, "has_more": end < len(points), "version": v})
}
func (s *Server) handleKPMeta(w http.ResponseWriter, r *http.Request) {
	v, err := s.kpSvc.ResolveVersion(r.Context(), r.URL.Query().Get("version_id"))
	if errors.Is(err, storage.ErrKnowledgeNoDefault) {
		writeJSON(w, 200, map[string]any{"categories": []string{}, "subjects": []string{}, "version": nil})
		return
	}
	if err != nil {
		kpError(w, err)
		return
	}
	categories, subjects, err := s.kpSvc.ListCategoriesAndSubjects(r.Context(), v.ID)
	if err != nil {
		kpError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"categories": categories, "subjects": subjects, "version": v})
}
func (s *Server) handleKPTree(w http.ResponseWriter, r *http.Request) {
	v, err := s.kpSvc.ResolveVersion(r.Context(), r.URL.Query().Get("version_id"))
	if errors.Is(err, storage.ErrKnowledgeNoDefault) {
		writeJSON(w, 200, map[string]any{"tree": []*knowledge.TreeNode{}, "version": nil})
		return
	}
	if err != nil {
		kpError(w, err)
		return
	}
	tree, err := s.kpSvc.Tree(r.Context(), v.ID)
	if err != nil {
		kpError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tree": tree, "version": v})
}
func (s *Server) handleKPVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.kpSvc.Versions(r.Context())
	if err != nil {
		kpError(w, err)
		return
	}
	latest := ""
	for _, v := range versions {
		if v.Status == "published" {
			latest = v.ID
			break
		}
	}
	writeJSON(w, 200, map[string]any{"versions": versions, "default_version_id": latest})
}
func (s *Server) handleCreateKPVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Year        int    `json:"year"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	v, err := s.kpSvc.CreateVersion(r.Context(), req.Name, req.Year, req.Description)
	if err != nil {
		kpError(w, err)
		return
	}
	s.logKnowledge(r, "version_create", fmt.Sprintf("创建 %s (%d)", v.Name, v.Year))
	writeJSON(w, 201, v)
}
func (s *Server) handlePublishKPVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.kpSvc.PublishVersion(r.Context(), id); err != nil {
		kpError(w, err)
		return
	}
	s.logKnowledge(r, "version_publish", "启用大纲版本 "+id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func (s *Server) handleCreateKP(w http.ResponseWriter, r *http.Request) {
	var req domain.KnowledgePoint
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	p, err := s.kpSvc.CreatePoint(r.Context(), req)
	if err != nil {
		kpError(w, err)
		return
	}
	s.logKnowledge(r, "create", fmt.Sprintf("%s / %s / %s", p.VersionName, p.OutlineCode, p.Topic))
	writeJSON(w, 201, p)
}
func (s *Server) handleUpdateKP(w http.ResponseWriter, r *http.Request) {
	var req domain.KnowledgePoint
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	p, err := s.kpSvc.UpdatePoint(r.Context(), r.PathValue("id"), req)
	if err != nil {
		kpError(w, err)
		return
	}
	s.logKnowledge(r, "update", fmt.Sprintf("%s / %s / 修订 %d / %s", p.VersionName, p.OutlineCode, p.Revision, p.Topic))
	writeJSON(w, 200, p)
}
func (s *Server) handleDeleteKP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.kpSvc.GetByID(r.Context(), id)
	if err != nil {
		kpError(w, err)
		return
	}
	if p == nil {
		kpError(w, storage.ErrKnowledgeNotFound)
		return
	}
	if err := s.kpSvc.DeletePoint(r.Context(), id); err != nil {
		kpError(w, err)
		return
	}
	s.logKnowledge(r, "delete", fmt.Sprintf("%s / %s / %s", p.VersionName, p.OutlineCode, p.Topic))
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}
func (s *Server) handleImportKP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, 400, "上传失败，文件总大小不得超过 32 MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["files"]
	files = append(files, r.MultipartForm.File["file"]...)
	if len(files) == 0 || len(files) > 20 {
		writeError(w, 400, "请选择 1–20 个文件")
		return
	}
	versionID := r.FormValue("version_id")
	mode := r.FormValue("mode")
	if mode != "" && mode != "merge" && mode != "replace" {
		writeError(w, 400, "无效的导入方式")
		return
	}
	dir, err := os.MkdirTemp("", "aigo-outline-*")
	if err != nil {
		writeError(w, 500, "无法创建导入临时目录")
		return
	}
	defer os.RemoveAll(dir)
	paths := []string{}
	names := []string{}
	for i, header := range files {
		name := filepath.Base(header.Filename)
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".xlsx" && ext != ".csv" && ext != ".docx" {
			writeError(w, 400, name+"：仅支持 xlsx、csv、docx")
			return
		}
		file, err := header.Open()
		if err != nil {
			writeError(w, 400, "无法读取 "+name)
			return
		}
		// A separate directory per file preserves useful original names in diagnostics.
		subdir := filepath.Join(dir, strconv.Itoa(i))
		err = os.Mkdir(subdir, 0700)
		if err != nil {
			file.Close()
			writeError(w, 500, "无法保存文件")
			return
		}
		path := filepath.Join(subdir, name)
		err = saveUploadedFile(path, file)
		file.Close()
		if err != nil {
			writeError(w, 500, "保存文件失败")
			return
		}
		paths = append(paths, path)
		names = append(names, name)
	}
	inserted, updated, duplicated, err := s.kpSvc.ImportDocuments(r.Context(), versionID, paths, mode == "replace")
	if err != nil {
		kpError(w, errors.New(strings.ReplaceAll(err.Error(), dir+string(filepath.Separator), "")))
		return
	}
	v, err := s.kpSvc.ResolveVersion(r.Context(), versionID)
	if err != nil {
		kpError(w, err)
		return
	}
	s.logKnowledge(r, "import", fmt.Sprintf("%s / %s / 文件 %s / 新增 %d，更新 %d，重复 %d", v.Name, mode, strings.Join(names, "、"), inserted, updated, duplicated))
	writeJSON(w, 200, map[string]any{"imported": inserted, "updated": updated, "duplicated": duplicated, "total": v.PointCount, "version": v, "files": names})
}

// handleExportKP exports the selected syllabus version as a re-importable Excel file.
func (s *Server) handleExportKP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VersionID string `json:"version_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	v, err := s.kpSvc.ResolveVersion(r.Context(), req.VersionID)
	if err != nil {
		kpError(w, err)
		return
	}
	points, err := s.kpSvc.SearchFiltered(r.Context(), knowledge.KPSearchOptions{VersionID: v.ID})
	if err != nil {
		kpError(w, err)
		return
	}
	if len(points) == 0 {
		writeError(w, http.StatusBadRequest, "当前版本没有可导出的知识点")
		return
	}

	outputDir := "output/exports"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建导出目录")
		return
	}
	// 使用 ASCII 文件名，避免部分浏览器将中文 Content-Disposition 按 Latin-1 解码。
	filename := "knowledge-points_" + strings.TrimPrefix(newExportFilename("xlsx"), "题目_")
	path := filepath.Join(outputDir, filename)
	if err := importer.ExportKnowledgePointsToXlsx(points, path); err != nil {
		writeError(w, http.StatusInternalServerError, "知识点导出失败: "+err.Error())
		return
	}
	defer os.Remove(path)
	s.logKnowledge(r, "export", fmt.Sprintf("导出 %s，共 %d 个知识点", v.Name, len(points)))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	http.ServeFile(w, r, path)
}

func (s *Server) handleDeleteKPVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.kpSvc.ResolveVersion(r.Context(), id)
	if err != nil {
		kpError(w, err)
		return
	}
	count, err := s.kpSvc.DeleteVersion(r.Context(), id)
	if err != nil {
		kpError(w, err)
		return
	}
	s.logKnowledge(r, "version_delete", fmt.Sprintf("删除大纲版本 %s / %s / %d 个知识点；保留历史题目与任务快照", id, v.Name, count))
	writeJSON(w, 200, map[string]any{"id": id, "deleted_count": count, "status": "ok"})
}
