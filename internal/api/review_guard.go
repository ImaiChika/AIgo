package api

import (
	"context"
	"net/http"

	"aigo/internal/auth"
)

func (s *Server) validateReviewActor(ctx context.Context, permissions ...string) error {
	id := auth.GetUserID(ctx)
	user, err := s.authSvc.GetUserByID(id)
	if err != nil {
		return err
	}
	if user == nil || !user.Enabled {
		return auth.ErrPermissionDenied
	}
	role := auth.GetRole(ctx)
	attached := role == "" && len(user.Roles) == 0
	for _, r := range user.Roles {
		if r == role {
			attached = true
			break
		}
	}
	if !attached {
		return auth.ErrPermissionDenied
	}
	for _, permission := range permissions {
		allowed, e := s.authSvc.HasPermission(ctx, id, permission)
		if e != nil {
			return e
		}
		if allowed {
			return nil
		}
	}
	return auth.ErrPermissionDenied
}

// withAssignmentGuard把资格预检与实际写入放进和送审/审核/还原相同的数据库锁。
func (s *Server) withAssignmentGuard(handler http.HandlerFunc, permissions ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.reviewSvc == nil {
			handler(w, r)
			return
		}
		err := s.reviewSvc.WithAssignmentLock(r.Context(), func(ctx context.Context) error {
			if err := s.validateReviewActor(ctx, permissions...); err != nil {
				return err
			}
			handler(w, r.WithContext(ctx))
			return nil
		})
		if err != nil {
			if err == auth.ErrPermissionDenied {
				writeError(w, 403, "当前身份已无操作权限，请刷新")
			} else {
				writeError(w, 503, "操作暂时无法执行，请稍后重试")
			}
		}
	}
}
