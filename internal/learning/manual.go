package learning

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func (s *Service) CreateExperience(ctx context.Context, uid, role string, req domain.PersonalKnowledgeCreateRequest) (*domain.AuthoringExperience, error) {
	card, _, err := s.CreateExperienceWithReplay(ctx, uid, role, req)
	return card, err
}

func (s *Service) CreateExperienceWithReplay(ctx context.Context, uid, role string, req domain.PersonalKnowledgeCreateRequest) (result *domain.AuthoringExperience, replayed bool, err error) {
	if !bounded(req.CreateKey, 1, 120) || strings.TrimSpace(req.CreateKey) != req.CreateKey {
		return nil, false, storage.ErrLearningInvalid
	}
	req.Title = strings.TrimSpace(req.Title)
	if !bounded(req.Title, 1, 100) || (req.Status != "draft" && req.Status != "active") {
		return nil, false, storage.ErrLearningInvalid
	}
	if err = validateScope(req.Scope); err != nil {
		return nil, false, err
	}
	if err = validateManualKnowledge(req.Title, req.Rules); err != nil {
		return nil, false, err
	}
	medical := hasMedical(req.Rules)
	if medical && req.Status == "active" && !req.ConfirmMedicalChanges {
		return nil, false, nilMedicalConfirmation()
	}
	for i := range req.Rules {
		if req.Rules[i].Field == "answer" {
			req.Rules[i].MedicalChange = true
		}
	}
	hash := payloadHash(req)
	err = s.access(ctx, uid, role, func(tx context.Context, permissions []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(permissions).Manage {
			return storage.ErrLearningDenied
		}
		candidate := "manual:" + req.CreateKey
		existing, e := s.Store.FindExperienceBySource(tx, uid, "", candidate)
		if e != nil {
			return e
		}
		if existing != nil {
			if existing.Source.Kind != "manual" || existing.Source.OriginRequestHash != hash {
				return storage.ErrLearningConflict
			}
			result, replayed = existing, true
			return nil
		}
		id, e := newID("experience-")
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		card := domain.AuthoringExperience{ID: id, OwnerID: uid, Kind: "authoring_experience", ScopeType: "personal", Title: req.Title, Scope: req.Scope, Rules: req.Rules, Status: req.Status, Revision: 1, MedicalConfirmed: medical && req.ConfirmMedicalChanges, Source: domain.ExperienceSource{Kind: "manual", CandidateID: candidate, OriginRequestHash: hash}, CreatedAt: now, UpdatedAt: now}
		if e = s.Store.SaveAuthoringExperience(tx, card, 0); e != nil {
			return e
		}
		result, e = s.Store.GetAuthoringExperience(tx, uid, id)
		return e
	})
	return
}

func validateManualKnowledge(title string, rules []domain.LearningRule) error {
	if err := validateRules(rules); err != nil {
		return err
	}
	textSize := 0
	for _, rule := range rules {
		textSize += utf8.RuneCountInString(rule.Text)
	}
	if textSize > 2000 {
		return fmt.Errorf("%w: 手工知识正文合计最多2000字", storage.ErrLearningInvalid)
	}
	entry := domain.PersonalExperienceUse{Title: title, Rules: rules, MatchReason: "适用于本人全部临床题"}
	if domain.PersonalExperienceContentSize(entry) > domain.MaxPersonalExperienceContentSize {
		return fmt.Errorf("%w: 知识正文及前后说明过长，无法在出题时完整采用", storage.ErrLearningInvalid)
	}
	return nil
}

func manualKnowledgeOrigin(kind string) bool {
	return kind == "manual" || kind == "legacy_personal_preference"
}

// Manual cards can add/reorganize entries. A carried medical flag cannot be
// erased by changing its ID or deleting it and adding an otherwise identical row.
func preserveManualMedicalEvidence(current, prior []domain.LearningRule) {
	flagged := map[string]bool{}
	for _, r := range prior {
		if r.MedicalChange || r.Field == "answer" {
			flagged[r.ID] = true
		}
	}
	for i := range current {
		current[i].MedicalChange = current[i].MedicalChange || current[i].Field == "answer" || flagged[current[i].ID]
	}
	if len(flagged) > 0 && !hasMedical(current) {
		current[0].MedicalChange = true
	}
}
