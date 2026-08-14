// Package testutil 提供测试用的内存存储实现。
// 仅供单元测试和集成测试使用，不用于生产环境。
package testutil

import (
	"context"
	"strings"
	"sync"

	"aigo/internal/domain"
)

// ===== 题目存储 =====

type MemoryStore struct {
	mu        sync.Mutex
	questions []domain.A2Question
	index     map[string]int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{index: make(map[string]int)}
}

func (s *MemoryStore) SaveQuestion(_ context.Context, q domain.A2Question) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.index[q.ID]; ok {
		s.questions[idx] = q
		return nil
	}
	s.questions = append(s.questions, q)
	s.index[q.ID] = len(s.questions) - 1
	return nil
}

func (s *MemoryStore) SaveQuestions(_ context.Context, questions []domain.A2Question) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, q := range questions {
		s.questions = append(s.questions, q)
		s.index[q.ID] = len(s.questions) - 1
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
	mu      sync.Mutex
	flows   map[string]domain.ReviewFlowConfig
	tasks   map[string]domain.ReviewTask
	records map[string][]domain.ReviewRecord
}

func NewMemoryReviewStore() *MemoryReviewStore {
	return &MemoryReviewStore{
		flows:   make(map[string]domain.ReviewFlowConfig),
		tasks:   make(map[string]domain.ReviewTask),
		records: make(map[string][]domain.ReviewRecord),
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
	s.tasks[t.ID] = t
	return nil
}

func (s *MemoryReviewStore) CountActiveTasksByFlow(_ context.Context, flowID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, t := range s.tasks {
		if t.FlowID == flowID && (t.Status == domain.StatusReviewing || t.Status == domain.StatusRevisionRequired) {
			count++
		}
	}
	return count, nil
}

func (s *MemoryReviewStore) SaveRecord(_ context.Context, r domain.ReviewRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// ===== 图片存储 =====

type MemoryImageStore struct {
	mu       sync.Mutex
	prompts  map[string]domain.ImagePrompt
	images   map[string]domain.GeneratedImage
	records  map[string][]domain.ImageReviewRecord
	byQID    map[string]string
	byPrompt map[string][]string
}

func NewMemoryImageStore() *MemoryImageStore {
	return &MemoryImageStore{
		prompts:  make(map[string]domain.ImagePrompt),
		images:   make(map[string]domain.GeneratedImage),
		records:  make(map[string][]domain.ImageReviewRecord),
		byQID:    make(map[string]string),
		byPrompt: make(map[string][]string),
	}
}

func (s *MemoryImageStore) SavePrompt(_ context.Context, p domain.ImagePrompt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts[p.ID] = p
	s.byQID[p.QuestionID] = p.ID
	return nil
}

func (s *MemoryImageStore) GetPrompt(_ context.Context, id string) (*domain.ImagePrompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prompts[id]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

func (s *MemoryImageStore) GetPromptByQuestionID(_ context.Context, qid string) (*domain.ImagePrompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok := s.byQID[qid]
	if !ok {
		return nil, nil
	}
	p, ok := s.prompts[pid]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

func (s *MemoryImageStore) SaveImage(_ context.Context, img domain.GeneratedImage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images[img.ID] = img
	s.byPrompt[img.PromptID] = append(s.byPrompt[img.PromptID], img.ID)
	return nil
}

func (s *MemoryImageStore) GetImage(_ context.Context, id string) (*domain.GeneratedImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, ok := s.images[id]
	if !ok {
		return nil, nil
	}
	return &img, nil
}

func (s *MemoryImageStore) ListImagesByQuestionID(_ context.Context, qid string) ([]domain.GeneratedImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok := s.byQID[qid]
	if !ok {
		return nil, nil
	}
	ids := s.byPrompt[pid]
	var result []domain.GeneratedImage
	for _, id := range ids {
		if img, ok := s.images[id]; ok {
			result = append(result, img)
		}
	}
	return result, nil
}

func (s *MemoryImageStore) ListImagesByPromptID(_ context.Context, pid string) ([]domain.GeneratedImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.byPrompt[pid]
	var result []domain.GeneratedImage
	for _, id := range ids {
		if img, ok := s.images[id]; ok {
			result = append(result, img)
		}
	}
	return result, nil
}

func (s *MemoryImageStore) UpdateImageStatus(_ context.Context, id string, status domain.ImageStatus, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, ok := s.images[id]
	if !ok {
		return nil
	}
	img.Status = status
	img.ReviewNote = note
	s.images[id] = img
	return nil
}

func (s *MemoryImageStore) SaveReviewRecord(_ context.Context, r domain.ImageReviewRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[r.ImageID] = append(s.records[r.ImageID], r)
	return nil
}

func (s *MemoryImageStore) ListReviewRecordsByImageID(_ context.Context, imageID string) ([]domain.ImageReviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ImageReviewRecord, len(s.records[imageID]))
	copy(out, s.records[imageID])
	return out, nil
}

// ===== 知识点存储 =====

type MemoryKnowledgeStore struct {
	mu     sync.Mutex
	points []domain.KnowledgePoint
	index  map[string]int
}

func NewMemoryKnowledgeStore() *MemoryKnowledgeStore {
	return &MemoryKnowledgeStore{index: make(map[string]int)}
}

func (s *MemoryKnowledgeStore) SavePoints(_ context.Context, points []domain.KnowledgePoint) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, p := range points {
		if idx, ok := s.index[p.ID]; ok {
			s.points[idx] = p
		} else {
			s.points = append(s.points, p)
			s.index[p.ID] = len(s.points) - 1
		}
		count++
	}
	return count, nil
}

func (s *MemoryKnowledgeStore) GetPoint(_ context.Context, id string) (*domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil, nil
	}
	p := s.points[idx]
	return &p, nil
}

func (s *MemoryKnowledgeStore) ListPoints(_ context.Context) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.KnowledgePoint, len(s.points))
	copy(out, s.points)
	return out, nil
}

func (s *MemoryKnowledgeStore) SearchPoints(_ context.Context, keyword string) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kw := strings.ToLower(keyword)
	var result []domain.KnowledgePoint
	for _, p := range s.points {
		if matchPoint(p, kw) {
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
		if p.Subject == subject {
			result = append(result, p)
		}
	}
	return result, nil
}

func (s *MemoryKnowledgeStore) DeletePoint(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil
	}
	lastIdx := len(s.points) - 1
	if idx != lastIdx {
		s.points[idx] = s.points[lastIdx]
		s.index[s.points[idx].ID] = idx
	}
	s.points = s.points[:lastIdx]
	delete(s.index, id)
	return nil
}

func (s *MemoryKnowledgeStore) KPCount(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.points), nil
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
