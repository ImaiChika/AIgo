package storage

import (
	"context"
	"sync"

	"aigo/internal/domain"
)

// ImageStore 图片相关存储。
type ImageStore interface {
	// 提示词
	SavePrompt(ctx context.Context, prompt domain.ImagePrompt) error
	GetPrompt(ctx context.Context, id string) (*domain.ImagePrompt, error)
	GetPromptByQuestionID(ctx context.Context, questionID string) (*domain.ImagePrompt, error)

	// 候选图
	SaveImage(ctx context.Context, img domain.GeneratedImage) error
	GetImage(ctx context.Context, id string) (*domain.GeneratedImage, error)
	ListImagesByQuestionID(ctx context.Context, questionID string) ([]domain.GeneratedImage, error)
	ListImagesByPromptID(ctx context.Context, promptID string) ([]domain.GeneratedImage, error)
	UpdateImageStatus(ctx context.Context, id string, status domain.ImageStatus, note string) error

	// 审核记录
	SaveReviewRecord(ctx context.Context, record domain.ImageReviewRecord) error
	ListReviewRecordsByImageID(ctx context.Context, imageID string) ([]domain.ImageReviewRecord, error)
}

// MemoryImageStore 内存图片存储。
type MemoryImageStore struct {
	mu       sync.Mutex
	prompts  map[string]domain.ImagePrompt           // promptID → prompt
	images   map[string]domain.GeneratedImage        // imageID → image
	records  map[string][]domain.ImageReviewRecord    // imageID → records
	byQID    map[string]string                        // questionID → promptID
	byPrompt map[string][]string                      // promptID → imageIDs
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

func (s *MemoryImageStore) SavePrompt(_ context.Context, prompt domain.ImagePrompt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts[prompt.ID] = prompt
	s.byQID[prompt.QuestionID] = prompt.ID
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

func (s *MemoryImageStore) GetPromptByQuestionID(_ context.Context, questionID string) (*domain.ImagePrompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok := s.byQID[questionID]
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

func (s *MemoryImageStore) ListImagesByQuestionID(_ context.Context, questionID string) ([]domain.GeneratedImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok := s.byQID[questionID]
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

func (s *MemoryImageStore) ListImagesByPromptID(_ context.Context, promptID string) ([]domain.GeneratedImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.byPrompt[promptID]
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

func (s *MemoryImageStore) SaveReviewRecord(_ context.Context, record domain.ImageReviewRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.ImageID] = append(s.records[record.ImageID], record)
	return nil
}

func (s *MemoryImageStore) ListReviewRecordsByImageID(_ context.Context, imageID string) ([]domain.ImageReviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ImageReviewRecord, len(s.records[imageID]))
	copy(out, s.records[imageID])
	return out, nil
}
