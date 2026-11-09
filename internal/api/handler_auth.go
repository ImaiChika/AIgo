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
	s.logAuthenticationSuccess(r, user, clientIP)

	writeJSON(w, 200, map[string]any{
		"token": token, // 前端保存此 token，后续请求带在 Authorization 头
		"user":  user,  // 用户基本信息（不含密码）
	})
}

// logAuthenticationSuccess 登录成功也留痕：管理员界面的操作日志必须能看到
// 每个账号（含管理员自身）的登录记录。来源 IP 与失败日志同口径做哈希脱敏。
func (s *Server) logAuthenticationSuccess(r *http.Request, user *auth.User, clientIP string) {
	if s.auditSvc == nil {
		return
	}
	roleName := user.Role
	if name, ok := user.RoleNames[user.Role]; ok && name != "" {
		roleName = name
	}
	detail := fmt.Sprintf("account=%s role=%s source=%s", user.Username, roleName, auth.AuditSubject("ip", clientIP))
	_ = s.auditSvc.Log(r.Context(), "", "auth_login", user.Username, detail)
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
// 待办计数为账号级口径：任务归属账号本身，与当前工作身份无关；仪表盘据此在
// 任意身份下显示跨身份待办提醒（前端再按当前身份权限决定直达或引导切换）。
// 账号级权限判断使用不带当前角色上下文的查询（全身份并集口径）。
func (s *Server) handleMySummary(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("view") == "authoring" {
		s.handleAuthoringSummary(w, r)
		return
	}
	userID := auth.GetUserID(r.Context())
	// "我已出题"只统计本人实际可见过的题（三层题库）：
	// AI 检查未完成/耗尽的暂存题从未进入题库，不计入，避免与分层之和矛盾。
	_, generated, err := s.questionStore.SearchQuestions(r.Context(), storage.QuestionFilter{
		OwnerID: userID,
		Tiers:   []string{string(domain.TierFormal), string(domain.TierWorking), string(domain.TierEliminated)},
	}, 1, 1)
	if err != nil {
		writeMySummaryQueryError(w, r, err, "读取个人数据失败")
		return
	}
	_, newQuestions, err := s.questionStore.SearchQuestions(r.Context(), storage.QuestionFilter{
		OwnerID: userID, Status: string(domain.StatusAIReviewed),
		Tiers: []string{string(domain.TierWorking)}, NoActiveReviewTask: true, LibraryStates: []string{string(domain.LibraryUnfiled)},
	}, 1, 1)
	if err != nil {
		writeMySummaryQueryError(w, r, err, "读取待提交新题失败")
		return
	}
	// C3 个人题库归属计数：普通库（已确认当前版本）/待整理/删除库。
	libraryCounts := map[string]int{}
	for _, state := range []domain.LibraryState{domain.LibraryActive, domain.LibraryUnfiled, domain.LibraryDeleted} {
		_, total, err := s.questionStore.SearchQuestions(r.Context(), storage.QuestionFilter{
			OwnerID: userID, LibraryStates: []string{string(state)},
			Tiers: []string{string(domain.TierFormal), string(domain.TierWorking), string(domain.TierEliminated)},
		}, 1, 1)
		if err != nil {
			writeMySummaryQueryError(w, r, err, "读取题库归属数据失败")
			return
		}
		libraryCounts[string(state)] = total
	}
	reviewed := 0
	if s.reviewSvc != nil {
		reviewed, err = s.reviewSvc.CountReviewedBy(r.Context(), userID)
		if err != nil {
			writeMySummaryQueryError(w, r, err, "读取已审核统计失败")
			return
		}
	}

	// 账号级待办计数（跨身份提醒）：账号任一身份具备对应权限才可能被分配
	// 该类任务（收权守卫保证在途任务持有者不会失去权限），因此按全身份并集
	// 权限门控查询；无对应权限时计数恒为 0，不发查询。
	// Mask identity only for this account-wide statistics query, while keeping
	// the request deadline/cancellation. HTTP authorization still uses r.Context().
	accountCtx := context.WithValue(context.WithValue(r.Context(), auth.UserIDKey, ""), auth.RoleKey, "")
	hasReview, revErr := s.authSvc.HasPermission(accountCtx, userID, domain.PermReviewDo)
	if revErr != nil {
		writeMySummaryQueryError(w, r, revErr, "读取待办数据失败")
		return
	}
	hasFinal, finalErr := s.authSvc.HasPermission(accountCtx, userID, domain.PermReviewFinal)
	if finalErr != nil {
		writeMySummaryQueryError(w, r, finalErr, "读取待办数据失败")
		return
	}
	hasShareReview, shareErr := s.authSvc.HasPermission(accountCtx, userID, domain.PermQuestionShareReview)
	if shareErr != nil {
		writeMySummaryQueryError(w, r, shareErr, "读取待办数据失败")
		return
	}
	isAdminAccount, adminErr := s.authSvc.HasPermission(accountCtx, userID, domain.PermUserManage)
	if adminErr != nil {
		writeMySummaryQueryError(w, r, adminErr, "读取待办数据失败")
		return
	}
	pendingReview := 0
	if s.reviewSvc != nil && hasReview {
		if tasks, taskErr := s.reviewSvc.MyTasks(accountCtx, userID, false); taskErr == nil {
			pendingReview = len(tasks)
		}
	}
	pendingDecision := 0
	if s.reviewSvc != nil && hasFinal {
		if tasks, taskErr := s.reviewSvc.MyDecisionsForViewer(accountCtx, userID, isAdminAccount); taskErr == nil {
			pendingDecision = len(tasks)
		}
	}
	pendingRevision := 0
	if s.reviewSvc != nil {
		actor := auth.GetUsername(r.Context())
		if actor == "" {
			actor = userID
		}
		if items, itemErr := s.reviewSvc.MyRevisions(accountCtx, actor); itemErr == nil {
			pendingRevision = len(items)
		}
	}
	pendingShareApproval := 0
	if s.shareStore != nil && hasShareReview {
		if shares, shareListErr := s.shareStore.ListQuestionShares(r.Context(), string(domain.QuestionSharePending), ""); shareListErr == nil {
			pendingShareApproval = len(shares)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"generated_count":              generated,
		"reviewed_count":               reviewed,
		"new_questions_count":          newQuestions,
		"pending_review_count":         pendingReview,
		"pending_decision_count":       pendingDecision,
		"pending_revision_count":       pendingRevision,
		"pending_share_approval_count": pendingShareApproval,
		// C3 个人题库归属计数：active=普通题库，unfiled=待整理，deleted=删除题库。
		"library_active_count":  libraryCounts[string(domain.LibraryActive)],
		"library_unfiled_count": libraryCounts[string(domain.LibraryUnfiled)],
		"library_deleted_count": libraryCounts[string(domain.LibraryDeleted)],
	})
}

// The isolated authoring UI needs no legacy review statistics or question bodies.
// Reuse the exact existing filters and count-only storage API; never cache grants.
func (s *Server) handleAuthoringSummary(w http.ResponseWriter, r *http.Request) {
	uid := auth.GetUserID(r.Context())
	filters := []struct {
		key    string
		filter storage.QuestionFilter
	}{
		{"generated_count", storage.QuestionFilter{OwnerID: uid, Tiers: []string{"formal", "working", "eliminated"}}},
		{"new_questions_count", storage.QuestionFilter{OwnerID: uid, Status: string(domain.StatusAIReviewed), Tiers: []string{"working"}, NoActiveReviewTask: true, LibraryStates: []string{string(domain.LibraryUnfiled)}}},
	}
	for _, state := range []domain.LibraryState{domain.LibraryActive, domain.LibraryUnfiled, domain.LibraryDeleted} {
		filters = append(filters, struct {
			key    string
			filter storage.QuestionFilter
		}{"library_" + string(state) + "_count", storage.QuestionFilter{OwnerID: uid, LibraryStates: []string{string(state)}, Tiers: []string{"formal", "working", "eliminated"}}})
	}
	result := map[string]int{}
	for _, item := range filters {
		counts, err := s.questionStore.CountQuestionsByStatus(r.Context(), item.filter)
		if err != nil {
			writeMySummaryQueryError(w, r, err, "读取个人命题计数失败")
			return
		}
		for _, count := range counts {
			result[item.key] += count
		}
		if _, ok := result[item.key]; !ok {
			result[item.key] = 0
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func writeMySummaryQueryError(w http.ResponseWriter, r *http.Request, err error, message string) {
	cancelled := errors.Is(err, context.Canceled)
	// lib/pq returns SQLSTATE 57014 when a query already running in PostgreSQL
	// is cancelled. It does not wrap context.Canceled. Still require the HTTP
	// request itself to be cancelled; live statement timeouts remain failures.
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "57014" {
		cancelled = true
	}
	if errors.Is(r.Context().Err(), context.Canceled) && cancelled {
		writeError(w, 499, "请求已取消")
		return
	}
	writeError(w, http.StatusInternalServerError, message)
}

// handleSwitchRole 切换当前账号的工作身份。角色集合由管理员维护，服务端
// 重新签发 JWT，后续接口的权限只取所选身份及用户直接授权。
//
// 已挂载身份之间的切换永远自由：任务归属账号本身，与当前查看身份无关，
// 随时可以切回处理；切换只改变可见页面，不改变任务归属与数据。
// 「升级放行 / 收权查在途任务」的准入语义属于 admin 权限分配界面
// （handleUpdateUser 的占用守卫），不属于身份切换。
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
	if s.auditSvc != nil && role != auth.GetRole(r.Context()) {
		_ = s.auditSvc.Log(r.Context(), "", "auth_switch_role", auth.GetUsername(r.Context()),
			fmt.Sprintf("切换工作身份 → %s", role))
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
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "auth_update_profile", auth.GetUsername(r.Context()),
			fmt.Sprintf("更新个人资料：昵称「%s」", req.DisplayName))
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
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "auth_change_password", auth.GetUsername(r.Context()), "修改了自己的登录密码")
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
	// 资格预检先行：越权操作与受保护账号必须先得到 403 拒绝，不能落入
	// 下方占用守卫的 409——否则无权操作者会看到目标账号的在途任务细节，
	// 且误导其"处理完任务即可操作"。与 UpdateUserAsWithRoles 同口径。
	assignedRoles := append([]string(nil), req.Roles...)
	if req.Role != "" && !slices.Contains(assignedRoles, req.Role) {
		assignedRoles = append([]string{req.Role}, assignedRoles...)
	}
	if err := s.authSvc.ValidateUserMutationAs(r.Context(), auth.GetUserID(r.Context()), userID, req.Role, assignedRoles, req.Permissions, req.BankIDs); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, auth.ErrSuperAdminOnly) ||
			errors.Is(err, auth.ErrProtectedAccount) || errors.Is(err, auth.ErrSuperAdminRoleNotAssignable) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
	// cleanupExpertAfterUpdate 为 true 时（本次更新确实移除了审题权限或停用了账号），
	// 在更新成功后同步清理专家库镜像条目；重新授权/启用时 syncUserToExperts 会自动补回。
	cleanupExpertAfterUpdate := false
	if s.reviewSvc != nil {
		current, currentErr := s.authSvc.GetUserByID(userID)
		roles := assignedRoles
		futurePermissions, permissionErr := s.authSvc.PermissionsForAssignments(r.Context(), roles, req.Permissions)
		futureEnabled := current != nil && current.Enabled
		if req.Enabled != nil {
			futureEnabled = *req.Enabled
		}
		// 多身份模板口径：有效权限 = 全部已挂模板权限 ∪ 直接授权权限。
		// 只有所有身份都不再提供某权限时才算"失去"，移除其中一个模板
		// 但其他模板仍覆盖该权限时不触发守卫（下方 losesXxx 均按并集判断）。
		losesReviewRole := current != nil && slices.Contains(current.Permissions, domain.PermReviewDo) && !slices.Contains(futurePermissions, domain.PermReviewDo)
		losesFinalRole := current != nil && slices.Contains(current.Permissions, domain.PermReviewFinal) && !slices.Contains(futurePermissions, domain.PermReviewFinal)
		// 编辑权限对称守卫：教师名下审核流程占用中的题目（退修/审核/待决断）
		// 后续都可能要求本人修改，收走 question:edit 或停用会让题目无人可改。
		losesEditRole := current != nil && slices.Contains(current.Permissions, domain.PermQuestionEdit) && !slices.Contains(futurePermissions, domain.PermQuestionEdit)
		losesSubmitRole := current != nil && slices.Contains(current.Permissions, domain.PermReviewSubmit) && !slices.Contains(futurePermissions, domain.PermReviewSubmit)
		if currentErr == nil && permissionErr == nil && current != nil && (!futureEnabled || losesReviewRole || losesFinalRole || losesEditRole || losesSubmitRole) {
			var blockers []string
			// 阻断项按「实际失去的权限」取对应分组：丢审题权限才看轮审人/进行中
			// 任务引用，丢决断权限才看把关人/待决断引用，避免丢 A 权限被 B 占用误拦。
			references, referenceErr := s.reviewSvc.AssignmentReferencesByPermission(r.Context(), userID)
			if referenceErr != nil {
				writeError(w, http.StatusInternalServerError, "检查审核任务引用失败")
				return
			}
			if losesReviewRole || !futureEnabled {
				blockers = append(blockers, references.ActiveReviewDo...)
			}
			if losesFinalRole || !futureEnabled {
				blockers = append(blockers, references.ActiveFinal...)
			}
			if losesEditRole || losesSubmitRole || !futureEnabled {
				// 属主侧互斥占用：退回修改中 / 审核中 / 待最终决断的题目。
				pendings, pendErr := s.reviewSvc.OwnerInFlightQuestions(r.Context(), userID)
				if pendErr != nil {
					writeError(w, http.StatusInternalServerError, "检查审核流程占用题目失败")
					return
				}
				blockers = append(blockers, pendings...)
			}
			if !futureEnabled {
				// 停用账号专属占用：自主任务（命题/批量/分享）在停用后无法导入或处理。
				autonomous, autoErr := s.authSvc.InflightAutonomousWork(r.Context(), userID)
				if autoErr != nil {
					writeError(w, http.StatusInternalServerError, "检查进行中任务失败")
					return
				}
				blockers = append(blockers, autonomous...)
			}
			if len(blockers) > 0 {
				writeError(w, http.StatusConflict, "该账号仍有进行中的事务占用，停用或收回权限后将无法处理，请先完成或移交："+strings.Join(blockers, "；"))
				return
			}
		}
		if current != nil && permissionErr == nil {
			cleanupExpertAfterUpdate = (slices.Contains(current.Permissions, domain.PermReviewDo) && !slices.Contains(futurePermissions, domain.PermReviewDo)) ||
				(current.Enabled && !futureEnabled)
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
	if cleanupExpertAfterUpdate && s.reviewSvc != nil {
		// 专家库镜像同步清理：失去审题权限或停用后条目不再有效（重新授权/启用自动补回）。
		if err := s.reviewSvc.DeleteExpert(r.Context(), user.ID); err == nil {
			s.auditSvc.LogExpert(r.Context(), user.ID, auth.GetUsername(r.Context()), "delete", "账号失去审题权限或被停用，自动移出专家库")
		}
	}
	s.syncUserToExperts(r, user)
	writeJSON(w, 200, user)
}

// handleDeleteUser 删除普通用户。超级管理员账号和当前操作账号由服务层保护。
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	// 资格预检先行（与 handleUpdateUser 同理）：越权/受保护账号先得 403，
	// 不让业务占用的 409 抢先返回给无权操作者。
	if err := s.authSvc.ValidateDeleteUserAs(r.Context(), auth.GetUserID(r.Context()), userID); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, auth.ErrSuperAdminOnly) || errors.Is(err, auth.ErrProtectedAccount) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
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
		// 删除账号同样会使其名下审核流程占用中的题目永久无人可改，按同一守卫拦截。
		pendings, pendErr := s.reviewSvc.OwnerInFlightQuestions(r.Context(), userID)
		if pendErr != nil {
			writeError(w, http.StatusInternalServerError, "检查审核流程占用题目失败")
			return
		}
		if len(pendings) > 0 {
			writeError(w, http.StatusConflict, fmt.Sprintf("该用户名下仍有 %d 道审核流程占用中的题目（退修/审核/待决断），删除后题目将永久无人可改；请先处理：%s",
				len(pendings), strings.Join(pendings, "；")))
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
	if s.reviewSvc != nil {
		// 专家库镜像随账号删除同步清理，避免留下孤儿条目。
		if err := s.reviewSvc.DeleteExpert(r.Context(), deleted.ID); err == nil {
			s.auditSvc.LogExpert(r.Context(), deleted.ID, auth.GetUsername(r.Context()), "delete", "账号已删除，自动移出专家库")
		}
	}
	s.auditSvc.LogUser(r.Context(), deleted.Username, auth.GetUsername(r.Context()), "delete")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": deleted.ID})
}

// syncUserToExperts 有审题权限的启用用户自动在专家库建立记录（ID 一致）。
// 停用账号不再补建条目：专家库只镜像"当前可参与审题"的账号。
func (s *Server) syncUserToExperts(r *http.Request, user *auth.User) {
	if s.reviewSvc == nil || !user.Enabled {
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

// handleResetUserPassword 管理员重置用户密码。
// 请求：{"new_password": ""}（空=生成随机初始口令）；响应只回一次新口令。
// 被重置账号的既有 token 维持 24 小时自然过期。
func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	newPassword, target, err := s.authSvc.ResetUserPasswordAs(r.Context(), auth.GetUserID(r.Context()), userID, req.NewPassword)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrPermissionDenied) {
			status = http.StatusForbidden
		} else if errors.Is(err, auth.ErrProtectedAccount) || errors.Is(err, auth.ErrSuperAdminOnly) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
	s.auditSvc.LogUser(r.Context(), target.Username, auth.GetUsername(r.Context()), "reset_password")
	writeJSON(w, 200, map[string]any{"username": target.Username, "new_password": newPassword})
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

	if !existing.IsBuiltin && s.reviewSvc != nil {
		users, userErr := s.authSvc.ListUsers()
		if userErr != nil {
			writeError(w, 500, "检查角色持有用户失败")
			return
		}
		var blockers []string
		for _, u := range users {
			if !slices.Contains(u.Roles, id) {
				continue
			}
			remaining := []string{}
			for _, role := range u.Roles {
				if role != id {
					remaining = append(remaining, role)
				}
			}
			direct := append(append([]string(nil), u.DirectPermissions...), req.Permissions...)
			future, e := s.authSvc.PermissionsForAssignments(r.Context(), remaining, direct)
			if e != nil {
				writeError(w, 500, "计算角色调整后权限失败")
				return
			}
			losesReview := slices.Contains(u.Permissions, domain.PermReviewDo) && !slices.Contains(future, domain.PermReviewDo)
			losesFinal := slices.Contains(u.Permissions, domain.PermReviewFinal) && !slices.Contains(future, domain.PermReviewFinal)
			losesEdit := slices.Contains(u.Permissions, domain.PermQuestionEdit) && !slices.Contains(future, domain.PermQuestionEdit)
			losesSubmit := slices.Contains(u.Permissions, domain.PermReviewSubmit) && !slices.Contains(future, domain.PermReviewSubmit)
			if losesReview || losesFinal {
				refs, e := s.reviewSvc.AssignmentReferencesByPermission(r.Context(), u.ID)
				if e != nil {
					writeError(w, 500, "检查审核任务引用失败")
					return
				}
				if losesReview {
					blockers = append(blockers, refs.ActiveReviewDo...)
				}
				if losesFinal {
					blockers = append(blockers, refs.ActiveFinal...)
				}
			}
			if losesEdit || losesSubmit {
				refs, e := s.reviewSvc.OwnerInFlightQuestions(r.Context(), u.ID)
				if e != nil {
					writeError(w, 500, "检查出题人任务失败")
					return
				}
				blockers = append(blockers, refs...)
			}
		}
		if len(blockers) > 0 {
			writeError(w, 409, "该角色仍被在途任务依赖，请先处理："+strings.Join(blockers, "；"))
			return
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
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "bank_collect", auth.GetUsername(r.Context()),
			fmt.Sprintf("题库「%s」重新归纳，归入 %d 道题", bank.Name, count))
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "collected": count})
}
