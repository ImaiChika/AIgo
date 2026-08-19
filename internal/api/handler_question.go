package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// handleGenerate 调用千问大模型生成 A2 型试题。
// 请求：{"subject": "消化", "difficulty": "0.65", "topic": "消化性溃疡", "outline_code": "110.4.3.1.1", "count": 1, "bank_id": "bank-neike"}
// 返回：{"questions": [...], "count": N}
// 生成的题目自动保存到数据库，状态为 ai_draft 草稿。
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject     string `json:"subject"`      // 专业科目
		Category    string `json:"category"`     // 分类：基础医学/临床综合
		Difficulty  string `json:"difficulty"`   // 难度系数：0.55/0.65/0.75/0.85
		Topic       string `json:"topic"`        // 知识点主题
		OutlineCode string `json:"outline_code"` // 大纲代码
		Count       int    `json:"count"`        // 生成数量
		BankID      string `json:"bank_id"`      // 目标题库（分库）
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	// 设置默认值
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Subject == "" {
		req.Subject = "临床医学"
	}
	if req.Difficulty == "" {
		req.Difficulty = "0.65"
	}
	if req.Topic == "" {
		req.Topic = "常见症状鉴别诊断"
	}

	// 从数据库获取完整知识点信息（包含 Category、Unit、SubItem 等）
	kp := domain.KnowledgePoint{
		ID:          req.OutlineCode,
		Category:    req.Category,
		Subject:     req.Subject,
		Topic:       req.Topic,
		OutlineCode: req.OutlineCode,
	}
	if req.OutlineCode != "" {
		dbKP, err := s.kpSvc.GetByID(r.Context(), req.OutlineCode)
		if err == nil && dbKP != nil {
			kp = *dbKP // 使用数据库中的完整知识点
		}
	}
	genReq := domain.GenerationRequest{
		Subject:         req.Subject,
		Difficulty:      domain.Difficulty(req.Difficulty),
		KnowledgePoints: []domain.KnowledgePoint{kp},
		Count:           req.Count,
	}

	// 调用 pipeline 生成题目（内部会调千问API + 解析JSON + 存入数据库，状态为 ai_draft 草稿）
	questions, err := s.pipe.Generate(r.Context(), genReq)
	if err != nil {
		writeError(w, 500, "生成失败: "+err.Error())
		return
	}

	// 记录日志
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = "ai"
	}
	for _, q := range questions {
		if req.BankID != "" {
			q.BankIDs = []string{req.BankID}
		} else {
			// 未指定题库：按题目专业自动归入匹配的题库（专业范围自动归纳）
			if err := s.bankSvc.AssignBank(r.Context(), &q); err != nil {
				fmt.Printf("⚠ 自动归纳题库失败: %v\n", err)
			}
		}
		if err := s.questionStore.SaveQuestion(r.Context(), q); err != nil {
			fmt.Printf("⚠ 更新题目题库归属失败: %v\n", err)
		}
		s.auditSvc.LogCreate(r.Context(), q.ID, actor)
	}

	writeJSON(w, 200, map[string]any{
		"questions": questions,
		"count":     len(questions),
	})
}

// handleCreateQuestion 手工新建题目（状态为草稿）。
func (s *Server) handleCreateQuestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClinicalStem string `json:"clinical_stem"`
		Options      []struct {
			Label string `json:"label"`
			Text  string `json:"text"`
		} `json:"options"`
		Answer      string `json:"answer"`
		Explanation string `json:"explanation"`
		Difficulty  string `json:"difficulty"`
		BankID      string `json:"bank_id"`
		OutlineCode string `json:"outline_code"`
		Profession  string `json:"profession"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if strings.TrimSpace(req.ClinicalStem) == "" {
		writeError(w, 400, "题干不能为空")
		return
	}
	if len(req.Options) == 0 {
		writeError(w, 400, "至少需要一个选项")
		return
	}
	if req.Answer == "" {
		writeError(w, 400, "正确答案不能为空")
		return
	}
	// 答案必须存在于选项中
	answerValid := false
	for _, o := range req.Options {
		if o.Label == req.Answer {
			answerValid = true
			break
		}
	}
	if !answerValid {
		writeError(w, 400, "正确答案必须存在于选项中")
		return
	}
	difficulty := domain.Difficulty(req.Difficulty)
	if difficulty == "" {
		difficulty = domain.DifficultyMedium
	}
	now := time.Now()
	options := make([]domain.Option, len(req.Options))
	for i, o := range req.Options {
		options[i] = domain.Option{Label: o.Label, Text: o.Text}
	}
	q := domain.A2Question{
		ID:           fmt.Sprintf("q-manual-%d", now.UnixNano()),
		ClinicalStem: req.ClinicalStem,
		Options:      options,
		Answer:       req.Answer,
		Explanation:  req.Explanation,
		Difficulty:   difficulty,
		OutlineCode:  req.OutlineCode,
		Profession:   req.Profession,
		BankIDs:      nil,
		Status:       domain.StatusAIDraft,
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	// 指定了题库则加入；否则按题目专业自动归入匹配的题库
	if req.BankID != "" {
		q.BankIDs = []string{req.BankID}
	} else if err := s.bankSvc.AssignBank(r.Context(), &q); err != nil {
		fmt.Printf("⚠ 自动归纳题库失败: %v\n", err)
	}
	if err := s.questionStore.SaveQuestion(r.Context(), q); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	s.auditSvc.LogCreate(r.Context(), q.ID, actor)
	writeJSON(w, 201, q)
}

// handleListQuestions 列出所有题目（支持分页）。
// 按用户权限的题库范围过滤（bank_ids 空=全部）。
// 查询参数：page=页码（默认1），page_size=每页数量（默认100，最大500），bank_id=题库筛选。
func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	questions, err := s.questionStore.ListQuestions(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writePagedQuestions(w, s.filterByBankScope(r, questions), r)
}

// handleGetQuestion 根据 ID 获取单道题目详情。
func (s *Server) handleGetQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id") // 从 URL 路径提取题目 ID
	q, err := s.questionStore.GetQuestion(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if q == nil {
		writeError(w, 404, "题目不存在")
		return
	}
	writeJSON(w, 200, q)
}

// handleUpdateQuestion 修改题目内容（题干、选项、答案、解析）。
// 只更新请求中提供的字段，版本号自动递增。
func (s *Server) handleUpdateQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.questionStore.GetQuestion(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeError(w, 404, "题目不存在")
		return
	}

	var req struct {
		ClinicalStem string `json:"clinical_stem"` // 题干
		Options      []struct {
			Label string `json:"label"` // 选项标签 A-E
			Text  string `json:"text"`  // 选项内容
		} `json:"options"`
		Answer      string `json:"answer"`      // 正确答案
		Explanation string `json:"explanation"` // 解析
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	// 按字段更新（空值表示不修改）
	if req.ClinicalStem != "" {
		existing.ClinicalStem = req.ClinicalStem
	}
	if len(req.Options) > 0 {
		existing.Options = make([]domain.Option, len(req.Options))
		for i, o := range req.Options {
			existing.Options[i] = domain.Option{Label: o.Label, Text: o.Text}
		}
	}
	if req.Answer != "" {
		existing.Answer = req.Answer
	}
	if req.Explanation != "" {
		existing.Explanation = req.Explanation
	}

	// 修改已审核/已发布的题目时，回退状态到 ai_draft，需重新走审核流程
	if existing.Status == domain.StatusApproved || existing.Status == domain.StatusPublished || existing.Status == domain.StatusArchived {
		existing.Status = domain.StatusAIDraft
	}

	existing.Version++              // 版本号递增
	existing.UpdatedAt = time.Now() // 更新时间

	if err := s.questionStore.SaveQuestion(r.Context(), *existing); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}

	// 记录日志
	actor := auth.GetUsername(r.Context())
	s.auditSvc.LogUpdate(r.Context(), existing.ID, actor, "修改题目内容")

	writeJSON(w, 200, existing)
}

// handleDeleteQuestion 删除题目。
func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 记录日志（删除前记录，因为删除后就查不到了）
	actor := auth.GetUsername(r.Context())
	s.auditSvc.LogDelete(r.Context(), id, actor)

	if err := s.questionStore.DeleteQuestion(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handlePublishQuestion 将审核通过的题目发布到正式题库。
func (s *Server) handlePublishQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actor := auth.GetUsername(r.Context())
	if err := s.reviewSvc.PublishQuestion(r.Context(), id); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	// 记录发布日志
	s.auditSvc.LogPublish(r.Context(), id, actor)

	q, _ := s.questionStore.GetQuestion(r.Context(), id)
	writeJSON(w, 200, q)
}

// handleSearchQuestions 搜索题目（支持分页）。
// 准确筛选：status（状态）、profession（专业，逗号多选）、difficulty（难度）、
//
//	outline_code（大纲代码前缀）、bank_id（题库，__unclassified__=未分类）
//
// 模糊搜索：q（关键词，匹配题干/答案/ID）
func (s *Server) handleSearchQuestions(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	difficulty := r.URL.Query().Get("difficulty")
	outlineCode := r.URL.Query().Get("outline_code")
	ctx := r.Context()

	// 专业筛选：支持多选（逗号分隔）
	var professions []string
	if p := r.URL.Query().Get("profession"); p != "" {
		for _, part := range strings.Split(p, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				professions = append(professions, part)
			}
		}
	}

	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// 逐条筛选
	var filtered []domain.A2Question
	for _, q := range questions {
		// 按状态筛选（准确）
		if status != "" && string(q.Status) != status {
			continue
		}
		// 按难度筛选（准确）
		if difficulty != "" && string(q.Difficulty) != difficulty {
			continue
		}
		// 按大纲代码前缀筛选（准确）
		if outlineCode != "" && !strings.HasPrefix(q.OutlineCode, outlineCode) {
			continue
		}
		// 按专业筛选（准确）
		if len(professions) > 0 {
			matched := false
			for _, p := range professions {
				if q.Profession == p {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		// 按关键词模糊筛选（题干/答案/ID 包含关键词）
		if keyword != "" {
			keywordLower := strings.ToLower(keyword)
			idMatch := strings.Contains(strings.ToLower(q.ID), keywordLower)
			stemMatch := strings.Contains(strings.ToLower(q.ClinicalStem), keywordLower)
			answerMatch := strings.Contains(strings.ToLower(q.Answer), keywordLower)
			if !idMatch && !stemMatch && !answerMatch {
				continue
			}
		}
		filtered = append(filtered, q)
	}

	writePagedQuestions(w, s.filterByBankScope(r, filtered), r)
}

// handleListProfessions 返回题库中所有出现的专业值（供筛选下拉）。
func (s *Server) handleListProfessions(w http.ResponseWriter, r *http.Request) {
	questions, err := s.questionStore.ListQuestions(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	seen := make(map[string]bool)
	var result []string
	for _, q := range questions {
		if q.Profession != "" && !seen[q.Profession] {
			seen[q.Profession] = true
			result = append(result, q.Profession)
		}
	}
	sort.Strings(result)
	writeJSON(w, 200, map[string]any{"professions": result, "total": len(result)})
}

// handleAddQuestionBank 把题目加入指定题库（多对多，不影响其他库归属）。
func (s *Server) handleAddQuestionBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		BankID string `json:"bank_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if err := s.bankSvc.AddQuestionToBank(r.Context(), id, req.BankID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	q, _ := s.questionStore.GetQuestion(r.Context(), id)
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if q != nil {
		s.auditSvc.LogUpdate(r.Context(), id, actor, fmt.Sprintf("题目加入题库: %s", req.BankID))
	}
	writeJSON(w, 200, q)
}

// handleRemoveQuestionBank 把题目从指定题库移出（不影响其他库归属）。
func (s *Server) handleRemoveQuestionBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bankID := r.PathValue("bankId")
	if err := s.bankSvc.RemoveQuestionFromBank(r.Context(), id, bankID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	q, _ := s.questionStore.GetQuestion(r.Context(), id)
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if q != nil {
		s.auditSvc.LogUpdate(r.Context(), id, actor, fmt.Sprintf("题目移出题库: %s", bankID))
	}
	writeJSON(w, 200, q)
}

// handleMoveQuestionsBankBatch 批量把题目加入题库（多对多，含所有状态题目，高效 SQL）。
// 请求：{"ids": [...], "bank_id": "xxx"}
func (s *Server) handleMoveQuestionsBankBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string `json:"ids"`
		BankID string   `json:"bank_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, 400, "请至少选择一道题目")
		return
	}
	moved, err := s.bankSvc.AddQuestionsToBankBatch(r.Context(), req.IDs, req.BankID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if moved > 0 {
		s.auditSvc.LogUpdate(r.Context(), "", actor, fmt.Sprintf("批量加入 %d 道题到题库 %s", moved, req.BankID))
	}
	writeJSON(w, 200, map[string]any{"moved": moved, "failed": []string{}})
}

func bankIDLabel(bankID string) string {
	if bankID == "" {
		return "未分类"
	}
	return bankID
}

// filterByBankScope 按用户权限的题库范围过滤题目。
// 权限来自角色模板（全范围）或 bank_ids 为空时不过滤；
// 直接分配权限时仅保留 bank_ids 覆盖的题目。
// 同时支持 ?bank_id= 参数显式筛选。
func (s *Server) filterByBankScope(r *http.Request, questions []domain.A2Question) []domain.A2Question {
	userID := auth.GetUserID(r.Context())
	explicitBank := r.URL.Query().Get("bank_id")

	if userID != "" {
		scope, fullScope, _, err := s.authSvc.GetBankScope(r.Context(), userID, domain.PermQuestionView)
		if err == nil && !fullScope {
			bankSet := make(map[string]bool, len(scope))
			for _, b := range scope {
				bankSet[b] = true
			}
			var filtered []domain.A2Question
			for _, q := range questions {
				// 题目属于任一可见题库即可见（多对多）
				visible := false
				for _, b := range q.BankIDs {
					if bankSet[b] {
						visible = true
						break
					}
				}
				if visible {
					filtered = append(filtered, q)
				}
			}
			questions = filtered
		}
	}

	if explicitBank != "" {
		var filtered []domain.A2Question
		for _, q := range questions {
			for _, b := range q.BankIDs {
				if b == explicitBank {
					filtered = append(filtered, q)
					break
				}
			}
		}
		return filtered
	}
	return questions
}

// writePagedQuestions 对题目列表做分页并输出统一 JSON 结构。
// 支持 page（默认1）和 page_size（默认100，最大500）参数。
func writePagedQuestions(w http.ResponseWriter, questions []domain.A2Question, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 100
	}

	total := len(questions)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	writeJSON(w, 200, map[string]any{
		"questions": questions[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  end < total,
	})
}
