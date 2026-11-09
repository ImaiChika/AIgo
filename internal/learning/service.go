// Package learning implements optional, owner-scoped analysis. It never writes
// questions, creates first-check tasks, or publishes shared medical knowledge.
package learning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
)

type Service struct {
	Store        storage.LearningStore
	Client       llm.Client
	Limiter      *storage.ConcurrencyLimiter
	PollInterval time.Duration
	// Budget and Lease are test seams. Production uses each job's frozen policy.
	Budget     time.Duration
	Lease      time.Duration
	startOnce  sync.Once
	workerDone chan struct{}
}

func NewService(store storage.LearningStore, client llm.Client) *Service {
	return &Service{Store: store, Client: client, Limiter: storage.NewConcurrencyLimiter(1), workerDone: make(chan struct{})}
}

func permission(p []string, value string) bool {
	for _, item := range p {
		if item == value {
			return true
		}
	}
	return false
}
func readJobs(p []string) bool {
	c := domain.LearningCapabilitiesFor(p)
	return permission(p, domain.PermQuestionView) && (c.Analyze || c.Save || permission(p, domain.PermLearningUse) && c.Use)
}
func (s *Service) access(ctx context.Context, uid, role string, fn func(context.Context, []string) error) error {
	if s == nil || s.Store == nil {
		return storage.ErrLearningUnavailable
	}
	if strings.TrimSpace(uid) == "" {
		return storage.ErrLearningDenied
	}
	return s.Store.WithLearningAccess(ctx, uid, role, fn)
}

func validateExpectation(e domain.LearningExpectation) error {
	if !bounded(e.QuestionID, 1, 160) || e.ExpectedVersion < 1 || e.ExpectedLibraryRevision == nil || e.ExpectedReviewRevision == nil || *e.ExpectedLibraryRevision < 1 || *e.ExpectedReviewRevision < 0 {
		return fmt.Errorf("%w: 需要源题及完整三轴版本信息", storage.ErrLearningInvalid)
	}
	return nil
}
func matchesExpectation(source *domain.LearningSource, e domain.LearningExpectation) bool {
	return source != nil && source.Current != nil && source.Current.ID == e.QuestionID && source.Current.Version == e.ExpectedVersion && source.Current.LibraryRevision == *e.ExpectedLibraryRevision && source.Current.ReviewRevision == *e.ExpectedReviewRevision
}
func sourceReason(source *domain.LearningSource) string {
	if source == nil || source.Current == nil {
		return "源题不存在或不可访问"
	}
	q := source.Current
	if q.LibraryState == domain.LibraryDeleted {
		return "删除题目不能新建分析或导入经验"
	}
	if source.Original == nil || source.Original.Version != 1 {
		return "缺少不可变原始v1，不能比较"
	}
	if !source.InitialCheckPassed {
		return "缺少初始AI首检通过依据"
	}
	if source.ActiveReview || q.Status == domain.StatusReviewing || q.Status == domain.StatusConflict {
		return "题目正在可选审核中，请先结束流程"
	}
	if q.Status != domain.StatusAIReviewed && q.Status != domain.StatusPublished {
		return "题目当前阶段不能分析或导入经验"
	}
	if source.ShareStatus == domain.QuestionSharePending {
		return "题目正在分享审批中"
	}
	if source.ShareStatus == domain.QuestionShareApproved && !source.SharedSnapshotAvailable {
		return "无法核验共享固定版本"
	}
	changes, _, _ := CompareContent(*source.Original, *q)
	if q.Version == 1 || len(changes) == 0 {
		return "当前保存版与原始版没有内容差异"
	}
	return ""
}
func expectationFor(q *domain.A2Question) domain.LearningExpectation {
	if q == nil {
		return domain.LearningExpectation{}
	}
	library, review := q.LibraryRevision, q.ReviewRevision
	return domain.LearningExpectation{QuestionID: q.ID, ExpectedVersion: q.Version, ExpectedLibraryRevision: &library, ExpectedReviewRevision: &review}
}
func comparison(source *domain.LearningSource, canAnalyze bool) *domain.LearningComparison {
	result := &domain.LearningComparison{Changes: []domain.LearningFieldChange{}}
	if source != nil {
		result.Original, result.Current = source.Original, source.Current
		result.Expectation = expectationFor(source.Current)
	}
	if result.Original != nil && result.Current != nil {
		result.Changes, result.OriginalHash, result.CurrentHash = CompareContent(*result.Original, *result.Current)
	}
	result.BlockedReason = sourceReason(source)
	if result.BlockedReason == "" && !canAnalyze {
		result.BlockedReason = "当前身份无AI经验分析权限"
	}
	result.CanAnalyze = canAnalyze && result.BlockedReason == ""
	return result
}

func (s *Service) Compare(ctx context.Context, uid, role, questionID string) (result *domain.LearningComparison, err error) {
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !permission(p, domain.PermQuestionView) {
			return storage.ErrLearningDenied
		}
		source, e := s.Store.LoadLearningSource(tx, uid, questionID)
		if e != nil {
			return e
		}
		if source == nil || source.Current == nil || source.Current.OwnerID != uid {
			return storage.ErrLearningDenied
		}
		// Comparison is another question-content reader. A blocked Analyze flag
		// must never disclose a draft which the ordinary detail endpoint hides.
		if domain.IsStagingStatus(source.Current.Status) {
			return storage.ErrLearningNotFound
		}
		result = comparison(source, domain.LearningCapabilitiesFor(p).Analyze)
		return nil
	})
	return
}

func (s *Service) Submit(ctx context.Context, uid, role string, req domain.LearningSubmitRequest) (job *domain.LearningJob, replayed bool, err error) {
	if e := validateExpectation(req.LearningExpectation); e != nil {
		return nil, false, e
	}
	req.Note = strings.TrimSpace(req.Note)
	if !bounded(req.RequestKey, 1, 120) || req.RequestKey != strings.TrimSpace(req.RequestKey) || !bounded(req.Note, 0, 1000) {
		return nil, false, storage.ErrLearningInvalid
	}
	fingerprint := payloadHash(req)
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !domain.LearningCapabilitiesFor(p).Analyze {
			return storage.ErrLearningDenied
		}
		existing, e := s.Store.FindLearningJobByKey(tx, uid, req.RequestKey)
		if e != nil {
			return e
		}
		if existing != nil {
			if existing.RequestHash != fingerprint {
				return storage.ErrLearningConflict
			}
			job = existing
			replayed = true
			return s.decorateJob(tx, uid, p, job)
		}
		if s.Client == nil {
			return storage.ErrLearningUnavailable
		}
		source, e := s.Store.LoadLearningSource(tx, uid, req.QuestionID)
		if e != nil {
			return e
		}
		if source == nil || source.Current == nil || source.Current.OwnerID != uid {
			return storage.ErrLearningDenied
		}
		if !matchesExpectation(source, req.LearningExpectation) {
			return storage.ErrLearningConflict
		}
		if reason := sourceReason(source); reason != "" {
			return fmt.Errorf("%w: %s", storage.ErrLearningConflict, reason)
		}
		id, e := newID("learning-")
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		changes, first, last := CompareContent(*source.Original, *source.Current)
		job = &domain.LearningJob{ID: id, OwnerID: uid, RoleID: role, RequestKey: req.RequestKey, RequestHash: fingerprint, QuestionID: req.QuestionID, OriginalVersion: 1, CurrentVersion: source.Current.Version, OriginalHash: first, CurrentHash: last, LibraryRevision: source.Current.LibraryRevision, ReviewRevision: source.Current.ReviewRevision, ProtocolVersion: domain.LearningProtocolVersion, Note: req.Note, Source: source, Changes: changes, Status: "pending", Revision: 1, MaxAttempts: 2, Candidates: []domain.LearningCandidate{}, CreatedAt: now, UpdatedAt: now}
		job.ExecutionBudgetSeconds = int(domain.LearningExecutionBudget / time.Second)
		job.AttemptTimeoutSeconds = int(domain.LearningAttemptTimeout / time.Second)
		temperature := domain.DefaultLearningTemperature
		if configs, ok := s.Store.(storage.LearningProviderConfigStore); ok {
			config, configErr := configs.GetLearningProviderConfig(tx)
			if configErr != nil {
				return fmt.Errorf("读取总结配置失败: %w", storage.ErrLearningUnavailable)
			}
			if config != nil {
				temperature = config.Temperature
			}
		}
		job.SamplingPolicy = domain.LearningConfiguredSampling
		job.SamplingTemperature = &temperature
		job.PromptPolicy = domain.LearningScopedReflectionPrompt
		job.OutputPolicy = domain.LearningProgramOutput
		if e = s.Store.CreateLearningJob(tx, *job); e != nil {
			return e
		}
		if e = s.audit(tx, uid, req.QuestionID, "learning_submit", fmt.Sprintf("学习任务 %s；冻结v1与v%d", id, job.CurrentVersion)); e != nil {
			return e
		}
		return s.decorateJob(tx, uid, p, job)
	})
	return
}

func (s *Service) GetJob(ctx context.Context, uid, role, id string) (result *domain.LearningJob, err error) {
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !readJobs(p) {
			return storage.ErrLearningDenied
		}
		var e error
		result, e = s.Store.GetLearningJob(tx, uid, id)
		if e != nil {
			return e
		}
		if result == nil {
			return storage.ErrLearningNotFound
		}
		return s.decorateJob(tx, uid, p, result)
	})
	return
}
func (s *Service) ListJobs(ctx context.Context, uid, role, questionID string, limit int) (result []domain.LearningJob, err error) {
	err = s.access(ctx, uid, role, func(tx context.Context, p []string) error {
		if !readJobs(p) {
			return storage.ErrLearningDenied
		}
		var e error
		result, e = s.Store.ListLearningJobs(tx, uid, questionID, limit)
		if e != nil {
			return e
		}
		for i := range result {
			if e = s.decorateJob(tx, uid, p, &result[i]); e != nil {
				return e
			}
		}
		return nil
	})
	return
}
func jobMatchesSource(job *domain.LearningJob, source *domain.LearningSource) bool {
	if source == nil || source.Current == nil || source.Original == nil {
		return false
	}
	_, first, last := CompareContent(*source.Original, *source.Current)
	return job.QuestionID == source.Current.ID && job.CurrentVersion == source.Current.Version && job.OriginalHash == first && job.CurrentHash == last
}
func (s *Service) decorateJob(ctx context.Context, uid string, p []string, job *domain.LearningJob) error {
	job.ImportedExperiences = map[string]string{}
	for _, candidate := range job.Candidates {
		imported, err := s.Store.FindExperienceBySource(ctx, uid, job.ID, candidate.ID)
		if err != nil {
			return err
		}
		if imported != nil {
			job.ImportedExperiences[candidate.ID] = imported.ID
		} else if reader, ok := s.Store.(interface {
			FindKnowledgeCandidate(context.Context, string, string, string) (string, error)
		}); ok {
			id, e := reader.FindKnowledgeCandidate(ctx, uid, job.ID, candidate.ID)
			if e != nil {
				return e
			}
			if id != "" {
				job.ImportedExperiences[candidate.ID] = id
			}
		}
	}
	source, e := s.Store.LoadLearningSource(ctx, uid, job.QuestionID)
	if e != nil {
		return e
	}
	job.SourceCurrent = jobMatchesSource(job, source)
	job.CanImport = false
	job.BlockedReason = sourceReason(source)
	if job.BlockedReason == "" && !job.SourceCurrent {
		job.BlockedReason = "此结果对应的源题版本或状态已变化，请重新分析"
	}
	if job.BlockedReason == "" && job.Status != "succeeded" {
		job.BlockedReason = "分析尚未成功完成"
	}
	if job.BlockedReason == "" && !domain.LearningCapabilitiesFor(p).Save {
		job.BlockedReason = "当前身份无个人经验保存权限"
	}
	job.CanImport = job.BlockedReason == "" && len(job.Candidates) > 0
	if job.CanImport && len(job.ImportedExperiences) == len(job.Candidates) {
		job.CanImport = false
		job.BlockedReason = "全部候选已导入个人知识库，请在我的经验中管理或修订已有经验"
	}
	job.Warnings = candidateWarnings(job.Candidates)
	return nil
}

func (s *Service) guardJobSource(ctx context.Context, uid string, job *domain.LearningJob, e domain.LearningExpectation) error {
	if e.QuestionID != job.QuestionID {
		return storage.ErrLearningConflict
	}
	source, err := s.Store.LoadLearningSource(ctx, uid, job.QuestionID)
	if err != nil {
		return err
	}
	if !matchesExpectation(source, e) || !jobMatchesSource(job, source) || sourceReason(source) != "" {
		return storage.ErrLearningConflict
	}
	return nil
}
func (s *Service) UpdateCandidates(ctx context.Context, uid, role string, req domain.LearningCandidateEdit) (result *domain.LearningJob, err error) {
	if e := validateExpectation(req.LearningExpectation); e != nil {
		return nil, e
	}
	if req.ExpectedRevision < 1 || len(req.Candidates) > 3 {
		return nil, storage.ErrLearningInvalid
	}
	if len(req.Candidates) > 0 {
		if e := validateCandidates(req.Candidates, false); e != nil {
			return nil, e
		}
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
		if job.Status != "succeeded" || job.Revision != req.ExpectedRevision {
			return storage.ErrLearningConflict
		}
		if e = s.guardJobSource(tx, uid, job, req.LearningExpectation); e != nil {
			return e
		}
		candidates := map[string]domain.LearningCandidate{}
		for _, c := range job.Candidates {
			candidates[c.ID] = c
		}
		for i := range req.Candidates {
			candidate := &req.Candidates[i]
			prior, ok := candidates[candidate.ID]
			if !ok || candidate.Style != prior.Style {
				return storage.ErrLearningConflict
			}
			if e = preserveRuleEvidence(candidate.Rules, prior.Rules); e != nil {
				return e
			}
		}
		if e = s.Store.SaveLearningCandidates(tx, uid, job.ID, req.ExpectedRevision, req.Candidates); e != nil {
			return e
		}
		if e = s.audit(tx, uid, job.QuestionID, "learning_candidates_edit", fmt.Sprintf("学习任务 %s；候选数%d", job.ID, len(req.Candidates))); e != nil {
			return e
		}
		result, e = s.Store.GetLearningJob(tx, uid, job.ID)
		if e != nil {
			return e
		}
		return s.decorateJob(tx, uid, p, result)
	})
	return
}
func preserveRuleEvidence(rules, prior []domain.LearningRule) error {
	byID := map[string]domain.LearningRule{}
	for _, r := range prior {
		byID[r.ID] = r
	}
	for i := range rules {
		original, ok := byID[rules[i].ID]
		if !ok {
			return storage.ErrLearningConflict
		}
		rules[i].MedicalChange = rules[i].MedicalChange || original.MedicalChange || original.Field == "answer"
		if rules[i].Field != original.Field {
			return fmt.Errorf("%w: 不能改写原字段定位", storage.ErrLearningInvalid)
		}
	}
	return nil
}
func newID(prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}
func (s *Service) audit(ctx context.Context, uid, qid, action, detail string) error {
	if store, ok := s.Store.(storage.AuditStore); ok {
		id, err := newID("log-learning-")
		if err != nil {
			return err
		}
		return store.SaveLog(ctx, domain.AuditLog{ID: id, QuestionID: qid, Actor: uid, Action: action, Detail: detail, CreatedAt: time.Now().UTC()})
	}
	return nil
}
