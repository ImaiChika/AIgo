package auth

import (
	"aigo/internal/domain"
	"context"
	"fmt"
	"strings"
)

func learningCapabilityMap(permissions []string) map[string]bool {
	c := domain.LearningCapabilitiesFor(permissions)
	result := map[string]bool{}
	if c.Analyze {
		result[domain.PermLearningAnalyze] = true
	}
	if c.Save {
		result[domain.PermLearningSave] = true
	}
	if c.Use && containsString(permissions, domain.PermLearningUse) {
		result[domain.PermLearningUse] = true
	}
	knowledge := domain.PersonalKnowledgeCapabilitiesFor(permissions)
	if knowledge.View && containsString(permissions, domain.PermExperienceView) {
		result[domain.PermExperienceView] = true
	}
	if knowledge.Manage && containsString(permissions, domain.PermExperienceView) && containsString(permissions, domain.PermExperienceManage) {
		result[domain.PermExperienceManage] = true
	}
	if knowledge.Use && containsString(permissions, domain.PermExperienceView) && containsString(permissions, domain.PermExperienceUse) {
		result[domain.PermExperienceUse] = true
	}
	return result
}
func (s *Service) effectiveLearningCapabilities(ctx context.Context, roles, direct []string) (map[string]bool, error) {
	result := map[string]bool{}
	if len(roles) == 0 {
		roles = []string{""}
	}
	for _, roleID := range roles {
		permissions := append([]string{}, direct...)
		if roleID != "" {
			role, err := s.GetRole(ctx, roleID)
			if err != nil {
				return nil, err
			}
			if role == nil {
				continue
			}
			permissions = append(permissions, role.Permissions...)
		}
		for p := range learningCapabilityMap(permissions) {
			result[roleID+"\x00"+p] = true
		}
	}
	return result, nil
}
func (s *Service) checkLearningDelegation(ctx context.Context, actorID string, target *User, roles, direct, newGrants []string) error {
	var err error
	// HasPermission reads the request's current role, not the account-wide union.
	actorPermissions := []string{}
	for _, p := range []string{domain.PermLearningAnalyze, domain.PermLearningSave, domain.PermLearningUse, domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse, domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermBatchRun} {
		ok, e := s.HasPermission(ctx, actorID, p)
		if e != nil {
			return e
		}
		if ok {
			actorPermissions = append(actorPermissions, p)
		}
	}
	allowed := learningCapabilityMap(actorPermissions)
	for _, p := range newGrants {
		if (domain.IsLearningPermission(p) || domain.IsPersonalKnowledgePermission(p)) && !allowed[p] {
			return fmt.Errorf("%w: 当前身份没有对应学习有效权限，不能新增授予", ErrPermissionDenied)
		}
	}
	before := map[string]bool{}
	if target != nil {
		before, err = s.effectiveLearningCapabilities(ctx, normalizeRoleIDs(target.Role, target.Roles), target.DirectPermissions)
		if err != nil {
			return err
		}
	}
	after, err := s.effectiveLearningCapabilities(ctx, roles, direct)
	if err != nil {
		return err
	}
	for identityCapability := range after {
		_, p, _ := strings.Cut(identityCapability, "\x00")
		if !before[identityCapability] && !allowed[p] {
			return fmt.Errorf("%w: 新授权会激活当前身份不具备的学习权限", ErrPermissionDenied)
		}
	}
	return nil
}
