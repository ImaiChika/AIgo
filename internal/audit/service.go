package audit

import (
	"context"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// Service 审计日志服务。
type Service struct {
	store storage.AuditStore
}

// NewService 创建审计服务。
func NewService(store storage.AuditStore) *Service {
	return &Service{store: store}
}

// Log 记录一条操作日志。
func (s *Service) Log(ctx context.Context, questionID, action, actor, detail string) error {
	return s.store.SaveLog(ctx, domain.AuditLog{
		ID:         fmt.Sprintf("log-%d", time.Now().UnixNano()),
		QuestionID: questionID,
		Action:     action,
		Actor:      actor,
		Detail:     detail,
		CreatedAt:  time.Now(),
	})
}

// LogCreate 记录题目创建。
func (s *Service) LogCreate(ctx context.Context, questionID, actor string) error {
	return s.Log(ctx, questionID, "create", actor, "创建题目")
}

// LogUpdate 记录题目修改。
func (s *Service) LogUpdate(ctx context.Context, questionID, actor, detail string) error {
	return s.Log(ctx, questionID, "update", actor, detail)
}

// LogDelete 记录题目删除。
func (s *Service) LogDelete(ctx context.Context, questionID, actor string) error {
	return s.Log(ctx, questionID, "delete", actor, "删除题目")
}

// LogReview 记录审核操作。
func (s *Service) LogReview(ctx context.Context, questionID, actor, action, opinion string) error {
	detail := fmt.Sprintf("审核结论: %s", action)
	if opinion != "" {
		detail += fmt.Sprintf(", 意见: %s", opinion)
	}
	return s.Log(ctx, questionID, "review", actor, detail)
}

// LogPublish 记录题目发布。
func (s *Service) LogPublish(ctx context.Context, questionID, actor string) error {
	return s.Log(ctx, questionID, "publish", actor, "发布到正式题库")
}

// LogImport 记录批量导入。
func (s *Service) LogImport(ctx context.Context, actor string, count int) error {
	return s.Log(ctx, "", "import", actor, fmt.Sprintf("批量导入 %d 道题目", count))
}

// ListLogs 列出最近的操作日志。
func (s *Service) ListLogs(ctx context.Context, limit int) ([]domain.AuditLog, error) {
	return s.store.ListLogs(ctx, limit)
}

// ListByQuestion 列出某题目的操作日志。
func (s *Service) ListByQuestion(ctx context.Context, questionID string) ([]domain.AuditLog, error) {
	return s.store.ListLogsByQuestion(ctx, questionID)
}

// ListByActor 列出某用户的操作日志。
func (s *Service) ListByActor(ctx context.Context, actor string) ([]domain.AuditLog, error) {
	return s.store.ListLogsByActor(ctx, actor)
}
