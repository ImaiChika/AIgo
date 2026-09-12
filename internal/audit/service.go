package audit

import (
	"context"
	"fmt"
	"strings"
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
	// 最终把关决断使用独立的 final_* 动作留痕，与轮内普通审核投票可按动作区分。
	auditAction := "review"
	if strings.HasPrefix(action, "final_") {
		auditAction = action
	}
	return s.Log(ctx, questionID, auditAction, actor, detail)
}

// LogPublish 记录题目发布。
func (s *Service) LogPublish(ctx context.Context, questionID, actor string) error {
	return s.Log(ctx, questionID, "publish", actor, "发布到正式题库")
}

// LogSubmit 记录提交/重新提交审核。
func (s *Service) LogSubmit(ctx context.Context, questionID, flowID, actor string, resubmit bool) error {
	action := "submit"
	detail := "提交审核"
	if resubmit {
		action = "resubmit"
		detail = "重新提交审核"
	}
	return s.Log(ctx, questionID, action, actor, fmt.Sprintf("%s, 流程: %s", detail, flowID))
}

// LogFlow 记录审核流程配置变更。
func (s *Service) LogFlow(ctx context.Context, flowID, actor, action, detail string) error {
	return s.Log(ctx, "", "flow_"+action, actor, fmt.Sprintf("流程 %s: %s", flowID, detail))
}

// LogExpert 记录专家库变更。
func (s *Service) LogExpert(ctx context.Context, expertID, actor, action, detail string) error {
	return s.Log(ctx, "", "expert_"+action, actor, fmt.Sprintf("专家 %s: %s", expertID, detail))
}

// LogUser 记录用户管理变更。
func (s *Service) LogUser(ctx context.Context, username, actor, action string) error {
	return s.Log(ctx, "", "user_"+action, actor, fmt.Sprintf("用户 %s", username))
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
