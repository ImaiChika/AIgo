package domain

import "time"

// 内置角色 ID。super_admin 是系统唯一的最高权限身份，不能通过普通角色
// 或直接权限模拟；admin 是由超级管理员分配的可下放管理员身份。
const (
	RoleSuperAdmin = "super_admin"
	RoleAdmin      = "admin"
	RoleExpert     = "expert"
	RoleTeacher    = "teacher"
)

// IsSuperAdminRole 判断角色是否为系统唯一的超级管理员角色。
func IsSuperAdminRole(role string) bool {
	return role == RoleSuperAdmin
}

// 权限点定义。权限分两类：
//   - 全局权限：作用于整个系统，无题库范围
//   - 题库权限：可限定作用题库（bank scope），空范围=全部题库
//
// 用户有效权限 = 角色模板权限 ∪ 直接分配权限；用户 bank_ids 作为所有题库范围权限的
// 统一资源边界（空范围=全部题库）。审题/决断题目的可见性由任务分配另行控制。

const (
	// ===== 全局权限 =====
	PermUserManage   = "user:manage"      // 用户与权限管理
	PermRoleManage   = "role:manage"      // 角色模板管理
	PermBankManage   = "bank:manage"      // 分类子题库管理（建库/改库）
	PermFlowManage   = "flow:manage"      // 审核流程配置
	PermExpertManage = "expert:manage"    // 专家库管理
	PermAuditView    = "audit:view"       // 操作日志查看
	PermBatchRun     = "batch:run"        // 批量推理（独立于单题出题）
	PermKnowledgeMng = "knowledge:manage" // 知识点导入/增删

	// ===== 题库范围权限 =====
	PermQuestionView        = "question:view"         // 查看题目列表/详情（过程题库）
	PermQuestionEdit        = "question:edit"         // 编辑题目
	PermQuestionDelete      = "question:delete"       // 删除题目（过程/淘汰题库）
	PermQuestionGenerate    = "question:generate"     // 单题 AI 出题
	PermQuestionDownload    = "question:download"     // 题库下载/导出（仅正式题库）
	PermQuestionShare       = "question:share"        // 个人正式题目申请分享至全局题库
	PermQuestionViewGlobal  = "question:view_global"  // 查看全局题库三层逻辑视图
	PermQuestionShareReview = "question:share_review" // 审批个人题目分享至全局题库
	PermReviewDo            = "review:do"             // 执行审核（题库范围仅用于自动分配任务）
	PermReviewSubmit        = "review:submit"         // 出题老师提交本人题目进入审核流程
	PermReviewFinal         = "review:final"          // 最终把关（按任务/把关人名单授权，不授予题库浏览）
	PermReviewResults       = "review:view_results"   // 查看审核记录汇总（不等同于个人题库查看）
	PermStatsView           = "stats:view"            // 统计分析

	// ===== 题库分层权限 =====
	// 分层由题目状态决定（TierForStatus）：published=正式题库，
	// 流程中状态=过程题库，rejected/archived=淘汰题库。
	PermQuestionViewFormal     = "question:view_formal"     // 查看正式题库（published 定稿题）
	PermQuestionViewEliminated = "question:view_eliminated" // 查看淘汰题库（驳回/归档留档）
	PermQuestionDeleteFormal   = "question:delete_formal"   // 删除正式题库题目
	// PermQuestionViewAll 查看全部题库：跨出题人查看所有人的题目（含历史无归属题）。
	// 未持有该权限时，题库可见性 = 本人题目 ∪ 直接分配题库范围（bank_ids）内的题目；
	// 超级管理员默认具备，下放管理员需显式授予。
	PermQuestionViewAll = "question:view_all"
)

// PermissionMeta 权限点元数据（名称、分组、是否题库范围）。
type PermissionMeta struct {
	Code      string `json:"code"`       // 权限点标识
	Name      string `json:"name"`       // 中文名
	Group     string `json:"group"`      // 分组
	BankScope bool   `json:"bank_scope"` // 是否可按题库限定范围
}

// AllPermissions 全部可分配权限点（供权限矩阵 UI 和角色模板使用）。
func AllPermissions() []PermissionMeta {
	all := []PermissionMeta{
		// 题库权限
		{PermQuestionView, "查看题目", "题库", true},
		{PermQuestionEdit, "编辑题目", "题库", true},
		{PermQuestionDelete, "删除题目", "题库", true},
		{PermQuestionGenerate, "单题出题", "命题", true},
		{PermBatchRun, "批量推理", "命题", false},
		{PermQuestionDownload, "题库下载", "题库", true},
		{PermQuestionShare, "申请分享至全局题库", "题库", false},
		{PermQuestionViewGlobal, "查看全局题库", "题库", false},
		{PermQuestionShareReview, "审批全局题库分享", "题库", false},
		{PermQuestionViewFormal, "查看正式题库", "题库", true},
		{PermQuestionDeleteFormal, "删除正式题库题目", "题库", true},
		{PermQuestionViewEliminated, "查看淘汰题库", "题库", true},
		{PermQuestionViewAll, "查看全部题库", "题库", false},
		{PermReviewDo, "审题", "审核", true},
		{PermReviewSubmit, "提交审核", "审核", false},
		{PermReviewFinal, "最终把关", "审核", false},
		{PermReviewResults, "查看审核记录", "审核", false},
		{PermStatsView, "数据统计", "题库", true},
		// 全局权限
		{PermUserManage, "用户管理", "系统", false},
		{PermRoleManage, "角色管理", "系统", false},
		{PermBankManage, "分类子题库管理", "系统", false},
		{PermFlowManage, "审核流程", "系统", false},
		{PermExpertManage, "专家库管理", "系统", false},
		{PermAuditView, "操作日志", "系统", false},
		{PermKnowledgeMng, "知识点管理", "系统", false},
	}
	return all
}

// IsValidPermission 校验权限点是否合法。
func IsValidPermission(code string) bool {
	for _, p := range AllPermissions() {
		if p.Code == code {
			return true
		}
	}
	return false
}

// TierViewPerm 返回查看指定题库分层所需的权限点。
// 过程题库沿用 question:view；正式/淘汰题库各有独立查看权限。
func TierViewPerm(tier QuestionTier) string {
	switch tier {
	case TierFormal:
		return PermQuestionViewFormal
	case TierEliminated:
		return PermQuestionViewEliminated
	default:
		return PermQuestionView
	}
}

// Role 角色模板。用户通过 role 字段引用角色，继承动作权限；若动作支持题库范围，
// 仍受该用户 bank_ids 的统一边界约束。
// 管理员可自定义任意数量的角色，避免身份硬编码。
type Role struct {
	ID          string    `json:"id"`          // 角色唯一标识（如 admin/expert/teacher/custom-xxx）
	Name        string    `json:"name"`        // 角色显示名
	Description string    `json:"description"` // 描述
	Permissions []string  `json:"permissions"` // 权限点列表
	IsBuiltin   bool      `json:"is_builtin"`  // 是否内置角色
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// QuestionBank 分类子题库。它是专业组织、权限范围与批量送审单位，不代表题目生命周期分层。
// Professions 为题库的专业范围：题目 profession 匹配时可被自动归纳进库。
type QuestionBank struct {
	ID          string    `json:"id"`          // 题库唯一标识
	Name        string    `json:"name"`        // 题库名，如"内科题库"
	Description string    `json:"description"` // 描述
	Professions []string  `json:"professions"` // 专业范围（自动归纳规则，空=仅手动）
	CreatedAt   time.Time `json:"created_at"`
}
