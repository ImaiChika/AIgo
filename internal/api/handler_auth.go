package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
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
	req.Username = strings.TrimSpace(req.Username)
	clientIP := s.clientIP(r)
	wait, err := s.authSvc.CheckLoginAllowed(r.Context(), req.Username, clientIP)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "登录服务暂时不可用")
		return
	}
	if wait > 0 {
		writeRateLimited(w, wait)
		return
	}

	// 调用认证服务验证密码并生成 JWT
	token, user, err := s.authSvc.Login(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrUserDisabled) {
			blockedFor, limitErr := s.authSvc.RecordLoginFailure(r.Context(), req.Username, clientIP)
			if limitErr != nil {
				writeError(w, http.StatusServiceUnavailable, "登录服务暂时不可用")
				return
			}
			s.logAuthenticationFailure(r, req.Username, clientIP, blockedFor > 0)
			if blockedFor > 0 {
				writeRateLimited(w, blockedFor)
				return
			}
			writeError(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		writeError(w, http.StatusInternalServerError, "登录服务暂时不可用")
		return
	}
	if err := s.authSvc.RecordLoginSuccess(r.Context(), req.Username); err != nil {
		writeError(w, http.StatusServiceUnavailable, "登录服务暂时不可用")
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
	user, err := s.authSvc.GetUserByIDForRole(r.Context(), userID, auth.GetRole(r.Context()))
	if err != nil || user == nil {
		writeError(w, 401, "用户不存在")
		return
	}
	writeJSON(w, 200, user)
}

// handleMySummary 返回个人中心需要的轻量累计指标，不暴露题目内容或他人数据。
func (s *Server) handleMySummary(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	_, generated, err := s.questionStore.SearchQuestions(r.Context(), storage.QuestionFilter{OwnerID: userID}, 1, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取个人数据失败")
		return
	}
	_, newQuestions, err := s.questionStore.SearchQuestions(r.Context(), storage.QuestionFilter{
		OwnerID: userID, Status: string(domain.StatusAIReviewed),
		Tiers: []string{string(domain.TierWorking)}, NoReviewTask: true,
	}, 1, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取待提交新题失败")
		return
	}
	reviewed := 0
	if s.reviewSvc != nil {
		if tasks, taskErr := s.reviewSvc.ListAllTasks(r.Context()); taskErr == nil {
			for _, task := range tasks {
				records, recordErr := s.reviewSvc.ListRecords(r.Context(), task.ID)
				if recordErr != nil {
					continue
				}
				for _, record := range records {
					if record.ExpertID == userID {
						reviewed++
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"generated_count":     generated,
		"reviewed_count":      reviewed,
		"new_questions_count": newQuestions,
	})
}

// handleSwitchRole 切换当前账号的工作身份。角色集合由管理员维护，服务端
// 重新签发 JWT，后续接口的权限只取所选身份及用户直接授权。
func (s *Server) handleSwitchRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		writeError(w, http.StatusBadRequest, "请选择要切换的身份")
		return
	}
	token, user, err := s.authSvc.SwitchRole(r.Context(), auth.GetUserID(r.Context()), role)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
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
	user, _ := s.authSvc.GetUserByIDForRole(r.Context(), userID, auth.GetRole(r.Context()))
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
// 受 AIGO_REGISTER_ENABLED 配置控制：默认开启；注册接口另有来源 IP 频率限制。
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
	clientIP := s.clientIP(r)
	wait, err := s.authSvc.CheckRegistrationAllowed(r.Context(), clientIP)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "注册服务暂时不可用")
		return
	}
	if wait > 0 {
		writeRateLimited(w, wait)
		return
	}
	blockedFor, err := s.authSvc.RecordRegistrationAttempt(r.Context(), clientIP)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "注册服务暂时不可用")
		return
	}
	if blockedFor > 0 {
		writeRateLimited(w, blockedFor)
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

func (s *Server) logAuthenticationFailure(r *http.Request, username, clientIP string, limited bool) {
	if s.auditSvc == nil {
		return
	}
	action := "auth_login_failed"
	if limited {
		action = "auth_rate_limited"
	}
	detail := fmt.Sprintf("account=%s source=%s", auth.AuditSubject("account", strings.ToLower(strings.TrimSpace(username))), auth.AuditSubject("ip", clientIP))
	_ = s.auditSvc.Log(r.Context(), "", action, "anonymous", detail)
}

func writeRateLimited(w http.ResponseWriter, delay time.Duration) {
	seconds := int((delay + time.Second - 1) / time.Second)
	seconds = ((seconds + 9) / 10) * 10
	if seconds < 10 {
		seconds = 10
	}
	if seconds > 300 {
		seconds = 300
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	writeError(w, http.StatusTooManyRequests, "操作过于频繁，请稍后再试")
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
		Roles       []string `json:"roles"`
		Permissions []string `json:"permissions"`
		BankIDs     []string `json:"bank_ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	user, err := s.authSvc.CreateUserAsWithRoles(r.Context(), auth.GetUserID(r.Context()), req.Username, req.Password, req.DisplayName, req.Role, req.Roles, req.Permissions, req.BankIDs)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrSuperAdminOnly) || errors.Is(err, auth.ErrSuperAdminExists) || errors.Is(err, auth.ErrSuperAdminRoleNotAssignable) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
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
		Roles       []string `json:"roles"`
		Permissions []string `json:"permissions"`
		BankIDs     []string `json:"bank_ids"`
		Enabled     *bool    `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if s.reviewSvc != nil {
		current, currentErr := s.authSvc.GetUserByID(userID)
		roles := append([]string(nil), req.Roles...)
		if req.Role != "" && !slices.Contains(roles, req.Role) {
			roles = append([]string{req.Role}, roles...)
		}
		futurePermissions, permissionErr := s.authSvc.PermissionsForAssignments(r.Context(), roles, req.Permissions)
		futureEnabled := current != nil && current.Enabled
		if req.Enabled != nil {
			futureEnabled = *req.Enabled
		}
		losesReviewRole := current != nil && slices.Contains(current.Permissions, domain.PermReviewDo) && !slices.Contains(futurePermissions, domain.PermReviewDo)
		losesFinalRole := current != nil && slices.Contains(current.Permissions, domain.PermReviewFinal) && !slices.Contains(futurePermissions, domain.PermReviewFinal)
		if currentErr == nil && permissionErr == nil && current != nil && (!futureEnabled || losesReviewRole || losesFinalRole) {
			references, referenceErr := s.reviewSvc.UserAssignmentReferences(r.Context(), userID)
			if referenceErr != nil {
				writeError(w, http.StatusInternalServerError, "检查审核任务引用失败")
				return
			}
			if len(references) > 0 {
				writeError(w, http.StatusConflict, "该用户仍被审核流程或进行中任务引用，请先调整："+strings.Join(references, "；"))
				return
			}
		}
	}
	user, err := s.authSvc.UpdateUserAsWithRoles(r.Context(), auth.GetUserID(r.Context()), userID, req.DisplayName, req.Role, req.Roles, req.Permissions, req.BankIDs, req.Enabled)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrSuperAdminOnly) || errors.Is(err, auth.ErrProtectedAccount) || errors.Is(err, auth.ErrSuperAdminRoleNotAssignable) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
	s.auditSvc.LogUser(r.Context(), user.Username, auth.GetUsername(r.Context()), "update_permissions")
	s.syncUserToExperts(r, user)
	writeJSON(w, 200, user)
}

// handleDeleteUser 删除普通用户。超级管理员账号和当前操作账号由服务层保护。
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if s.reviewSvc != nil {
		references, err := s.reviewSvc.UserAssignmentReferences(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "检查审核任务引用失败")
			return
		}
		if len(references) > 0 {
			writeError(w, http.StatusConflict, "该用户仍被审核流程或进行中任务引用，请先调整："+strings.Join(references, "；"))
			return
		}
	}
	deleted, err := s.authSvc.DeleteUserAs(r.Context(), auth.GetUserID(r.Context()), userID)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrSuperAdminOnly) || errors.Is(err, auth.ErrProtectedAccount) {
			status = http.StatusForbidden
		} else if errors.Is(err, auth.ErrUserHasWork) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	s.auditSvc.LogUser(r.Context(), deleted.Username, auth.GetUsername(r.Context()), "delete")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": deleted.ID})
}

// syncUserToExperts 有审题权限的用户自动在专家库建立记录（ID 一致）。
func (s *Server) syncUserToExperts(r *http.Request, user *auth.User) {
	if s.reviewSvc == nil {
		return
	}
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
	if err := s.authSvc.SaveRoleAs(r.Context(), auth.GetUserID(r.Context()), role); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrSuperAdminOnly) || errors.Is(err, auth.ErrProtectedRole) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
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
	// 收回审题/把关权限前，确认持有该角色的用户没有被审核流程或进行中任务引用，
	// 防止角色模板编辑绕过用户级的审核人生命周期保护（与 handleUpdateUser 同口径）。
	if !existing.IsBuiltin && s.reviewSvc != nil {
		losesReview := slices.Contains(existing.Permissions, domain.PermReviewDo) && !slices.Contains(req.Permissions, domain.PermReviewDo)
		losesFinal := slices.Contains(existing.Permissions, domain.PermReviewFinal) && !slices.Contains(req.Permissions, domain.PermReviewFinal)
		if losesReview || losesFinal {
			users, userErr := s.authSvc.ListUsers()
			if userErr != nil {
				writeError(w, http.StatusInternalServerError, "检查角色持有用户失败")
				return
			}
			seen := map[string]bool{}
			var references []string
			for _, u := range users {
				if !slices.Contains(u.Roles, id) {
					continue
				}
				refs, refErr := s.reviewSvc.UserAssignmentReferences(r.Context(), u.ID)
				if refErr != nil {
					writeError(w, http.StatusInternalServerError, "检查审核任务引用失败")
					return
				}
				for _, ref := range refs {
					if !seen[ref] {
						seen[ref] = true
						references = append(references, ref)
					}
				}
			}
			if len(references) > 0 {
				writeError(w, http.StatusConflict, "该角色仍被审核流程或进行中任务引用的用户持有，请先调整："+strings.Join(references, "；"))
				return
			}
		}
	}
	existing.Name = req.Name
	existing.Description = req.Description
	existing.Permissions = req.Permissions
	if err := s.authSvc.SaveRoleAs(r.Context(), auth.GetUserID(r.Context()), *existing); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrSuperAdminOnly) || errors.Is(err, auth.ErrProtectedRole) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
	s.auditSvc.Log(r.Context(), "", "role_update", auth.GetUsername(r.Context()), fmt.Sprintf("修改角色 %s", id))
	writeJSON(w, 200, existing)
}

// handleDeleteRole 删除角色模板（有用户引用时拒绝）。
func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.authSvc.DeleteRoleAs(r.Context(), auth.GetUserID(r.Context()), id); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrSuperAdminOnly) || errors.Is(err, auth.ErrProtectedRole) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
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
	catalogScope, catalogRestricted := s.bankCatalogScope(r)
	if catalogRestricted {
		visible := banks[:0]
		for _, bank := range banks {
			if catalogScope[bank.ID] {
				visible = append(visible, bank)
			}
		}
		banks = visible
	}
	includeStats := r.URL.Query().Get("include_stats") != "false"
	// 分类子题库只管理待审核层，因此题量与状态分布不计正式/淘汰题。
	counts := make(map[string]int)
	statusCounts := make(map[string]map[string]int)
	if includeStats {
		questions, qErr := s.questionStore.ListQuestions(r.Context())
		if qErr == nil {
			globalCatalog := s.hasPermission(r, domain.PermQuestionViewGlobal)
			viewerID := auth.GetUserID(r.Context())
			for _, q := range questions {
				if q.Tier() != domain.TierWorking {
					continue
				}
				if !globalCatalog && q.OwnerID != viewerID {
					continue
				}
				for _, b := range q.BankIDs {
					counts[b]++
					if statusCounts[b] == nil {
						statusCounts[b] = make(map[string]int)
					}
					statusCounts[b][string(q.Status)]++
				}
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

// handleCreateBank 创建题库。指定专业范围时自动归纳存量待归类题目。
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
	actor := auth.GetUsername(r.Context())
	detail := fmt.Sprintf("创建题库「%s」（专业范围 %v）", req.Name, req.Professions)
	ok := s.withAuditedTx(w, r, "创建题库",
		func(txCtx context.Context) error {
			_, err := s.bankSvc.CreateBank(txCtx, req.ID, req.Name, req.Description, req.Professions)
			if err != nil {
				writeError(w, 400, err.Error())
				return errResponded
			}
			return nil
		},
		func(txCtx context.Context) error {
			return s.auditSvc.Log(txCtx, "", "bank_create", actor, detail)
		})
	if !ok {
		return
	}
	bank, _ := s.bankSvc.GetBank(r.Context(), req.ID)
	if bank == nil {
		bank = &domain.QuestionBank{ID: req.ID, Name: req.Name, Description: req.Description, Professions: req.Professions}
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
	actor := auth.GetUsername(r.Context())
	ok := s.withAuditedTx(w, r, "更新题库",
		func(txCtx context.Context) error {
			_, err := s.bankSvc.UpdateBank(txCtx, id, req.Name, req.Description, req.Professions)
			if err != nil {
				writeError(w, 400, err.Error())
				return errResponded
			}
			return nil
		},
		func(txCtx context.Context) error {
			return s.auditSvc.Log(txCtx, "", "bank_update", actor, fmt.Sprintf("更新题库「%s」（专业范围 %v）", req.Name, req.Professions))
		})
	if !ok {
		return
	}
	bank, _ := s.bankSvc.GetBank(r.Context(), id)
	if bank == nil {
		bank = &domain.QuestionBank{ID: id, Name: req.Name, Description: req.Description, Professions: req.Professions}
	}
	writeJSON(w, 200, bank)
}

// handleDeleteBank 删除题库（题目解除分类子题库归属，进入待归类状态）。
func (s *Server) handleDeleteBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	users, err := s.authSvc.ListUsers()
	if err != nil {
		writeError(w, 500, "检查用户题库范围失败")
		return
	}
	for _, user := range users {
		for _, bankID := range user.BankIDs {
			if bankID == id {
				writeError(w, 409, fmt.Sprintf("题库仍分配给用户「%s」，请先移除该用户的题库范围", user.DisplayName))
				return
			}
		}
	}
	flows, err := s.reviewSvc.ListFlows(r.Context())
	if err != nil {
		writeError(w, 500, "检查审核流程引用失败")
		return
	}
	for _, flow := range flows {
		if flow.BankID == id {
			writeError(w, 409, fmt.Sprintf("题库正被审核流程「%s」使用，请先处理该流程", flow.Name))
			return
		}
	}
	used, err := s.reviewSvc.HasSubmissionBankHistory(r.Context(), id)
	if err != nil {
		writeError(w, 500, "检查历史审核任务失败")
		return
	}
	if used {
		writeError(w, 409, "题库已有审核历史，需永久保留用于追溯，不能物理删除")
		return
	}
	actor := auth.GetUsername(r.Context())
	ok := s.withAuditedTx(w, r, "删除题库",
		func(txCtx context.Context) error {
			if err := s.bankSvc.DeleteBank(txCtx, id); err != nil {
				writeError(w, 400, err.Error())
				return errResponded
			}
			return nil
		},
		func(txCtx context.Context) error {
			return s.auditSvc.Log(txCtx, "", "bank_delete", actor, "删除题库（题目解除分类子题库归属，进入待归类状态）")
		})
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handleListBankQuestions 列出题库内的题目（手动微调进出用，支持搜索，数据库端分页）。
func (s *Server) handleListBankQuestions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	filter := storage.QuestionFilter{BankID: id, Keyword: r.URL.Query().Get("q"), Tiers: []string{string(domain.TierWorking)}}
	s.respondPagedQuestions(w, r, filter)
}

// handleCollectBank 重新归纳：把待归类且专业匹配的题目自动归入题库。
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
