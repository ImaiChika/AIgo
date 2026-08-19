package domain

import "time"

// 权限点定义。权限分两类：
//   - 全局权限：作用于整个系统，无题库范围
//   - 题库权限：可限定作用题库（bank scope），空范围=全部题库
//
// 用户有效权限 = 角色模板权限（全范围）∪ 直接分配权限（带题库范围）。

const (
	// ===== 全局权限 =====
	PermUserManage    = "user:manage"      // 用户与权限管理
	PermRoleManage    = "role:manage"      // 角色模板管理
	PermBankManage    = "bank:manage"      // 题库管理（建库/改库）
	PermFlowManage    = "flow:manage"      // 审核流程配置
	PermExpertManage  = "expert:manage"    // 专家库管理
	PermAuditView     = "audit:view"       // 操作日志查看
	PermAICheck       = "ai:check"         // AI 质量检查
	PermBatchRun      = "batch:run"        // 批量推理
	PermKnowledgeMng  = "knowledge:manage" // 知识点导入/增删
	PermImageGenerate = "image:generate"   // 配图提示词/生图
	PermImageReview   = "image:review"     // 配图审核

	// ===== 题库范围权限 =====
	PermQuestionView     = "question:view"     // 查看题目列表/详情
	PermQuestionCreate   = "question:create"   // 手工新建题目
	PermQuestionEdit     = "question:edit"     // 编辑题目
	PermQuestionDelete   = "question:delete"   // 删除题目
	PermQuestionGenerate = "question:generate" // AI 出题
	PermQuestionDownload = "question:download" // 题库下载/导出
	PermReviewDo         = "review:do"         // 执行审核（审题）
	PermReviewFinal      = "review:final"      // 最终把关（决断冲突、发布入库）
	PermStatsView        = "stats:view"        // 统计分析
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
		{PermQuestionCreate, "新建题目", "题库", true},
		{PermQuestionEdit, "编辑题目", "题库", true},
		{PermQuestionDelete, "删除题目", "题库", true},
		{PermQuestionGenerate, "AI 出题", "题库", true},
		{PermQuestionDownload, "题库下载", "题库", true},
		{PermReviewDo, "审题", "审核", true},
		{PermReviewFinal, "最终把关", "审核", true},
		{PermStatsView, "统计分析", "题库", true},
		// 全局权限
		{PermUserManage, "用户管理", "系统", false},
		{PermRoleManage, "角色管理", "系统", false},
		{PermBankManage, "题库管理", "系统", false},
		{PermFlowManage, "审核流程配置", "系统", false},
		{PermExpertManage, "专家库管理", "系统", false},
		{PermAuditView, "操作日志", "系统", false},
		{PermAICheck, "AI 检查", "系统", false},
		{PermBatchRun, "批量推理", "系统", false},
		{PermKnowledgeMng, "知识点管理", "系统", false},
		{PermImageGenerate, "配图生成", "系统", false},
		{PermImageReview, "配图审核", "系统", false},
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

// Role 角色模板。用户通过 role 字段引用角色，继承其权限（全范围）。
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

// QuestionBank 题库（分库）。题目归属某个题库，权限可按题库限定范围。
// Professions 为题库的专业范围：题目 profession 匹配时可被自动归纳进库。
type QuestionBank struct {
	ID          string    `json:"id"`          // 题库唯一标识
	Name        string    `json:"name"`        // 题库名，如"内科题库"
	Description string    `json:"description"` // 描述
	Professions []string  `json:"professions"` // 专业范围（自动归纳规则，空=仅手动）
	CreatedAt   time.Time `json:"created_at"`
}
