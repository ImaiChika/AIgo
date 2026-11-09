package learning

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"fmt"
	"strings"
)

// Conversion is deterministic and free. Keep the source locked until the private
// cards and receipt commit; never accept an unverified job/source from the client.
func (s *Service) WithKnowledgeCandidates(ctx context.Context, uid, role string, req domain.LearningImportRequest, fn func(context.Context, []domain.KnowledgeCard, []*domain.ExperienceSource) error) error {
	if !bounded(req.ImportKey, 1, 120) || req.ExpectedRevision < 1 || len(req.Items) < 1 || len(req.Items) > 3 {
		return storage.ErrLearningInvalid
	}
	guard := req.LearningExpectation
	if guard.QuestionID == "" {
		guard.QuestionID = "bound-to-owned-job"
	}
	if e := validateExpectation(guard); e != nil {
		return e
	}
	return s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !domain.LearningCapabilitiesFor(p).Save {
			return storage.ErrLearningDenied
		}
		job, e := s.Store.GetLearningJob(tx, uid, req.JobID)
		if e != nil {
			return e
		}
		if job == nil {
			return storage.ErrLearningNotFound
		}
		if req.QuestionID != "" && job.QuestionID != req.QuestionID || job.Status != "succeeded" || job.Revision != req.ExpectedRevision {
			return storage.ErrLearningConflict
		}
		req.QuestionID = job.QuestionID
		if e = s.guardJobSource(tx, uid, job, req.LearningExpectation); e != nil {
			return e
		}
		if job.Source == nil || job.Source.Current == nil {
			return storage.ErrLearningConflict
		}
		byID := map[string]domain.LearningCandidate{}
		for _, c := range job.Candidates {
			byID[c.ID] = c
		}
		seen := map[string]bool{}
		cards := []domain.KnowledgeCard{}
		origins := []*domain.ExperienceSource{}
		for _, item := range req.Items {
			candidate, ok := byID[item.CandidateID]
			if !ok || seen[item.CandidateID] {
				return storage.ErrLearningInvalid
			}
			seen[item.CandidateID] = true
			if !bounded(item.Title, 1, 100) || item.Status != "draft" && item.Status != "active" {
				return storage.ErrLearningInvalid
			}
			if e = validateRules(item.Rules); e != nil {
				return e
			}
			if e = preserveRuleEvidence(item.Rules, candidate.Rules); e != nil {
				return e
			}
			if item.Status == "active" && hasMedical(item.Rules) && !item.ConfirmMedicalChanges {
				return fmt.Errorf("%w: 医学变化须人工核对", storage.ErrLearningInvalid)
			}
			lines := []string{}
			for _, rule := range item.Rules {
				lines = append(lines, rule.Text)
			}
			content := domain.KnowledgeContent{CustomText: strings.Join(lines, "\n")}
			if item.AsExpression {
				content.Expression = &domain.ExpressionRequirements{Instructions: content.CustomText}
				content.CustomText = ""
			}
			cards = append(cards, domain.KnowledgeCard{Kind: "custom", Title: strings.TrimSpace(item.Title), Status: item.Status, KnowledgePoints: job.Source.Current.KnowledgePoints, Source: "教师整理的题目修订经验（非医学认证）", Content: content})
			origins = append(origins, &domain.ExperienceSource{Kind: "learning", Evidence: item.Rules, JobID: job.ID, CandidateID: item.CandidateID, QuestionID: job.QuestionID, OriginalVersion: job.OriginalVersion, CurrentVersion: job.CurrentVersion, OriginalHash: job.OriginalHash, CurrentHash: job.CurrentHash})
		}
		return fn(tx, cards, origins)
	})
}
