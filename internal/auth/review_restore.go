package auth

import (
	"aigo/internal/domain"
	"aigo/internal/review"
	"context"
	"crypto/sha256"
)

func (s *Service) AuthorizeReviewRestore(ctx context.Context, userID string) (string, bool, error) {
	user, err := s.GetUserByID(userID)
	if err != nil {
		return "", false, err
	}
	if user == nil || !user.Enabled {
		return "", false, nil
	}
	if role := GetRole(ctx); role != "" && !containsString(user.Roles, role) {
		return "", false, nil
	}
	allowed, err := s.HasPermission(ctx, userID, domain.PermReviewRestore)
	return user.Username, allowed, err
}

// ReviewRestoreSigningKey派生用途隔离的确认凭证密钥，不暴露JWT签名密钥。
func (s *Service) ReviewRestoreSigningKey() []byte {
	sum := sha256.Sum256(append([]byte("aigo-review-restore-preview-v1:"), s.jwtSecret...))
	return append([]byte(nil), sum[:]...)
}

func (s *Service) ListFinalReviewCandidates(ctx context.Context) ([]review.Candidate, error) {
	users, err := s.ListUsers()
	if err != nil {
		return nil, err
	}
	out := []review.Candidate{}
	for _, u := range users {
		if u.Enabled && containsPermission(u.Permissions, domain.PermReviewFinal) {
			out = append(out, review.Candidate{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName})
		}
	}
	return out, nil
}
