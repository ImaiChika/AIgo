package learning

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
)

func (s *Service) Import(ctx context.Context, uid, role string, req domain.LearningImportRequest) (result []domain.AuthoringExperience, replayed bool, err error) {
	guard := req.LearningExpectation
	// The owned job in the route is the authority for its source question.
	if guard.QuestionID == "" {
		guard.QuestionID = "bound-to-owned-job"
	}
	if e := validateExpectation(guard); e != nil {
		return nil, false, e
	}
	if !bounded(req.ImportKey, 1, 120) || req.ImportKey != strings.TrimSpace(req.ImportKey) || req.ExpectedRevision < 1 || len(req.Items) < 1 || len(req.Items) > 3 {
		return nil, false, storage.ErrLearningInvalid
	}
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !permission(p, domain.PermQuestionView) || !domain.LearningCapabilitiesFor(p).Save {
			return storage.ErrLearningDenied
		}
		job, e := s.Store.GetLearningJob(tx, uid, req.JobID)
		if e != nil {
			return e
		}
		if job == nil {
			return storage.ErrLearningNotFound
		}
		if req.QuestionID != "" && req.QuestionID != job.QuestionID {
			return storage.ErrLearningConflict
		}
		req.QuestionID = job.QuestionID
		fingerprint := payloadHash(req)
		if job.Status != "succeeded" || job.Revision != req.ExpectedRevision {
			return storage.ErrLearningConflict
		}
		if e = s.guardJobSource(tx, uid, job, req.LearningExpectation); e != nil {
			return e
		}
		operation, e := s.Store.GetLearningImportOperation(tx, uid, req.ImportKey)
		if e != nil {
			return e
		}
		if operation != nil {
			if operation.RequestHash != fingerprint || operation.JobID != job.ID {
				return storage.ErrLearningConflict
			}
			for _, id := range operation.ExperienceIDs {
				card, e := s.Store.GetAuthoringExperience(tx, uid, id)
				if e != nil {
					return e
				}
				if card == nil {
					return storage.ErrLearningConflict
				}
				result = append(result, *card)
			}
			replayed = true
			return nil
		}
		candidates := map[string]domain.LearningCandidate{}
		for _, candidate := range job.Candidates {
			candidates[candidate.ID] = candidate
		}
		seen := map[string]bool{}
		ids := []string{}
		for _, item := range req.Items {
			candidate, ok := candidates[item.CandidateID]
			if !ok || seen[item.CandidateID] {
				return storage.ErrLearningInvalid
			}
			seen[item.CandidateID] = true
			item.Title = strings.TrimSpace(item.Title)
			if item.Title == "" {
				item.Title = candidate.Title
			}
			if item.Scope.Kind == "" {
				item.Scope = candidate.Scope
			}
			if !bounded(item.Title, 1, 100) || (item.Status != "draft" && item.Status != "active") {
				return storage.ErrLearningInvalid
			}
			if e = validateScope(item.Scope); e != nil {
				return e
			}
			if e = validateRules(item.Rules); e != nil {
				return e
			}
			if e = preserveRuleEvidence(item.Rules, candidate.Rules); e != nil {
				return e
			}
			medical := hasMedical(item.Rules)
			if medical && item.Status == "active" && !item.ConfirmMedicalChanges {
				return fmt.Errorf("%w: 疑似医学变更需显式核对后启用", storage.ErrLearningInvalid)
			}
			card := domain.AuthoringExperience{OwnerID: uid, Kind: "authoring_experience", ScopeType: "personal", Title: item.Title, Scope: item.Scope, Rules: item.Rules, Status: item.Status, MedicalConfirmed: medical && item.ConfirmMedicalChanges, Revision: 1, Source: domain.ExperienceSource{JobID: job.ID, CandidateID: candidate.ID, QuestionID: job.QuestionID, OriginalVersion: job.OriginalVersion, CurrentVersion: job.CurrentVersion, OriginalHash: job.OriginalHash, CurrentHash: job.CurrentHash}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			existing, e := s.Store.FindExperienceBySource(tx, uid, job.ID, candidate.ID)
			if e != nil {
				return e
			}
			if existing != nil {
				if existing.Title != card.Title || existing.Status != card.Status || !contentUnchanged(*existing, card) {
					return fmt.Errorf("%w: 此候选已保存，请修订已有经验", storage.ErrLearningConflict)
				}
				card = *existing
			} else {
				card.ID, e = newID("experience-")
				if e != nil {
					return e
				}
				if e = s.Store.SaveAuthoringExperience(tx, card, 0); e != nil {
					return e
				}
			}
			result = append(result, card)
			ids = append(ids, card.ID)
		}
		if e = s.Store.SaveLearningImportOperation(tx, storage.LearningImportOperation{OwnerID: uid, ImportKey: req.ImportKey, RequestHash: fingerprint, JobID: job.ID, ExperienceIDs: ids, CreatedAt: time.Now().UTC()}); e != nil {
			return e
		}
		return s.audit(tx, uid, job.QuestionID, "learning_import", fmt.Sprintf("学习任务 %s；保存个人经验%d条", job.ID, len(ids)))
	})
	return
}

func (s *Service) ListExperiences(ctx context.Context, uid, role string, includeArchived bool) (result []domain.AuthoringExperience, err error) {
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return storage.ErrLearningDenied
		}
		var e error
		result, e = s.Store.ListAuthoringExperiences(tx, uid, includeArchived)
		return e
	})
	return
}
func (s *Service) ExperienceVersions(ctx context.Context, uid, role, id string) (result []domain.AuthoringExperience, err error) {
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return storage.ErrLearningDenied
		}
		card, e := s.Store.GetAuthoringExperience(tx, uid, id)
		if e != nil {
			return e
		}
		if card == nil {
			return storage.ErrLearningNotFound
		}
		result, e = s.Store.ListAuthoringExperienceVersions(tx, uid, id)
		return e
	})
	return
}

// SaveExperience revises an already imported personal card. Source provenance and
// ownership are immutable; the client cannot clear a medical-change flag.
func (s *Service) SaveExperience(ctx context.Context, uid, role, id string, input domain.AuthoringExperience, expected int, medicalConfirmed ...bool) (result *domain.AuthoringExperience, err error) {
	if id == "" || expected < 1 || !bounded(input.Title, 1, 100) || (input.Status != "draft" && input.Status != "active" && input.Status != "archived") {
		return nil, storage.ErrLearningInvalid
	}
	if e := validateScope(input.Scope); e != nil {
		return nil, e
	}
	if e := validateRules(input.Rules); e != nil {
		return nil, e
	}
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).Manage {
			return storage.ErrLearningDenied
		}
		prior, e := s.Store.GetAuthoringExperience(tx, uid, id)
		if e != nil {
			return e
		}
		if prior == nil {
			return storage.ErrLearningNotFound
		}
		if prior.Revision != expected {
			return storage.ErrLearningConflict
		}
		if manualKnowledgeOrigin(prior.Source.Kind) {
			if e = validateManualKnowledge(input.Title, input.Rules); e != nil {
				return e
			}
			preserveManualMedicalEvidence(input.Rules, prior.Rules)
		} else if e = preserveRuleEvidence(input.Rules, prior.Rules); e != nil {
			return e
		}
		ack := len(medicalConfirmed) > 0 && medicalConfirmed[0]
		confirmed := hasMedical(input.Rules) && (ack || prior.MedicalConfirmed && contentUnchanged(*prior, input))
		if hasMedical(input.Rules) && input.Status == "active" && !confirmed {
			return nilMedicalConfirmation()
		}
		input.ID, input.OwnerID, input.Kind, input.ScopeType = id, uid, "authoring_experience", "personal"
		input.Source, input.CreatedAt = prior.Source, prior.CreatedAt
		input.MedicalConfirmed = confirmed
		input.Revision = expected + 1
		input.UpdatedAt = time.Now().UTC()
		if e = s.Store.SaveAuthoringExperience(tx, input, expected); e != nil {
			return e
		}
		if e = s.audit(tx, uid, prior.Source.QuestionID, "learning_experience_edit", fmt.Sprintf("个人经验 %s；修订%d；状态%s", id, input.Revision, input.Status)); e != nil {
			return e
		}
		result, e = s.Store.GetAuthoringExperience(tx, uid, id)
		return e
	})
	return
}
func nilMedicalConfirmation() error {
	return fmt.Errorf("%w: 医学经验正文或范围已变化，需重新显式核对后启用", storage.ErrLearningInvalid)
}

func (s *Service) FreezeExperiences(ctx context.Context, uid, role string, points []domain.KnowledgePoint) (result *domain.PersonalExperienceSnapshot, err error) {
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !domain.LearningCapabilitiesFor(p).Use {
			return storage.ErrLearningDenied
		}
		var cards []domain.AuthoringExperience
		var e error
		matchingTotal := -1
		// The migrated manual-use grant must not resurrect revoked AI-learning
		// adoption. Filter origins in SQL before matching counts and limits.
		var allowedOrigins []string
		if !permission(p, domain.PermLearningUse) {
			allowedOrigins = []string{"manual", "legacy_personal_preference"}
		}
		if query, ok := s.Store.(storage.ExperienceQueryStore); ok {
			ids, professions := experienceQueryPoints(points)
			cards, matchingTotal, e = query.MatchAuthoringExperiences(tx, uid, ids, professions, 200, allowedOrigins...)
		} else {
			cards, e = s.Store.ListAuthoringExperiences(tx, uid, false)
		}
		if e != nil {
			return e
		}
		type match struct {
			card   domain.AuthoringExperience
			ids    []string
			rank   int
			reason string
		}
		matches := []match{}
		for _, card := range cards {
			if len(allowedOrigins) > 0 && !manualKnowledgeOrigin(card.Source.Kind) {
				continue
			}
			if card.OwnerID != uid || card.Kind != "authoring_experience" || card.ScopeType != "personal" || card.Status != "active" || hasMedical(card.Rules) && !card.MedicalConfirmed {
				continue
			}
			ids, rank, reason := matchExperience(card.Scope, points)
			if len(ids) > 0 {
				matches = append(matches, match{card, ids, rank, reason})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			// The formerly unique enabled personal preference must remain an
			// applied instruction after migration, even among newer point cards.
			leftLegacy := matches[i].card.Source.Kind == "legacy_personal_preference"
			rightLegacy := matches[j].card.Source.Kind == "legacy_personal_preference"
			if leftLegacy != rightLegacy {
				return leftLegacy
			}
			if matches[i].rank != matches[j].rank {
				return matches[i].rank > matches[j].rank
			}
			if !matches[i].card.UpdatedAt.Equal(matches[j].card.UpdatedAt) {
				return matches[i].card.UpdatedAt.After(matches[j].card.UpdatedAt)
			}
			return matches[i].card.ID < matches[j].card.ID
		})
		result = &domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: uid, CapturedAt: time.Now().UTC(), Entries: []domain.PersonalExperienceUse{}}
		used := 0
		for _, m := range matches {
			// Count the whole bounded entry, including examples and reasons. Never
			// truncate a teacher's rule into a different instruction.
			entry := domain.PersonalExperienceUse{ID: m.card.ID, Revision: m.card.Revision, Title: m.card.Title, Scope: m.card.Scope, Rules: m.card.Rules, MatchedPointIDs: m.ids, MatchReason: m.reason}
			size := domain.PersonalExperienceContentSize(entry)
			if len(result.Entries) >= domain.MaxPersonalExperienceEntries || used+size > domain.MaxPersonalExperienceContentSize {
				result.ExcludedCount++
				continue
			}
			used += size
			result.Entries = append(result.Entries, entry)
		}
		if matchingTotal >= 0 {
			result.ExcludedCount = matchingTotal - len(result.Entries)
		}
		return nil
	})
	return
}
func matchExperience(scope domain.ExperienceScope, points []domain.KnowledgePoint) (ids []string, rank int, reason string) {
	seen := map[string]bool{}
	for _, point := range points {
		if !domain.IsEligibleClinicalPoint(point) || point.ID == "" || seen[point.ID] {
			continue
		}
		matches := scope.Kind == "all"
		switch scope.Kind {
		case "point":
			for _, id := range scope.PointIDs {
				if id == point.ID {
					matches = true
				}
			}
			rank = 3
			reason = "匹配指定临床考点"
		case "profession":
			profession, system := generator.QuestionMetadataForKnowledgePoint(point, point.Subject)
			matches = strings.TrimSpace(scope.Profession) == strings.TrimSpace(point.Subject) || strings.TrimSpace(scope.Profession) == profession || strings.TrimSpace(scope.Profession) == system
			rank = 2
			reason = "匹配指定专业"
		case "all":
			rank = 1
			reason = "适用于本人全部临床题"
		}
		if matches {
			seen[point.ID] = true
			ids = append(ids, point.ID)
		}
	}
	sort.Strings(ids)
	return
}

func experienceQueryPoints(points []domain.KnowledgePoint) ([]string, []string) {
	ids, professions := []string{}, []string{}
	seenID, seenProfession := map[string]bool{}, map[string]bool{}
	for _, point := range points {
		if point.ID == "" || !domain.IsEligibleClinicalPoint(point) {
			continue
		}
		if !seenID[point.ID] {
			ids = append(ids, point.ID)
			seenID[point.ID] = true
		}
		profession, system := generator.QuestionMetadataForKnowledgePoint(point, point.Subject)
		for _, name := range []string{strings.TrimSpace(point.Subject), profession, system} {
			if name != "" && !seenProfession[name] {
				professions = append(professions, name)
				seenProfession[name] = true
			}
		}
	}
	return ids, professions
}

func candidateWarnings(items []domain.LearningCandidate) []string {
	warnings := []string{}
	seen := map[string]string{}
	fields := map[string]string{}
	for _, item := range items {
		for _, rule := range item.Rules {
			key := strings.TrimSpace(rule.Text)
			if prior, ok := seen[key]; ok && prior != item.ID {
				warnings = append(warnings, "不同候选包含相同建议，请避免重复导入")
			}
			seen[key] = item.ID
			if prior, ok := fields[rule.Field]; ok && prior != key {
				warnings = append(warnings, "同一字段存在不同建议，合并前请核对适用条件与潜在冲突")
			}
			fields[rule.Field] = key
		}
	}
	result := []string{}
	for _, warning := range warnings {
		exists := false
		for _, prior := range result {
			if warning == prior {
				exists = true
			}
		}
		if !exists {
			result = append(result, warning)
		}
	}
	return result
}
