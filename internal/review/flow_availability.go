package review

import (
	"aigo/internal/domain"
	"context"
	"fmt"
)

type finalCandidateResolver interface {
	ListFinalReviewCandidates(context.Context) ([]Candidate, error)
}

// FlowAvailability 在接受任务前检查全部轮次，避免放松模板引用守卫后卡在后续轮次。
func (s *Service) FlowAvailability(ctx context.Context, flow domain.ReviewFlowConfig) error {
	if !domain.ValidReviewCompletionMode(flow.CompletionMode) {
		return fmt.Errorf("%w: 审核完成方式无效", domain.ErrReviewNotSubmittable)
	}
	if flow.Archived {
		return fmt.Errorf("%w: 流程已归档，请选择其他有效流程", domain.ErrReviewNotSubmittable)
	}
	if len(flow.Rounds) == 0 {
		return fmt.Errorf("%w: 流程没有有效轮次", domain.ErrReviewNotSubmittable)
	}
	if s.users == nil {
		return fmt.Errorf("%w: 未配置审核账号解析器", domain.ErrReviewNotSubmittable)
	}
	candidates, err := s.users.ListReviewCandidates(ctx)
	if err != nil {
		return err
	}
	valid := map[string]bool{}
	for _, c := range candidates {
		valid[c.ID] = true
	}
	for _, round := range flow.Rounds {
		if len(round.ExpertIDs) == 0 {
			return fmt.Errorf("%w: 第%d轮未指定审核人", domain.ErrReviewNotSubmittable, round.RoundNumber)
		}
		for _, id := range round.ExpertIDs {
			if !valid[id] {
				return fmt.Errorf("%w: 第%d轮存在已停用、被删除或不再拥有审题权限的账号", domain.ErrReviewNotSubmittable, round.RoundNumber)
			}
		}
		if _, err := resolvedRequiredCount(round, round.ExpertIDs, flow.VoteRule); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrReviewNotSubmittable, err)
		}
	}
	if domain.ReviewCompletionMode(flow.CompletionMode) == domain.ReviewCompletionExternalFinal && len(flow.FinalReviewerIDs) > 0 {
		resolver, ok := s.users.(finalCandidateResolver)
		if !ok {
			return fmt.Errorf("%w: 无法核验最终把关资格", domain.ErrReviewNotSubmittable)
		}
		finals, err := resolver.ListFinalReviewCandidates(ctx)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, c := range finals {
			allowed[c.ID] = true
		}
		for _, id := range flow.FinalReviewerIDs {
			if !allowed[id] {
				return fmt.Errorf("%w: 最终把关账号已停用、被删除或不再拥有最终决断权限", domain.ErrReviewNotSubmittable)
			}
		}
	}
	return nil
}
