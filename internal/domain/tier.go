package domain

// QuestionTier 题库分层：按题目生命周期把题目划分到三个逻辑题库。
// 分层由审核状态唯一决定（TierForStatus），不单独落库，避免状态与分层不一致。
//
//   - 正式题库（formal）：通过全部审核环节并决断入库（published）的定稿题。
//     只有正式题库可导出文档；除自动入库、权限者删除/撤回外，其他功能不得再操作。
//   - 待审核题库（working）：AI 检查通过后进入送审、退回修改等流程的题目；
//     检查前的暂存态（ai_draft/auto_checked）对用户不可见，不算入本层可见集合。
//   - 淘汰题库（eliminated）：终态留档题（驳回锁定、归档）。
//     AI 检查不通过的题目会被物理删除并留淘汰档案（AICheckDiscard），不进入任何题库。
type QuestionTier string

const (
	TierFormal     QuestionTier = "formal"     // 正式题库
	TierWorking    QuestionTier = "working"    // 过程题库
	TierEliminated QuestionTier = "eliminated" // 淘汰题库
)

// TierNames 分层中文名（前端展示与错误提示统一用语）。
var TierNames = map[QuestionTier]string{
	TierFormal:     "正式题库",
	TierWorking:    "待审核题库",
	TierEliminated: "淘汰题库",
}

// Name 返回分层中文名。
func (t QuestionTier) Name() string {
	if n, ok := TierNames[t]; ok {
		return n
	}
	return string(t)
}

// IsStagingStatus 判断题目是否处于 AI 检查前的暂存态。
// 暂存态题目（ai_draft 草稿、遗留 auto_checked）对全部用户界面不可见：
// 仅在 AI 质量检查通过（ai_reviewed）后才进入可见题库；检查不通过会被
// 物理删除并留淘汰档案。产品语义：客户只能看到检查通过的题目。
func IsStagingStatus(status QuestionStatus) bool {
	return status == StatusAIDraft || status == StatusAutoChecked
}

// TierForStatus 按审核状态推导题目所属题库分层。
// 唯一事实来源：所有分层过滤、权限判断都必须经由本函数，禁止各处自行维护状态清单。
// 注意：暂存态（ai_draft/auto_checked）仍归 working——供内部流转（检查、恢复）
// 使用；但 TierStatuses(working) 不包含它们，即暂存题不出现在任何用户可见查询里。
func TierForStatus(status QuestionStatus) QuestionTier {
	switch CanonicalLifecycleStatus(status) {
	case StatusPublished:
		return TierFormal
	case StatusAIDraft, StatusAutoChecked, StatusAIReviewed,
		StatusReviewing, StatusConflict, StatusRevisionRequired:
		return TierWorking
	case StatusRejected, StatusArchived:
		return TierEliminated
	default:
		return TierWorking
	}
}

// TierStatuses 返回分层在用户可见查询中的成员状态。
// working 分层刻意不含暂存态（ai_draft/auto_checked）：待检题目对用户不可见，
// 通过 AI 检查变为 ai_reviewed 后才随本分层出现（2026-09-17 产品口径）。
func TierStatuses(tier QuestionTier) []QuestionStatus {
	switch tier {
	case TierFormal:
		return []QuestionStatus{StatusPublished}
	case TierEliminated:
		return []QuestionStatus{StatusRejected, StatusArchived}
	default:
		return []QuestionStatus{
			StatusAIReviewed,
			StatusReviewing, StatusConflict, StatusRevisionRequired,
		}
	}
}

// ParseTier 校验并解析分层标识（API 参数用）；合法返回分层，非法返回 false。
func ParseTier(s string) (QuestionTier, bool) {
	switch QuestionTier(s) {
	case TierFormal, TierWorking, TierEliminated:
		return QuestionTier(s), true
	}
	return "", false
}

// Tier 返回题目当前所属题库分层。
func (q *A2Question) Tier() QuestionTier {
	return TierForStatus(q.Status)
}
