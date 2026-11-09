package domain

import "fmt"

// 题目个人题库归属状态（C3）。用户可见的个人题库只有「普通」「删除」两个
// 页签；unfiled 不是第三种题库——表示尚未确认入库，仅出现在题目整理工作区
// 与过程视图。QuestionStatus 在兼容期保留原值，两个维度各自演化：
//   - QuestionStatus 表达生成/检查/审核生命周期（ai_reviewed 不因确认入库改变）；
//   - LibraryState 表达个人库归属（确认入库、修订待确认、用户删除）。
type LibraryState string

const (
	// LibraryUnfiled 尚未确认入库（刚通过首检、被修订、或从删除库恢复）。
	LibraryUnfiled LibraryState = "unfiled"
	// LibraryActive 普通题库：本人（或经审核流程）确认的当前版本。
	LibraryActive LibraryState = "active"
	// LibraryDeleted 删除题库：软删除留档，版本与审计完整保留。
	LibraryDeleted LibraryState = "deleted"
)

// 确认来源：self=0 轮本人确认；review=人工审核完成写入；legacy=历史数据迁移。
const (
	ConfirmSourceSelf   = "self"
	ConfirmSourceReview = "review"
	ConfirmSourceLegacy = "legacy"
)

// 删除原因（可区分，禁止混同）：
//   - owner_delete：用户本人删除（可恢复）；
//   - admin_delete：管理员删除；
//   - review_reject：审核驳回终态（不可恢复绕过审核）；
//   - legacy_archive：历史归档迁移。
const (
	DeleteReasonOwnerDelete    = "owner_delete"
	DeleteReasonOwnerArchive   = "owner_archive" // 旧永久归档入口，由本人操作，不可按软删除恢复
	DeleteReasonAdminDelete    = "admin_delete"
	DeleteReasonReviewReject   = "review_reject"
	DeleteReasonLegacyArchive  = "legacy_archive"
	DeleteReasonLegacyRejected = "legacy_rejected"
)

func ParseLibraryState(value string) (LibraryState, bool) {
	switch LibraryState(value) {
	case LibraryUnfiled, LibraryActive, LibraryDeleted:
		return LibraryState(value), true
	}
	return "", false
}

// InitialLibraryState 新生成题目的初始归属：首检通过前不可见，统一 unfiled。
func InitialLibraryState() LibraryState { return LibraryUnfiled }

// LibraryStateBackfill 历史状态映射（客户需求 §12.1）：
//   - published → active（confirmed_version=version，source=legacy）；
//   - ai_reviewed/reviewing/conflict/revision_required → unfiled（在途任务按旧流程结束）；
//   - rejected → deleted（legacy_rejected，终态锁）；archived → deleted（legacy_archive）；
//   - ai_draft/auto_checked → unfiled（暂存不可见语义不变）。
func LibraryStateBackfill(status QuestionStatus) (LibraryState, string) {
	switch CanonicalLifecycleStatus(status) {
	case StatusPublished:
		return LibraryActive, ConfirmSourceLegacy
	case StatusRejected:
		return LibraryDeleted, DeleteReasonLegacyRejected
	case StatusArchived:
		return LibraryDeleted, DeleteReasonLegacyArchive
	default:
		return LibraryUnfiled, ""
	}
}

// SelfConfirmable 0 轮本人确认的前置：首检通过（ai_reviewed）、未删除、
// 不在人工审核在途（reviewing/conflict 由审核路径收口，驳回为终态）。
func (q *A2Question) SelfConfirmable() error {
	if q.LibraryState == LibraryDeleted {
		return fmt.Errorf("题目已在删除题库，不能确认入库；请先恢复")
	}
	if q.Status == StatusPublished {
		return fmt.Errorf("题目已通过人工审核入库，无需再次确认")
	}
	if q.Status != StatusAIReviewed {
		switch q.Status {
		case StatusAIDraft, StatusAutoChecked:
			return fmt.Errorf("AI 首次检查未通过的题目不可见且不能入库")
		case StatusReviewing, StatusConflict:
			return fmt.Errorf("题目正在人工审核或等待决断，由审核流程收口")
		case StatusRevisionRequired:
			return fmt.Errorf("题目处于退回修改状态，请先保存修改")
		case StatusRejected:
			return fmt.Errorf("已驳回的题目为终态锁定，不能确认入库")
		}
		return fmt.Errorf("当前状态 %s 不能确认入库", q.Status)
	}
	return nil
}

// PersonallyExportable 个人普通题库导出判据（客户需求 §5.3）：
// 普通库（active）且确认版本等于当前内容版本；首检通过以 ai_reviewed/
// published 状态、合法确认来源为静态前置；调用方还须核对持久首检/审核证据。
func (q *A2Question) PersonallyExportable() bool {
	if q == nil || q.Version <= 0 || q.LibraryState != LibraryActive {
		return false
	}
	if q.Status != StatusAIReviewed && CanonicalLifecycleStatus(q.Status) != StatusPublished {
		return false
	}
	switch q.ConfirmationSource {
	case ConfirmSourceSelf, ConfirmSourceReview, ConfirmSourceLegacy:
		return q.ConfirmedVersion == q.Version
	default:
		return false
	}
}

// RestoreToUnfiled 从删除库恢复的目标状态：恢复后必须重新确认，不自动
// 恢复旧确认效力，也不触发 AI 复检。
func RestoreAllowed(reason string) bool {
	return reason == DeleteReasonOwnerDelete
}
