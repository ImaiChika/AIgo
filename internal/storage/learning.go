package storage

import (
	"context"
	"errors"
	"time"

	"aigo/internal/domain"
)

var (
	ErrLearningDenied      = errors.New("无权操作此学习任务或个人经验")
	ErrLearningConflict    = errors.New("源题、候选或个人经验已变化，请刷新后重试")
	ErrLearningInvalid     = errors.New("学习请求无效")
	ErrLearningUnavailable = errors.New("学习服务暂不可用")
	ErrLearningLeaseLost   = errors.New("学习任务执行租约已失效")
	ErrLearningNotFound    = errors.New("学习任务或个人经验不存在")
	ErrLearningBusy        = errors.New("学习任务名额已满：每人最多1个在途任务，全站最多20个，请稍后重试")
)

type LearningOutcome struct {
	Status           string
	Candidates       []domain.LearningCandidate
	ErrorKind, Error string
	ModelCallIDs     []string
	RetryAt          *time.Time
}

type LearningImportOperation struct {
	OwnerID, ImportKey, RequestHash, JobID string
	ExperienceIDs                          []string
	CreatedAt                              time.Time
}

// LearningStore callbacks use a short transaction and serialize current-role
// permissions with source guards. Never call a model inside WithLearningAccess.
type LearningStore interface {
	WithLearningAccess(context.Context, string, string, func(context.Context, []string) error) error
	LoadLearningSource(context.Context, string, string) (*domain.LearningSource, error)
	FindLearningJobByKey(context.Context, string, string) (*domain.LearningJob, error)
	GetLearningJob(context.Context, string, string) (*domain.LearningJob, error)
	ListLearningJobs(context.Context, string, string, int) ([]domain.LearningJob, error)
	CreateLearningJob(context.Context, domain.LearningJob) error
	// Claim enforces the configured global learning concurrency; fixes deadline_at on first
	// admission (120s total), increments attempts <=2 and returns a private token.
	ClaimLearningJob(context.Context, time.Duration) (*domain.LearningJob, error)
	// Every completion/failure/retry is fenced by id+attempt+lease token.
	FinishLearningJob(context.Context, string, int, string, LearningOutcome) error
	SaveLearningCandidates(context.Context, string, string, int, []domain.LearningCandidate) error
	// includeArchived=false lists drafts/active cards; true also returns archived.
	ListAuthoringExperiences(context.Context, string, bool) ([]domain.AuthoringExperience, error)
	GetAuthoringExperience(context.Context, string, string) (*domain.AuthoringExperience, error)
	FindExperienceBySource(context.Context, string, string, string) (*domain.AuthoringExperience, error)
	SaveAuthoringExperience(context.Context, domain.AuthoringExperience, int) error
	ListAuthoringExperienceVersions(context.Context, string, string) ([]domain.AuthoringExperience, error)
	GetLearningImportOperation(context.Context, string, string) (*LearningImportOperation, error)
	SaveLearningImportOperation(context.Context, LearningImportOperation) error
}

// Optional bounded SQL queries keep HTTP pagination and generation selection
// from materializing an author's complete experience history.
type ExperienceQueryStore interface {
	SearchAuthoringExperiences(context.Context, string, string, string, int, int) ([]domain.AuthoringExperience, int, error)
	MatchAuthoringExperiences(context.Context, string, []string, []string, int, ...string) ([]domain.AuthoringExperience, int, error)
}
