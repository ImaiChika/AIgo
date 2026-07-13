package api

import (
	"net/http"

	"aigo/internal/auth"
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

// handleListUsers 管理员查看所有用户列表。
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.authSvc.ListUsers()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"users": users, "total": len(users)})
}

// handleCreateUser 管理员创建新用户。
// 请求：{"username": "xxx", "password": "xxx", "display_name": "xxx", "role": "teacher|expert|admin"}
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	user, err := s.authSvc.CreateUser(req.Username, req.Password, req.DisplayName, req.Role)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, user) // 201 Created
}
