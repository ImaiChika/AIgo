package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// handleLogin 用户登录。
// 请求：{"username": "xxx", "password": "xxx"}
// 成功返回：{"token": "JWT字符串", "user": {用户信息}}
// 失败返回：401 {"error": "用户名或密码错误"}
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}

	// 调用认证服务验证密码并生成 JWT
	token, user, err := s.authSvc.Login(req.Username, req.Password)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"token": token, // 前端保存此 token，后续请求带在 Authorization 头
		"user":  user,  // 用户基本信息（不含密码）
	})
}

// handleMe 获取当前登录用户信息。
// 需要 Bearer token，从 token 中解析用户 ID 后查询数据库。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context()) // 从 context 获取中间件注入的用户 ID
	user, err := s.authSvc.GetUserByID(userID)
	if err != nil || user == nil {
		writeError(w, 401, "用户不存在")
		return
	}
	writeJSON(w, 200, user)
}

// handleUpdateProfile 修改当前用户的昵称。
// 请求：{"display_name": "新昵称"}
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.DisplayName == "" {
		writeError(w, 400, "昵称不能为空")
		return
	}
	// 更新数据库中的昵称
	if err := s.authSvc.UpdateDisplayName(userID, req.DisplayName); err != nil {
		writeError(w, 500, "更新失败")
		return
	}
	// 返回更新后的完整用户信息
	user, _ := s.authSvc.GetUserByID(userID)
	writeJSON(w, 200, user)
}

// handleChangePassword 修改当前用户的密码。
// 请求：{"old_password": "旧密码", "new_password": "新密码"}
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, 400, "新密码至少8位")
		return
	}
	if err := s.authSvc.ChangePassword(userID, req.OldPassword, req.NewPassword); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleRegisterEnabled 查询当前是否开放自助注册（公开接口，供登录页控制注册入口显示）。
func (s *Server) handleRegisterEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"enabled": s.registerEnabled})
}

// handleRegister 用户自主注册。
// 注册后默认无任何权限，由管理员在用户管理中分配权限。
// 受 AIGO_REGISTER_ENABLED 配置控制：默认关闭，防止公网被随意注册。
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registerEnabled {
		writeError(w, 403, "注册功能未开放，请联系管理员创建账号")
		return
	}
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	user, err := s.authSvc.Register(req.Username, req.Password, req.DisplayName)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.auditSvc.LogUser(r.Context(), user.Username, user.Username, "register")
	writeJSON(w, 201, user)
}

// handleListUsers 管理员查看所有用户列表（含权限）。
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.authSvc.ListUsers()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"users": users, "total": len(users)})
}

// handleCreateUser 管理员创建新用户。
// 请求：{"username","password","display_name","role","permissions":[],"bank_ids":[]}
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string   `json:"username"`
		Password    string   `json:"password"`
		DisplayName string   `json:"display_name"`
		Role        string   `json:"role"`
		Permissions []string `json:"permissions"`
		BankIDs     []string `json:"bank_ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	user, err := s.authSvc.CreateUser(req.Username, req.Password, req.DisplayName, req.Role, req.Permissions, req.BankIDs)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.auditSvc.LogUser(r.Context(), user.Username, auth.GetUsername(r.Context()), "create")
	// 有审题权限的用户自动同步专家库
	s.syncUserToExperts(r, user)
	writeJSON(w, 201, user)
}

// handleUpdateUser 管理员更新用户：角色模板、权限勾选、题库范围、启用状态、显示名。
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var req struct {
		DisplayName string   `json:"display_name"`
		Role        string   `json:"role"`
		Permissions []string `json:"permissions"`
		BankIDs     []string `json:"bank_ids"`
		Enabled     *bool    `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	user, err := s.authSvc.UpdateUser(userID, req.DisplayName, req.Role, req.Permissions, req.BankIDs, req.Enabled)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.auditSvc.LogUser(r.Context(), user.Username, auth.GetUsername(r.Context()), "update_permissions")
	s.syncUserToExperts(r, user)
	writeJSON(w, 200, user)
}

// syncUserToExperts 有审题权限的用户自动在专家库建立记录（ID 一致）。
func (s *Server) syncUserToExperts(r *http.Request, user *auth.User) {
	hasReview := false
	for _, p := range user.Permissions {
		if p == domain.PermReviewDo {
			hasReview = true
			break
		}
	}
	if !hasReview {
		return
	}
	existing, _ := s.reviewSvc.GetExpert(r.Context(), user.ID)
	if existing == nil {
		expert := domain.Expert{
			ID:      user.ID,
			Name:    user.DisplayName,
			Enabled: true,
		}
		s.reviewSvc.CreateExpert(r.Context(), expert)
	}
}

// handleListRoles 列出所有角色模板。
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.authSvc.ListRoles(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"roles": roles, "total": len(roles)})
}

// handleCreateRole 创建自定义角色模板。
func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("role-%d", time.Now().UnixNano())
	}
	role := domain.Role{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
	}
	if err := s.authSvc.SaveRole(r.Context(), role); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.auditSvc.Log(r.Context(), "", "role_create", auth.GetUsername(r.Context()), fmt.Sprintf("创建角色 %s", role.ID))
	writeJSON(w, 201, role)
}

// handleUpdateRole 更新角色模板（含内置角色）。
func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	id := r.PathValue("id")
	existing, err := s.authSvc.GetRole(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeError(w, 404, "角色不存在")
		return
	}
	existing.Name = req.Name
	existing.Description = req.Description
	existing.Permissions = req.Permissions
	if err := s.authSvc.SaveRole(r.Context(), *existing); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.auditSvc.Log(r.Context(), "", "role_update", auth.GetUsername(r.Context()), fmt.Sprintf("修改角色 %s", id))
	writeJSON(w, 200, existing)
}

// handleDeleteRole 删除角色模板（有用户引用时拒绝）。
func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.authSvc.DeleteRole(r.Context(), id); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.auditSvc.Log(r.Context(), "", "role_delete", auth.GetUsername(r.Context()), fmt.Sprintf("删除角色 %s", id))
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handleListBanks 列出所有题库（含各题库题量与状态分布，供冲突提示）。
func (s *Server) handleListBanks(w http.ResponseWriter, r *http.Request) {
	banks, err := s.bankSvc.ListBanks(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 统计各题库题量与状态分布（多对多：题目可属于多个题库）
	counts := make(map[string]int)
	statusCounts := make(map[string]map[string]int)
	questions, qErr := s.questionStore.ListQuestions(r.Context())
	if qErr == nil {
		for _, q := range questions {
			for _, b := range q.BankIDs {
				counts[b]++
				if statusCounts[b] == nil {
					statusCounts[b] = make(map[string]int)
				}
				statusCounts[b][string(q.Status)]++
			}
		}
	}
	type bankWithStats struct {
		domain.QuestionBank
		QuestionCount int            `json:"question_count"`
		StatusCounts  map[string]int `json:"status_counts"`
	}
	result := make([]bankWithStats, 0, len(banks))
	for _, b := range banks {
		result = append(result, bankWithStats{QuestionBank: b, QuestionCount: counts[b.ID], StatusCounts: statusCounts[b.ID]})
	}
	writeJSON(w, 200, map[string]any{"banks": result, "total": len(result)})
}

// handleCreateBank 创建题库。指定专业范围时自动归纳存量未分类题目。
func (s *Server) handleCreateBank(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Professions []string `json:"professions"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	bank, err := s.bankSvc.CreateBank(r.Context(), req.ID, req.Name, req.Description, req.Professions)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, bank)
}

// handleUpdateBank 更新题库（修改专业范围后自动重新归纳）。
func (s *Server) handleUpdateBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Professions []string `json:"professions"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	bank, err := s.bankSvc.UpdateBank(r.Context(), id, req.Name, req.Description, req.Professions)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, bank)
}

// handleDeleteBank 删除题库（题目保留为未分类）。
func (s *Server) handleDeleteBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.bankSvc.DeleteBank(r.Context(), id); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handleListBankQuestions 列出题库内的题目（手动微调进出用，支持搜索）。
func (s *Server) handleListBankQuestions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	questions, err := s.questionStore.ListQuestions(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	keyword := strings.ToLower(r.URL.Query().Get("q"))
	var inBank []domain.A2Question
	for _, q := range questions {
		// 题目属于该题库（多对多）
		inThis := false
		for _, b := range q.BankIDs {
			if b == id {
				inThis = true
				break
			}
		}
		if !inThis {
			continue
		}
		if keyword != "" {
			if !strings.Contains(strings.ToLower(q.ClinicalStem), keyword) &&
				!strings.Contains(strings.ToLower(q.ID), keyword) &&
				!strings.Contains(strings.ToLower(q.Profession), keyword) {
				continue
			}
		}
		inBank = append(inBank, q)
	}
	writePagedQuestions(w, inBank, r)
}

// handleCollectBank 重新归纳：把未分类且专业匹配的题目自动归入题库。
func (s *Server) handleCollectBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bank, err := s.bankSvc.GetBank(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if bank == nil {
		writeError(w, 404, "题库不存在")
		return
	}
	count, err := s.bankSvc.Collect(r.Context(), *bank)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "collected": count})
}
