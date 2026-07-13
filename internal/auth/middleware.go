package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// context key 类型，避免与其他包的 key 冲突。
type contextKey string

const (
	UserIDKey      contextKey = "user_id"      // 用户 ID
	UsernameKey    contextKey = "username"      // 用户名
	DisplayNameKey contextKey = "display_name" // 昵称
	RoleKey        contextKey = "role"         // 角色
)

// Middleware 返回一个 HTTP 中间件，用于验证 JWT token 并注入用户信息到 context。
// 适用于需要统一认证的路由组。
func Middleware(authSvc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 从 Authorization 头提取 Bearer token
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

			// 验证 token
			claims, err := authSvc.ValidateToken(token)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusUnauthorized)
				return
			}

			// 将用户信息注入 context
			ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, UsernameKey, claims.Username)
			ctx = context.WithValue(ctx, DisplayNameKey, claims.DisplayName)
			ctx = context.WithValue(ctx, RoleKey, claims.Role)

			next.ServeHTTP(w, r.WithContext(ctx))
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

// GetDisplayName 从 context 获取昵称。
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
