// Package testutil 提供测试用的内存存储实现。
// 仅供单元测试和集成测试使用，不用于生产环境。
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// ===== 题目存储 =====

type MemoryStore struct {
	mu                   sync.Mutex
	questions            []domain.A2Question
	index                map[string]int
	versions             map[string][]domain.QuestionVersion
	generationRuns       map[string]domain.GenerationRun
	failNextSaveQuestion error
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		index:          make(map[string]int),
		versions:       make(map[string][]domain.QuestionVersion),
		generationRuns: make(map[string]domain.GenerationRun),
	}
}

func (s *MemoryStore) SaveQuestion(ctx context.Context, q domain.A2Question) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNextSaveQuestion != nil {
		err := s.failNextSaveQuestion
		s.failNextSaveQuestion = nil
		return err
	}
	return s.saveQuestionLocked(ctx, q)
}

func (s *MemoryStore) saveQuestionLocked(ctx context.Context, q domain.A2Question) error {
	q.Status = domain.CanonicalLifecycleStatus(q.Status)
	if q.Version < 1 {
		return fmt.Errorf("%w: 题目 %s 的版本号必须从 1 开始", domain.ErrQuestionVersionConflict, q.ID)
	}
	idx, exists := s.index[q.ID]
	contentChanged := !exists
	if exists {
		existing := s.questions[idx]
		contentChanged = !domain.QuestionContentEqual(existing, q)
		if contentChanged && (existing.Status == domain.StatusReviewing || existing.Status == domain.StatusConflict) {
			return fmt.Errorf("%w: 题目 %s 正在审核或等待决断，不能修改内容", domain.ErrQuestionVersionConflict, q.ID)
		}
		if contentChanged && q.Version != existing.Version+1 {
			return fmt.Errorf("%w: 题目 %s 内容变化时必须从 %d 递增到 %d", domain.ErrQuestionVersionConflict, q.ID, existing.Version, existing.Version+1)
		}
		if !contentChanged && q.Version != existing.Version {
			return fmt.Errorf("%w: 题目 %s 内容未变化，版本号不能从 %d 变为 %d", domain.ErrQuestionVersionConflict, q.ID, existing.Version, q.Version)
		}
		q.CreatedBy = existing.CreatedBy
		q.OwnerID = existing.OwnerID
		s.questions[idx] = q
	} else {
		if q.Version != 1 {
			return fmt.Errorf("%w: 新题目 %s 的初始版本必须为 1", domain.ErrQuestionVersionConflict, q.ID)
		}
		if strings.TrimSpace(q.CreatedBy) == "" {
			q.CreatedBy = storage.QuestionChangeFromContext(ctx).Actor
		}
		if strings.TrimSpace(q.CreatedBy) == "" {
			q.CreatedBy = "system"
		}
		if strings.TrimSpace(q.OwnerID) == "" {
			q.OwnerID = storage.QuestionChangeFromContext(ctx).OwnerID
		}
		s.questions = append(s.questions, q)
		s.index[q.ID] = len(s.questions) - 1
	}
	if contentChanged {
		change := storage.QuestionChangeFromContext(ctx)
		if change.Actor == "" {
			change.Actor = "system"
		}
		if change.ChangeType == "" {
			if q.Version == 1 {
				change.ChangeType = "create"
			} else {
				change.ChangeType = "edit"
			}
		}
		if change.CreatedAt.IsZero() {
			change.CreatedAt = time.Now()
		}
		snapshot, err := cloneJSON(q)
		if err != nil {
			return err
		}
		s.versions[q.ID] = append(s.versions[q.ID], domain.QuestionVersion{
			QuestionID: q.ID, Version: q.Version, Snapshot: snapshot,
			Actor: change.Actor, ChangeType: change.ChangeType, ChangeNote: change.ChangeNote, CreatedAt: change.CreatedAt,
		})
	}
	return nil
}

// ForceQuestionForTest 仅用于模拟绕过应用存储层的历史数据或直接 SQL 篡改。
func (s *MemoryStore) ForceQuestionForTest(q domain.A2Question) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.index[q.ID]; ok {
		s.questions[idx] = q
		return
	}
	s.questions = append(s.questions, q)
	s.index[q.ID] = len(s.questions) - 1
}

// FailNextSaveQuestion 仅供故障注入测试：让下一次 SaveQuestion 返回指定错误。
func (s *MemoryStore) FailNextSaveQuestion(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNextSaveQuestion = err
}

func (s *MemoryStore) SaveQuestions(ctx context.Context, questions []domain.A2Question) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	questionSnapshot, _ := cloneJSON(s.questions)
	indexSnapshot, _ := cloneJSON(s.index)
	versionSnapshot, _ := cloneJSON(s.versions)
	count := 0
	for _, q := range questions {
		q.Status = domain.CanonicalLifecycleStatus(q.Status)
		if idx, ok := s.index[q.ID]; ok {
			existing := s.questions[idx]
			if domain.QuestionContentEqual(existing, q) {
				count++
				continue
			}
			if existing.Status == domain.StatusReviewing || existing.Status == domain.StatusConflict {
				s.questions, s.index, s.versions = questionSnapshot, indexSnapshot, versionSnapshot
				return count, fmt.Errorf("题目 %s 正在审核或等待决断，不能通过导入覆盖内容", q.ID)
			}
			q.Version = existing.Version + 1
			q.CreatedAt = existing.CreatedAt
			q.Status = domain.StatusAIDraft
			if len(q.BankIDs) == 0 {
				q.BankIDs = append([]string(nil), existing.BankIDs...)
			}
		}
		if q.Version < 1 {
			q.Version = 1
		}
		if q.CreatedAt.IsZero() {
			q.CreatedAt = time.Now()
		}
		q.UpdatedAt = time.Now()
		change := storage.QuestionChangeFromContext(ctx)
		if change.Actor == "" {
			change.Actor = "import"
		}
		if change.ChangeType == "" {
			change.ChangeType = "import"
		}
		if err := s.saveQuestionLocked(storage.WithQuestionChange(ctx, change), q); err != nil {
			s.questions, s.index, s.versions = questionSnapshot, indexSnapshot, versionSnapshot
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *MemoryStore) ListQuestions(_ context.Context) ([]domain.A2Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.A2Question, len(s.questions))
	copy(out, s.questions)
	return out, nil
}

// filterQuestionsLocked 按过滤条件筛选内存中的题目（调用方需持锁）。
// 过滤语义与 PostgreSQL 的 questionFilterWhere 保持一致。
func (s *MemoryStore) filterQuestionsLocked(filter storage.QuestionFilter) []domain.A2Question {
	tierStatuses := map[string]bool{}
	for _, tier := range filter.Tiers {
		for _, st := range domain.TierStatuses(domain.QuestionTier(tier)) {
			tierStatuses[string(st)] = true
		}
	}
	matched := make([]domain.A2Question, 0, len(s.questions))
	for _, q := range s.questions {
		if filter.OwnerID != "" && q.OwnerID != filter.OwnerID && !(filter.IncludeLegacyOwner && q.OwnerID == "") {
			continue
		}
		if len(filter.GlobalStatuses) > 0 {
			// 内存存储没有分享申请表；无 owner 的题目按历史全局题兼容，
			// 有 owner 的个人题目不能伪装成全局题。
			if !filter.IncludeLegacyGlobal || q.OwnerID != "" {
				continue
			}
			globalStatus := "pending"
			switch q.Tier() {
			case domain.TierFormal:
				globalStatus = "approved"
			case domain.TierEliminated:
				globalStatus = "rejected"
			}
			if !containsString(filter.GlobalStatuses, globalStatus) {
				continue
			}
		}
		if filter.Status != "" && string(q.Status) != filter.Status {
			continue
		}
		if len(tierStatuses) > 0 && !tierStatuses[string(q.Status)] {
			continue
		}
		if filter.Difficulty != "" && string(q.Difficulty) != filter.Difficulty {
			continue
		}
		if filter.DifficultyBand != "" && !storage.DifficultyMatchesBand(string(q.Difficulty), filter.DifficultyBand) {
			continue
		}
		if len(filter.Professions) > 0 && !containsString(filter.Professions, q.Profession) {
			continue
		}
		if filter.OutlineCode != "" && !strings.HasPrefix(q.OutlineCode, filter.OutlineCode) {
			continue
		}
		if !storage.QuestionMatchesKeyword(q, filter.Keyword) {
			continue
		}
		if filter.BankID != "" && !containsString(q.BankIDs, filter.BankID) {
			continue
		}
		if filter.Unclassified && len(q.BankIDs) > 0 {
			continue
		}
		if filter.ClassifiableOnly && q.Status != domain.StatusAIDraft && q.Status != domain.StatusAutoChecked &&
			q.Status != domain.StatusAIReviewed && q.Status != domain.StatusRevisionRequired {
			continue
		}
		if filter.ScopeRestricted {
			if len(filter.BankScope) == 0 || !hasAnyBank(q.BankIDs, filter.BankScope) {
				continue
			}
		}
		matched = append(matched, q)
	}
	return matched
}

// SearchQuestions 与 PostgreSQL 实现保持相同的过滤/排序/分页语义（内存版）。
func (s *MemoryStore) SearchQuestions(_ context.Context, filter storage.QuestionFilter, page, pageSize int) ([]domain.A2Question, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 100
	}
	matched := s.filterQuestionsLocked(filter)
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})
	total := len(matched)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return matched[start:end], total, nil
}

// CountQuestionsByStatus 按状态统计题目数量（内存版聚合，语义与 PostgreSQL 一致）。
func (s *MemoryStore) CountQuestionsByStatus(_ context.Context, filter storage.QuestionFilter) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	for _, q := range s.filterQuestionsLocked(filter) {
		counts[string(q.Status)]++
	}
	return counts, nil
}

// AggregateQuestionStats 内存版聚合统计；days<=0 视为 30。
func (s *MemoryStore) AggregateQuestionStats(_ context.Context, filter storage.QuestionFilter, days int) (*storage.QuestionStatsAggregate, error) {
	if days <= 0 {
		days = 30
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := s.filterQuestionsLocked(filter)
	agg := &storage.QuestionStatsAggregate{
		ByStatus:     map[string]int{},
		ByDifficulty: map[string]int{},
		ByProfession: map[string]int{},
		ByDay:        map[string]int{},
		ByBank:       map[string]int{},
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	for _, q := range matched {
		agg.Total++
		agg.ByStatus[string(q.Status)]++
		agg.ByDifficulty[string(q.Difficulty)]++
		agg.ByProfession[q.Profession]++
		if !q.CreatedAt.Before(cutoff) {
			agg.ByDay[q.CreatedAt.Format("2006-01-02")]++
		}
		if len(q.BankIDs) == 0 {
			agg.Unclassified++
		} else {
			for _, b := range q.BankIDs {
				agg.ByBank[b]++
			}
		}
	}
	// 专业取数量前 12（与 PostgreSQL 实现一致）
	if len(agg.ByProfession) > 12 {
		type professionCount struct {
			name  string
			count int
		}
		list := make([]professionCount, 0, len(agg.ByProfession))
		for name, count := range agg.ByProfession {
			list = append(list, professionCount{name, count})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].count > list[j].count })
		top := make(map[string]int, 12)
		for _, item := range list[:12] {
			top[item.name] = item.count
		}
		agg.ByProfession = top
	}
	return agg, nil
}

// CoveredKnowledgePointIDs 返回过滤范围内题目引用的知识点 ID 去重列表。
func (s *MemoryStore) CoveredKnowledgePointIDs(_ context.Context, filter storage.QuestionFilter) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	ids := []string{}
	for _, q := range s.filterQuestionsLocked(filter) {
		for _, kp := range q.KnowledgePoints {
			if kp.ID != "" && !seen[kp.ID] {
				seen[kp.ID] = true
				ids = append(ids, kp.ID)
			}
		}
	}
	return ids, nil
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func hasAnyBank(bankIDs, scope []string) bool {
	for _, b := range bankIDs {
		if containsString(scope, b) {
			return true
		}
	}
	return false
}

func (s *MemoryStore) ListProfessions(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]bool)
	out := []string{}
	for _, q := range s.questions {
		if q.Profession != "" && !seen[q.Profession] {
			seen[q.Profession] = true
			out = append(out, q.Profession)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *MemoryStore) CreateGenerationRun(_ context.Context, run domain.GenerationRun) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.generationRuns[run.ID]; exists {
		return false, nil
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.MaxAttempts <= 0 {
		run.MaxAttempts = 3
	}
	run.UpdatedAt = run.StartedAt
	run.QuestionIDs = append([]string(nil), run.QuestionIDs...)
	run.RequestJSON = append([]byte(nil), run.RequestJSON...)
	s.generationRuns[run.ID] = run
	return true, nil
}

func (s *MemoryStore) GetGenerationRun(_ context.Context, id string) (*domain.GenerationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.generationRuns[id]
	if !exists {
		return nil, nil
	}
	run.QuestionIDs = append([]string(nil), run.QuestionIDs...)
	run.RequestJSON = append([]byte(nil), run.RequestJSON...)
	return &run, nil
}

// ClaimNextGenerationRun 抢占 pending 或租约过期的 running 运行，语义与 PostgreSQL 实现一致。
func (s *MemoryStore) ClaimNextGenerationRun(_ context.Context, lease time.Duration) (*domain.GenerationRun, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	oldestID := ""
	var oldest time.Time
	for id, run := range s.generationRuns {
		claimable := (run.Status == domain.GenerationRunPending && (run.LeasedUntil.IsZero() || !run.LeasedUntil.After(now))) ||
			(run.Status == domain.GenerationRunRunning && !run.LeasedUntil.IsZero() && !run.LeasedUntil.After(now))
		if !claimable {
			continue
		}
		if oldestID == "" || run.StartedAt.Before(oldest) {
			oldestID = id
			oldest = run.StartedAt
		}
	}
	if oldestID == "" {
		return nil, nil, nil
	}
	run := s.generationRuns[oldestID]
	run.Status = domain.GenerationRunRunning
	run.Attempts++
	run.LeasedUntil = now.Add(lease)
	run.UpdatedAt = now
	s.generationRuns[oldestID] = run
	spec := append([]byte(nil), run.RequestJSON...)
	return cloneGenerationRun(&run), spec, nil
}

// RetryGenerationRun 未达上限回 pending 退避重试，已达上限标记 failed 终态。
func (s *MemoryStore) RetryGenerationRun(_ context.Context, id, errMsg string, backoff time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.generationRuns[id]
	if !exists || run.Status != domain.GenerationRunRunning {
		return false, nil
	}
	now := time.Now()
	if run.Attempts >= run.MaxAttempts {
		run.Status = domain.GenerationRunFailed
		run.Error = errMsg
		run.CompletedAt = &now
		run.LeasedUntil = time.Time{}
	} else {
		run.Status = domain.GenerationRunPending
		run.Error = errMsg
		run.LeasedUntil = now.Add(backoff)
	}
	run.UpdatedAt = now
	s.generationRuns[id] = run
	return run.Status == domain.GenerationRunPending, nil
}

func (s *MemoryStore) CompleteGenerationRun(_ context.Context, id string, questionIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.generationRuns[id]
	if !exists || run.Status != domain.GenerationRunRunning {
		return nil
	}
	now := time.Now()
	run.Status = domain.GenerationRunSucceeded
	run.QuestionIDs = append([]string(nil), questionIDs...)
	run.Error = ""
	run.CompletedAt = &now
	run.LeasedUntil = time.Time{}
	run.UpdatedAt = now
	s.generationRuns[id] = run
	return nil
}

func (s *MemoryStore) FailGenerationRun(_ context.Context, id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.generationRuns[id]
	if !exists || run.Status != domain.GenerationRunRunning {
		return nil
	}
	now := time.Now()
	run.Status = domain.GenerationRunFailed
	run.Error = message
	run.CompletedAt = &now
	run.LeasedUntil = time.Time{}
	run.UpdatedAt = now
	s.generationRuns[id] = run
	return nil
}

// cloneGenerationRun 复制运行记录（含切片与快照字节），避免调用方修改内部状态。
func cloneGenerationRun(run *domain.GenerationRun) *domain.GenerationRun {
	out := *run
	out.QuestionIDs = append([]string(nil), run.QuestionIDs...)
	out.RequestJSON = append([]byte(nil), run.RequestJSON...)
	return &out
}

// OwnerBankIDs 返回指定用户的题目所关联的分类子题库 ID 去重列表。
func (s *MemoryStore) OwnerBankIDs(_ context.Context, ownerID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, q := range s.questions {
		if q.OwnerID != ownerID {
			continue
		}
		for _, b := range q.BankIDs {
			set[b] = true
		}
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Strings(out)
	return out, nil
}

func (s *MemoryStore) GetQuestion(_ context.Context, id string) (*domain.A2Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil, nil
	}
	q := s.questions[idx]
	return &q, nil
}

func (s *MemoryStore) ListQuestionVersions(_ context.Context, questionID string) ([]domain.QuestionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions, err := cloneJSON(s.versions[questionID])
	if err != nil {
		return nil, err
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	return versions, nil
}

func (s *MemoryStore) GetQuestionVersion(_ context.Context, questionID string, version int) (*domain.QuestionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.versions[questionID] {
		if item.Version == version {
			clone, err := cloneJSON(item)
			if err != nil {
				return nil, err
			}
			return &clone, nil
		}
	}
	return nil, nil
}

func (s *MemoryStore) DeleteQuestion(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil
	}
	lastIdx := len(s.questions) - 1
	if idx != lastIdx {
		s.questions[idx] = s.questions[lastIdx]
		s.index[s.questions[idx].ID] = idx
	}
	s.questions = s.questions[:lastIdx]
	delete(s.index, id)
	delete(s.versions, id)
	return nil
}

func (s *MemoryStore) Count(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.questions), nil
}

// ===== 专家存储 =====

type MemoryExpertStore struct {
	mu      sync.Mutex
	experts map[string]domain.Expert
}

func NewMemoryExpertStore() *MemoryExpertStore {
	return &MemoryExpertStore{experts: make(map[string]domain.Expert)}
}

func (s *MemoryExpertStore) SaveExpert(_ context.Context, e domain.Expert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.experts[e.ID] = e
	return nil
}

func (s *MemoryExpertStore) GetExpert(_ context.Context, id string) (*domain.Expert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.experts[id]
	if !ok {
		return nil, nil
	}
	return &e, nil
}

func (s *MemoryExpertStore) ListExperts(_ context.Context) ([]domain.Expert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []domain.Expert
	for _, e := range s.experts {
		list = append(list, e)
	}
	return list, nil
}

func (s *MemoryExpertStore) UpdateExpert(_ context.Context, e domain.Expert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.experts[e.ID] = e
	return nil
}

func (s *MemoryExpertStore) DeleteExpert(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.experts, id)
	return nil
}

// ===== 审核存储 =====

type MemoryReviewStore struct {
	mu           sync.Mutex
	mutationLock sync.Mutex
	flows        map[string]domain.ReviewFlowConfig
	tasks        map[string]domain.ReviewTask
	records      map[string][]domain.ReviewRecord
	failNext     map[string]error
}

func NewMemoryReviewStore() *MemoryReviewStore {
	return &MemoryReviewStore{
		flows:    make(map[string]domain.ReviewFlowConfig),
		tasks:    make(map[string]domain.ReviewTask),
		records:  make(map[string][]domain.ReviewRecord),
		failNext: make(map[string]error),
	}
}

// FailNext 仅供故障注入测试：支持 save_task/update_task/delete_task/save_record。
func (s *MemoryReviewStore) FailNext(operation string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[operation] = err
}

func (s *MemoryReviewStore) consumeFailureLocked(operation string) error {
	err := s.failNext[operation]
	delete(s.failNext, operation)
	return err
}

// WithReviewTransaction 对内存审核/题目数据做深拷贝快照，回调失败时整体恢复。
func (s *MemoryReviewStore) WithReviewTransaction(ctx context.Context, questionStore storage.QuestionStore, fn func(context.Context) error) error {
	questions, ok := questionStore.(*MemoryStore)
	if !ok {
		return fmt.Errorf("内存审核事务要求使用 *testutil.MemoryStore 作为题目存储")
	}

	s.mu.Lock()
	flowsSnapshot, err := cloneJSON(s.flows)
	if err == nil {
		var tasksSnapshot map[string]domain.ReviewTask
		tasksSnapshot, err = cloneJSON(s.tasks)
		if err == nil {
			var recordsSnapshot map[string][]domain.ReviewRecord
			recordsSnapshot, err = cloneJSON(s.records)
			if err == nil {
				s.mu.Unlock()

				questions.mu.Lock()
				questionSnapshot, cloneErr := cloneJSON(questions.questions)
				versionSnapshot, versionCloneErr := cloneJSON(questions.versions)
				questions.mu.Unlock()
				if cloneErr != nil {
					return cloneErr
				}
				if versionCloneErr != nil {
					return versionCloneErr
				}

				if callbackErr := fn(ctx); callbackErr != nil {
					s.mu.Lock()
					s.flows = flowsSnapshot
					s.tasks = tasksSnapshot
					s.records = recordsSnapshot
					s.mu.Unlock()

					questions.mu.Lock()
					questions.questions = questionSnapshot
					questions.versions = versionSnapshot
					questions.index = make(map[string]int, len(questionSnapshot))
					for i := range questionSnapshot {
						questions.index[questionSnapshot[i].ID] = i
					}
					questions.mu.Unlock()
					return callbackErr
				}
				return nil
			}
		}
	}
	s.mu.Unlock()
	return fmt.Errorf("创建内存审核事务快照失败: %w", err)
}

func cloneJSON[T any](value T) (T, error) {
	var clone T
	data, err := json.Marshal(value)
	if err != nil {
		return clone, err
	}
	if err := json.Unmarshal(data, &clone); err != nil {
		return clone, err
	}
	return clone, nil
}

func (s *MemoryReviewStore) AcquireReviewMutationLock(ctx context.Context) (func() error, error) {
	locked := make(chan struct{})
	go func() {
		s.mutationLock.Lock()
		close(locked)
	}()
	select {
	case <-ctx.Done():
		// goroutine 最终拿到锁后必须立即释放，避免遗留永久锁。
		go func() {
			<-locked
			s.mutationLock.Unlock()
		}()
		return nil, ctx.Err()
	case <-locked:
		var once sync.Once
		return func() error {
			once.Do(s.mutationLock.Unlock)
			return nil
		}, nil
	}
}

func (s *MemoryReviewStore) SaveFlowConfig(_ context.Context, f domain.ReviewFlowConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flows[f.ID] = f
	return nil
}

func (s *MemoryReviewStore) GetFlowConfig(_ context.Context, id string) (*domain.ReviewFlowConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[id]
	if !ok {
		return nil, nil
	}
	return &f, nil
}

func (s *MemoryReviewStore) ListFlowConfigs(_ context.Context) ([]domain.ReviewFlowConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []domain.ReviewFlowConfig
	for _, f := range s.flows {
		list = append(list, f)
	}
	return list, nil
}

func (s *MemoryReviewStore) DeleteFlowConfig(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.flows, id)
	return nil
}

func (s *MemoryReviewStore) SaveTask(_ context.Context, t domain.ReviewTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Status = domain.CanonicalLifecycleStatus(t.Status)
	if t.QuestionVersion < 1 {
		return fmt.Errorf("%w: 审核任务 %s 必须绑定有效题目版本", domain.ErrQuestionVersionConflict, t.ID)
	}
	if err := s.consumeFailureLocked("save_task"); err != nil {
		return err
	}
	s.tasks[t.ID] = t
	return nil
}

func (s *MemoryReviewStore) GetTask(_ context.Context, id string) (*domain.ReviewTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, nil
	}
	return &t, nil
}

func (s *MemoryReviewStore) GetTaskByQuestionID(_ context.Context, qid string) (*domain.ReviewTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.QuestionID == qid {
			return &t, nil
		}
	}
	return nil, nil
}

func (s *MemoryReviewStore) UpdateTask(_ context.Context, t domain.ReviewTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Status = domain.CanonicalLifecycleStatus(t.Status)
	if t.QuestionVersion < 1 {
		return fmt.Errorf("%w: 审核任务 %s 必须绑定有效题目版本", domain.ErrQuestionVersionConflict, t.ID)
	}
	if err := s.consumeFailureLocked("update_task"); err != nil {
		return err
	}
	s.tasks[t.ID] = t
	return nil
}

func (s *MemoryReviewStore) DeleteTask(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.consumeFailureLocked("delete_task"); err != nil {
		return err
	}
	delete(s.tasks, id)
	delete(s.records, id)
	return nil
}

func (s *MemoryReviewStore) CountActiveTasksByFlow(_ context.Context, flowID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, t := range s.tasks {
		if t.FlowID == flowID && (t.Status == domain.StatusReviewing || t.Status == domain.StatusRevisionRequired || t.Status == domain.StatusConflict) {
			count++
		}
	}
	return count, nil
}

func (s *MemoryReviewStore) CountTasksByFlow(_ context.Context, flowID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, t := range s.tasks {
		if t.FlowID == flowID {
			count++
		}
	}
	return count, nil
}

func (s *MemoryReviewStore) SaveRecord(_ context.Context, r domain.ReviewRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.consumeFailureLocked("save_record"); err != nil {
		return err
	}
	s.records[r.TaskID] = append(s.records[r.TaskID], r)
	return nil
}

func (s *MemoryReviewStore) ListRecordsByTaskID(_ context.Context, taskID string) ([]domain.ReviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ReviewRecord, len(s.records[taskID]))
	copy(out, s.records[taskID])
	return out, nil
}

func (s *MemoryReviewStore) ListAllTasks(_ context.Context) ([]domain.ReviewTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ReviewTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	return out, nil
}

func (s *MemoryReviewStore) ListRecordsByTaskIDs(_ context.Context, taskIDs []string) ([]domain.ReviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ReviewRecord
	for _, taskID := range taskIDs {
		out = append(out, s.records[taskID]...)
	}
	return out, nil
}

// ===== AI 检查结果存储 =====

type MemoryAIReviewStore struct {
	mu       sync.Mutex
	results  []domain.AIReviewResult
	discards []domain.AICheckDiscard
}

func NewMemoryAIReviewStore() *MemoryAIReviewStore {
	return &MemoryAIReviewStore{}
}

func (s *MemoryAIReviewStore) SaveReviewResult(_ context.Context, result domain.AIReviewResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now()
	}
	s.results = append(s.results, result)
	return nil
}

func (s *MemoryAIReviewStore) GetLatestByQuestionID(_ context.Context, questionID string) (*domain.AIReviewResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *domain.AIReviewResult
	for i := range s.results {
		if s.results[i].QuestionID != questionID {
			continue
		}
		if latest == nil || s.results[i].CreatedAt.After(latest.CreatedAt) {
			clone := s.results[i]
			latest = &clone
		}
	}
	return latest, nil
}

func (s *MemoryAIReviewStore) ListByQuestionIDs(_ context.Context, questionIDs []string) ([]domain.AIReviewResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[string]bool, len(questionIDs))
	for _, id := range questionIDs {
		wanted[id] = true
	}
	latest := make(map[string]domain.AIReviewResult)
	for _, r := range s.results {
		if !wanted[r.QuestionID] {
			continue
		}
		if cur, ok := latest[r.QuestionID]; !ok || r.CreatedAt.After(cur.CreatedAt) {
			latest[r.QuestionID] = r
		}
	}
	out := make([]domain.AIReviewResult, 0, len(latest))
	for _, id := range questionIDs {
		if r, ok := latest[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *MemoryAIReviewStore) ListAll(_ context.Context, limit int) ([]domain.AIReviewResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sorted := make([]domain.AIReviewResult, len(s.results))
	copy(sorted, s.results)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})
	if limit <= 0 || limit > len(sorted) {
		limit = len(sorted)
	}
	out := make([]domain.AIReviewResult, limit)
	copy(out, sorted[:limit])
	return out, nil
}

// ===== AI 检查淘汰记录 =====

type MemoryAIDiscardStore struct {
	mu       sync.Mutex
	discards []domain.AICheckDiscard
}

func (s *MemoryAIReviewStore) SaveDiscardResult(_ context.Context, discard domain.AICheckDiscard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if discard.CreatedAt.IsZero() {
		discard.CreatedAt = time.Now()
	}
	s.discards = append(s.discards, discard)
	return nil
}

func (s *MemoryAIReviewStore) ListDiscardResultsByQuestionIDs(_ context.Context, questionIDs []string) (map[string]domain.AICheckDiscard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[string]bool, len(questionIDs))
	for _, id := range questionIDs {
		wanted[id] = true
	}
	out := make(map[string]domain.AICheckDiscard, len(questionIDs))
	// 倒序取每题最新一条
	for i := len(s.discards) - 1; i >= 0; i-- {
		d := s.discards[i]
		if wanted[d.QuestionID] {
			if _, seen := out[d.QuestionID]; !seen {
				out[d.QuestionID] = d
			}
		}
	}
	return out, nil
}

// CountReviewResultsByVerdict 按 verdict 统计 AI 检查结果数量。
func (s *MemoryAIReviewStore) CountReviewResultsByVerdict(_ context.Context) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	for _, r := range s.results {
		counts[r.Verdict]++
	}
	return counts, nil
}

// CountDiscardResults 统计淘汰留档总数。
func (s *MemoryAIReviewStore) CountDiscardResults(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.discards), nil
}

// DiscardCount 返回淘汰记录数（测试观测用）。
func (s *MemoryAIReviewStore) DiscardCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.discards)
}

// ===== 知识点存储 =====

type MemoryKnowledgeStore struct {
	mu              sync.Mutex
	points          []domain.KnowledgePoint
	index           map[string]int
	versions        []domain.KnowledgeVersion
	deletedVersions map[string]bool
	deleted         map[string]bool
}

func NewMemoryKnowledgeStore() *MemoryKnowledgeStore {
	return &MemoryKnowledgeStore{deletedVersions: make(map[string]bool), index: make(map[string]int), deleted: make(map[string]bool), versions: []domain.KnowledgeVersion{{ID: domain.LegacyKnowledgeVersion, Name: "历史大纲（年份待确认）", Status: "published"}}}
}

func (s *MemoryKnowledgeStore) SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, int, int, error) {
	version := domain.LegacyKnowledgeVersion
	if len(points) > 0 && points[0].VersionID != "" {
		version = points[0].VersionID
	}
	return s.SaveVersionPoints(ctx, version, points, false)
}

func (s *MemoryKnowledgeStore) GetPoint(_ context.Context, id string) (*domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok || s.deleted[id] {
		return nil, nil
	}
	p := s.points[idx]
	return &p, nil
}

func (s *MemoryKnowledgeStore) ListPoints(_ context.Context) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.KnowledgePoint{}
	for _, p := range s.points {
		if !s.deleted[p.ID] {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *MemoryKnowledgeStore) SearchPoints(_ context.Context, keyword string) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kw := strings.ToLower(keyword)
	var result []domain.KnowledgePoint
	for _, p := range s.points {
		if !s.deleted[p.ID] && matchPoint(p, kw) {
			result = append(result, p)
		}
	}
	return result, nil
}

func (s *MemoryKnowledgeStore) ListBySubject(_ context.Context, subject string) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.KnowledgePoint
	for _, p := range s.points {
		if !s.deleted[p.ID] && p.Subject == subject {
			result = append(result, p)
		}
	}
	return result, nil
}

func (s *MemoryKnowledgeStore) DeletePoint(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok || s.deleted[id] {
		return storage.ErrKnowledgeNotFound
	}
	s.deleted[id] = true
	s.points[idx].Revision++
	return nil
}
func (s *MemoryKnowledgeStore) KPCount(ctx context.Context) (int, error) {
	points, err := s.ListPoints(ctx)
	return len(points), err
}

func matchPoint(p domain.KnowledgePoint, keyword string) bool {
	if strings.Contains(strings.ToLower(p.Topic), keyword) {
		return true
	}
	if strings.Contains(strings.ToLower(p.OutlineCode), keyword) {
		return true
	}
	if strings.Contains(strings.ToLower(p.Unit), keyword) {
		return true
	}
	if strings.Contains(strings.ToLower(p.SubItem), keyword) {
		return true
	}
	if strings.Contains(strings.ToLower(p.Subject), keyword) {
		return true
	}
	for _, kw := range p.Keywords {
		if strings.Contains(strings.ToLower(kw), keyword) {
			return true
		}
	}
	return false
}

// ===== 审计日志存储 =====

type MemoryAuditStore struct {
	mu   sync.Mutex
	logs []domain.AuditLog
}

func NewMemoryAuditStore() *MemoryAuditStore {
	return &MemoryAuditStore{}
}

func (s *MemoryAuditStore) SaveLog(_ context.Context, log domain.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, log)
	return nil
}

func (s *MemoryAuditStore) ListLogs(_ context.Context, limit int) ([]domain.AuditLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := len(s.logs)
	if limit <= 0 || limit > total {
		limit = total
	}
	result := make([]domain.AuditLog, limit)
	for i := 0; i < limit; i++ {
		result[i] = s.logs[total-1-i]
	}
	return result, nil
}

func (s *MemoryAuditStore) ListLogsByQuestion(_ context.Context, qid string) ([]domain.AuditLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.AuditLog
	for i := len(s.logs) - 1; i >= 0; i-- {
		if s.logs[i].QuestionID == qid {
			result = append(result, s.logs[i])
		}
	}
	return result, nil
}

func (s *MemoryAuditStore) ListLogsByActor(_ context.Context, actor string) ([]domain.AuditLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.AuditLog
	for i := len(s.logs) - 1; i >= 0; i-- {
		if s.logs[i].Actor == actor {
			result = append(result, s.logs[i])
		}
	}
	return result, nil
}
