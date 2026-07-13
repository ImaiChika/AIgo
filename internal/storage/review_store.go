package storage

import (
	"context"
	"sync"

	"aigo/internal/domain"
)

// ExpertStore 专家存储接口。
type ExpertStore interface {
	SaveExpert(ctx context.Context, expert domain.Expert) error
	GetExpert(ctx context.Context, id string) (*domain.Expert, error)
	ListExperts(ctx context.Context) ([]domain.Expert, error)
	UpdateExpert(ctx context.Context, expert domain.Expert) error
	DeleteExpert(ctx context.Context, id string) error
}

// ReviewStore 审核相关存储接口，包括流程配置、任务、记录和版本。
type ReviewStore interface {
	// 审核流程配置
	SaveFlowConfig(ctx context.Context, flow domain.ReviewFlowConfig) error
	GetFlowConfig(ctx context.Context, id string) (*domain.ReviewFlowConfig, error)
	ListFlowConfigs(ctx context.Context) ([]domain.ReviewFlowConfig, error)

	// 审核任务
	SaveTask(ctx context.Context, task domain.ReviewTask) error
	GetTask(ctx context.Context, id string) (*domain.ReviewTask, error)
	GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error)
	UpdateTask(ctx context.Context, task domain.ReviewTask) error

	// 审核记录
	SaveRecord(ctx context.Context, record domain.ReviewRecord) error
	ListRecordsByTaskID(ctx context.Context, taskID string) ([]domain.ReviewRecord, error)

	// 题目版本
	SaveVersion(ctx context.Context, version domain.QuestionVersion) error
	ListVersionsByQuestionID(ctx context.Context, questionID string) ([]domain.QuestionVersion, error)
}

// MemoryExpertStore 内存专家存储。
type MemoryExpertStore struct {
	mu      sync.Mutex
	experts map[string]domain.Expert
}

func NewMemoryExpertStore() *MemoryExpertStore {
	return &MemoryExpertStore{experts: make(map[string]domain.Expert)}
}

func (s *MemoryExpertStore) SaveExpert(_ context.Context, expert domain.Expert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.experts[expert.ID] = expert
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

func (s *MemoryExpertStore) UpdateExpert(_ context.Context, expert domain.Expert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.experts[expert.ID] = expert
	return nil
}

func (s *MemoryExpertStore) DeleteExpert(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.experts, id)
	return nil
}

// MemoryReviewStore 内存审核存储。
type MemoryReviewStore struct {
	mu       sync.Mutex
	flows    map[string]domain.ReviewFlowConfig         // 流程配置
	tasks    map[string]domain.ReviewTask                // 审核任务
	records  map[string][]domain.ReviewRecord            // 审核记录（按任务ID分组）
	versions map[string][]domain.QuestionVersion         // 题目版本（按题目ID分组）
}

func NewMemoryReviewStore() *MemoryReviewStore {
	return &MemoryReviewStore{
		flows:    make(map[string]domain.ReviewFlowConfig),
		tasks:    make(map[string]domain.ReviewTask),
		records:  make(map[string][]domain.ReviewRecord),
		versions: make(map[string][]domain.QuestionVersion),
	}
}

func (s *MemoryReviewStore) SaveFlowConfig(_ context.Context, flow domain.ReviewFlowConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flows[flow.ID] = flow
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

func (s *MemoryReviewStore) SaveTask(_ context.Context, task domain.ReviewTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
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

func (s *MemoryReviewStore) GetTaskByQuestionID(_ context.Context, questionID string) (*domain.ReviewTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.QuestionID == questionID {
			return &t, nil
		}
	}
	return nil, nil
}

func (s *MemoryReviewStore) UpdateTask(_ context.Context, task domain.ReviewTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
	return nil
}

func (s *MemoryReviewStore) SaveRecord(_ context.Context, record domain.ReviewRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.TaskID] = append(s.records[record.TaskID], record)
	return nil
}

func (s *MemoryReviewStore) ListRecordsByTaskID(_ context.Context, taskID string) ([]domain.ReviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ReviewRecord, len(s.records[taskID]))
	copy(out, s.records[taskID])
	return out, nil
}

func (s *MemoryReviewStore) SaveVersion(_ context.Context, version domain.QuestionVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions[version.QuestionID] = append(s.versions[version.QuestionID], version)
	return nil
}

func (s *MemoryReviewStore) ListVersionsByQuestionID(_ context.Context, questionID string) ([]domain.QuestionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.QuestionVersion, len(s.versions[questionID]))
	copy(out, s.versions[questionID])
	return out, nil
}
