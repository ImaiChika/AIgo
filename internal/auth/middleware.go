package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type contextKey string

const (
	UserIDKey      contextKey = "user_id"
	UsernameKey    contextKey = "username"
	DisplayNameKey contextKey = "display_name"
	RoleKey        contextKey = "role"
)

// Middleware JWT 认证中间件。
func Middleware(authSvc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 从 Header 提取 token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"缺少登录凭证"}`, http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == authHeader {
				http.Error(w, `{"error":"无效的凭证格式"}`, http.StatusUnauthorized)
				return
			}

			claims, err := authSvc.ValidateToken(token)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusUnauthorized)
				return
			}

			// 注入用户信息到 context
			ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, UsernameKey, claims.Username)
			ctx = context.WithValue(ctx, DisplayNameKey, claims.DisplayName)
			ctx = context.WithValue(ctx, RoleKey, claims.Role)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequirePermission 权限检查中间件。
func RequirePermission(action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := GetRole(r.Context())
			if !CheckPermission(role, action) {
				http.Error(w, `{"error":"权限不足"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// GetUserID 从 context 获取用户 ID。
func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(UserIDKey).(string); ok {
		return v
	}
	return ""
}

// GetUsername 从 context 获取用户名。
func GetUsername(ctx context.Context) string {
	if v, ok := ctx.Value(UsernameKey).(string); ok {
		return v
	}
	return ""
}

// GetDisplayName 从 context 获取显示名。
func GetDisplayName(ctx context.Context) string {
	if v, ok := ctx.Value(DisplayNameKey).(string); ok {
		return v
	}
	return ""
}

// GetRole 从 context 获取角色。
func GetRole(ctx context.Context) string {
	if v, ok := ctx.Value(RoleKey).(string); ok {
		return v
	}
	return ""
}
