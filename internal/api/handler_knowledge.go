package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"aigo/internal/domain"
)

// handleListKP 列出知识点（支持分页和按系统筛选）。
// 查询参数：system=系统名，page=页码，page_size=每页数量。
func (s *Server) handleListKP(w http.ResponseWriter, r *http.Request) {
	system := r.URL.Query().Get("system")
	ctx := r.Context()

	// 按系统筛选或列出全部
	var points []domain.KnowledgePoint
	var err error
	if system != "" {
		points, err = s.kpSvc.ListBySystem(ctx, system)
	} else {
		points, err = s.kpSvc.ListAll(ctx)
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// 分页处理
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(points) {
		start = len(points)
	}
	if end > len(points) {
		end = len(points)
	}

	writeJSON(w, 200, map[string]any{
		"points":    points[start:end],
		"total":     len(points),
		"page":      page,
		"page_size": pageSize,
	})
}

// handleSearchKP 搜索知识点（按名称或关键词匹配）。
// 查询参数：q=搜索关键词。
func (s *Server) handleSearchKP(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("q")
	if keyword == "" {
		writeError(w, 400, "缺少搜索关键词 q")
		return
	}
	points, err := s.kpSvc.Search(r.Context(), keyword)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"points": points,
		"total":  len(points),
	})
}

// handleCreateKP 创建单个知识点。
// 请求：{"topic": "肺炎", "system": "呼吸内科", "keywords": ["发热","咳嗽"]}
func (s *Server) handleCreateKP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string   `json:"id"`       // 可选，不填自动生成
		Subject  string   `json:"subject"`  // 科目，默认"临床医学"
		System   string   `json:"system"`   // 所属系统
		Topic    string   `json:"topic"`    // 知识点名称（必填）
		Keywords []string `json:"keywords"` // 关键词列表
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.Topic == "" {
		writeError(w, 400, "知识点名称不能为空")
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("kp-custom-%d", time.Now().UnixNano())
	}
	if req.Subject == "" {
		req.Subject = "临床医学"
	}

	kp := domain.KnowledgePoint{
		ID:       req.ID,
		Subject:  req.Subject,
		System:   req.System,
		Topic:    req.Topic,
		Keywords: req.Keywords,
	}
	if _, err := s.kpSvc.SavePoints(r.Context(), []domain.KnowledgePoint{kp}); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, 201, kp)
}

// handleDeleteKP 根据 ID 删除知识点。
func (s *Server) handleDeleteKP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.kpSvc.DeletePoint(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handleImportKP 从 Excel 文件批量导入知识点。
// 请求格式：multipart/form-data，字段名 "file"。
func (s *Server) handleImportKP(w http.ResponseWriter, r *http.Request) {
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "缺少文件字段 file: "+err.Error())
		return
	}
	defer file.Close()

	// 保存到临时文件
	tmpPath := "/tmp/aigo_kp_import_" + header.Filename
	if err := saveUploadedFile(tmpPath, file); err != nil {
		writeError(w, 500, "保存临时文件失败: "+err.Error())
		return
	}

	// 调用知识点服务解析并导入
	count, err := s.kpSvc.ImportFromXlsx(r.Context(), tmpPath)
	if err != nil {
		writeError(w, 500, "导入失败: "+err.Error())
		return
	}

	total, _ := s.kpSvc.Count(r.Context())
	writeJSON(w, 200, map[string]any{
		"imported": count, // 本次导入数量
		"total":    total, // 导入后总量
	})
}
