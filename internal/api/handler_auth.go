package api

import (
	"net/http"

	"aigo/internal/auth"
)

// handleLogin 用户登录，返回 JWT token。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}

	token, user, err := s.authSvc.Login(req.Username, req.Password)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"token": token,
		"user":  user,
	})
}

// handleMe 获取当前登录用户信息。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	user, err := s.authSvc.GetUserByID(userID)
	if err != nil || user == nil {
		writeError(w, 401, "用户不存在")
		return
	}
	writeJSON(w, 200, user)
}

// handleUpdateProfile 修改当前用户昵称。
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
	if err := s.authSvc.UpdateDisplayName(userID, req.DisplayName); err != nil {
		writeError(w, 500, "更新失败")
		return
	}
	user, _ := s.authSvc.GetUserByID(userID)
	writeJSON(w, 200, user)
}

// handleChangePassword 修改当前用户密码。
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

// handleListUsers 管理员查看所有用户。
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.authSvc.ListUsers()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"users": users, "total": len(users)})
}

// handleCreateUser 管理员创建用户。
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
	writeJSON(w, 201, user)
}
