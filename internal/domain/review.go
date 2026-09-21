package domain

import (
	"errors"
	"strings"
	"time"
)

// ErrReviewNotSubmittable 题目当前状态不允许送审（未通过 AI 检查、终态锁定、已在审核中等）。
// 属于客户端可纠正的状态冲突，API 层应映射为 409 而不是 500。
var ErrReviewNotSubmittable = errors.New("题目当前状态不允许提交审核")

// ErrReviewBadRequest 送审请求本身不合法。
// API 层应映射为 400。
var ErrReviewBadRequest = errors.New("送审请求不合法")

// QuestionStatus 题目生命周期状态。
type QuestionStatus string

const (
	StatusAIDraft          QuestionStatus = "ai_draft"          // AI 草稿（刚生成）
	StatusAutoChecked      QuestionStatus = "auto_checked"      // 自动初评完成
	StatusReviewing        QuestionStatus = "reviewing"         // 审核中
	StatusConflict         QuestionStatus = "conflict"          // 所有轮次通过，待轮外最终把关人决断
	StatusRevisionRequired QuestionStatus = "revision_required" // 需要修改
	StatusRejected         QuestionStatus = "rejected"          // 驳回
	StatusApproved         QuestionStatus = "approved"          // 审核动作/结论：通过（不能作为题目或任务终态）
	StatusAIReviewed       QuestionStatus = "ai_reviewed"       // AI 检查通过
	StatusPublished        QuestionStatus = "published"         // 题目/任务唯一成功终态，前端显示“已通过”
	StatusArchived         QuestionStatus = "archived"          // 归档
)

// CanonicalLifecycleStatus 将旧版生命周期状态 approved 收口为 published。
// 仅用于题目和审核任务状态；审核动作与审核记录中的 approved 必须保留。
func CanonicalLifecycleStatus(status QuestionStatus) QuestionStatus {
	if status == StatusApproved {
		return StatusPublished
	}
	return status
}

func IsPassedLifecycleStatus(status QuestionStatus) bool {
	return CanonicalLifecycleStatus(status) == StatusPublished
}

// Expert 专家信息。
type Expert struct {
	ID          string   `json:"id"`                // 专家唯一标识
	Name        string   `json:"name"`              // 姓名
	Department  string   `json:"department"`        // 所属科室或专业方向
	Title       string   `json:"title"`             // 职称
	Specialties []string `json:"specialties"`       // 擅长知识点
	ExpertTypes []string `json:"expert_types"`      // 可审核题型
	Contact     string   `json:"contact,omitempty"` // 联系方式
	Enabled     bool     `json:"enabled"`           // 是否启用
}

// ReviewFlowConfig 审核流程配置。
// 管理员可配置多个流程，每个流程包含多轮审核。
// BankID 仅保留历史数据结构兼容；新流程必须为空。FinalReviewerIDs 为最终把关管理员列表。
// VoteRule 投票规则："" = 达到通过票数即过轮（默认）；"veto" = 本轮收齐意见后须全员通过，否则按驳回/需修改结果结束本轮。
type ReviewFlowConfig struct {
	ID               string        `json:"id"`                           // 流程唯一标识
	Name             string        `json:"name"`                         // 流程名称
	Description      string        `json:"description,omitempty"`        // 描述
	Subject          string        `json:"subject"`                      // 适用专业（兼容旧字段）
	BankID           string        `json:"bank_id,omitempty"`            // 历史兼容字段；新流程不使用
	FinalReviewerIDs []string      `json:"final_reviewer_ids,omitempty"` // 最终把关管理员（空=任意有最终把关权限者）
	VoteRule         string        `json:"vote_rule,omitempty"`          // 投票规则（""/veto）
	Rounds           []RoundConfig `json:"rounds"`                       // 各轮配置
	Archived         bool          `json:"archived,omitempty"`           // 已删除归档，仅供历史任务沿用原流程
	CreatedAt        time.Time     `json:"created_at"`                   // 创建时间
}

// RoundConfig 单轮审核配置。
// 每轮必须显式选择至少一名当前启用且有审题权限的用户。
type RoundConfig struct {
	RoundNumber   int      `json:"round_number"`   // 第几轮
	Name          string   `json:"name"`           // 轮次名称（如"命题教师初审"）
	ExpertIDs     []string `json:"expert_ids"`     // 本轮审核人 ID 列表（必填）
	RequiredCount int      `json:"required_count"` // 需要几位通过（达标立即过轮；未达标收齐意见后裁定；0=全部）
	CanModify     bool     `json:"can_modify"`     // 是否允许直接修改题目
	PassCondition string   `json:"pass_condition"` // 通过条件说明
	IsRequired    bool     `json:"is_required"`    // 是否必审
}

// ReviewTask 审核任务。
// 一道题提交到审核流程后生成一个任务，跟踪各轮审核进度。
// Attempt 为当前提交批次号：仅退回修改后重新送审时递增（驳回为锁定终态），
// 历史批次的审核记录永久留痕，决断对比视图只展示当前批次。
type ReviewTask struct {
	ID                 string         `json:"id"`                       // 任务唯一标识
	QuestionID         string         `json:"question_id"`              // 关联的题目 ID
	FlowID             string         `json:"flow_id"`                  // 使用的审核流程 ID
	FlowName           string         `json:"flow_name,omitempty"`      // 流程显示名（读取时由服务层回填，不落库）
	SubmissionBankID   string         `json:"submission_bank_id"`       // 历史兼容快照；新任务为空
	CurrentRound       int            `json:"current_round"`            // 当前轮次
	Status             QuestionStatus `json:"status"`                   // 任务状态
	AssignedTo         []string       `json:"assigned_to"`              // 当前轮审核人 ID
	FinalReviewerIDs   []string       `json:"final_reviewer_ids"`       // 最终把关管理员快照
	FinalDecision      *ExpertReview  `json:"final_decision,omitempty"` // 最终把关决断
	QuestionPrevStatus QuestionStatus `json:"question_prev_status"`     // 提交前题目状态（撤销时恢复用）
	QuestionVersion    int            `json:"question_version"`         // 提交时绑定的题目内容版本
	Attempt            int            `json:"attempt"`                  // 当前提交批次号（从1起）
	RoundResults       []RoundResult  `json:"round_results"`            // 各轮审核结果
	CreatedAt          time.Time      `json:"created_at"`               // 创建时间
	UpdatedAt          time.Time      `json:"updated_at"`               // 更新时间
}

// RoundResult 记录某一轮的审核结果。
type RoundResult struct {
	RoundNumber   int            `json:"round_number"`   // 轮次
	Reviews       []ExpertReview `json:"reviews"`        // 每位专家的审核
	ApprovedCount int            `json:"approved_count"` // 通过数
	RejectedCount int            `json:"rejected_count"` // 驳回数
	RevisionCount int            `json:"revision_count"` // 需修改数
	Passed        bool           `json:"passed"`         // 本轮是否通过
}

// ExpertReview 单位专家的审核结果。
type ExpertReview struct {
	ExpertID   string         `json:"expert_id"`             // 审核人 ID
	ExpertName string         `json:"expert_name,omitempty"` // 审核人显示名快照
	Conclusion QuestionStatus `json:"conclusion"`            // 审核结论
	Opinion    string         `json:"opinion"`               // 审核意见（结构化评语的拼接文本，兼容旧展示）
	Comment    *ReviewComment `json:"comment,omitempty"`     // 结构化评语
	ReviewedAt time.Time      `json:"reviewed_at"`           // 审核时间
}

// ReviewComment 结构化评语：按题目部位分栏填写，便于多位专家横向对比。
// 通过时可不填；驳回/需修改时至少一栏非空（由审核服务强制校验）。
type ReviewComment struct {
	Stem    string `json:"stem,omitempty"`    // 题干部分意见
	Options string `json:"options,omitempty"` // 选项部分意见
	Answer  string `json:"answer,omitempty"`  // 答案与解析部分意见
	Other   string `json:"other,omitempty"`   // 其他意见
}

// IsEmpty 判断结构化评语是否全部为空白。
func (c *ReviewComment) IsEmpty() bool {
	if c == nil {
		return true
	}
	return strings.TrimSpace(c.Stem) == "" &&
		strings.TrimSpace(c.Options) == "" &&
		strings.TrimSpace(c.Answer) == "" &&
		strings.TrimSpace(c.Other) == ""
}

// SectionLabels 结构化评语各栏的中文标签（顺序即展示顺序）。
var SectionLabels = []struct {
	Key  string
	Name string
}{
	{"stem", "题干"},
	{"options", "选项"},
	{"answer", "答案与解析"},
	{"other", "其他"},
}

// Flatten 把结构化评语拼接为纯文本，用于旧版自由文本意见字段的兼容展示。
func (c *ReviewComment) Flatten() string {
	if c.IsEmpty() {
		return ""
	}
	parts := make([]string, 0, len(SectionLabels))
	for _, s := range SectionLabels {
		v := ""
		switch s.Key {
		case "stem":
			v = strings.TrimSpace(c.Stem)
		case "options":
			v = strings.TrimSpace(c.Options)
		case "answer":
			v = strings.TrimSpace(c.Answer)
		case "other":
			v = strings.TrimSpace(c.Other)
		}
		if v != "" {
			parts = append(parts, "【"+s.Name+"】"+v)
		}
	}
	return strings.Join(parts, "\n")
}

// FlowsConfigFile 审核流程配置文件结构，对应 review_flows.json。
type FlowsConfigFile struct {
	Flows []ReviewFlowConfig `json:"flows"`
}

// ReviewRecord 审核记录，每轮审核留痕，不可修改。
// Attempt 标记记录所属的提交批次，跨批次留痕互不混淆。
type ReviewRecord struct {
	ID          string         `json:"id"`                // 记录唯一标识
	TaskID      string         `json:"task_id"`           // 关联的审核任务 ID
	QuestionID  string         `json:"question_id"`       // 关联的题目 ID
	RoundNumber int            `json:"round_number"`      // 第几轮
	Attempt     int            `json:"attempt"`           // 所属提交批次号
	ExpertID    string         `json:"expert_id"`         // 审核人 ID
	ExpertName  string         `json:"expert_name"`       // 审核人显示名快照
	Conclusion  QuestionStatus `json:"review_status"`     // 审核结论
	Opinion     string         `json:"opinion"`           // 审核意见（结构化评语的拼接文本，兼容旧展示）
	Comment     *ReviewComment `json:"comment,omitempty"` // 结构化评语（历史记录可能为空）
	CreatedAt   time.Time      `json:"created_at"`        // 审核时间
}

// AuditLog 操作日志，记录所有对题库的变更操作。
type AuditLog struct {
	ID         string    `json:"id"`          // 日志 ID
	QuestionID string    `json:"question_id"` // 关联题目 ID（可为空，如批量导入）
	Action     string    `json:"action"`      // 操作类型：create/update/delete/review/publish
	Actor      string    `json:"actor"`       // 操作人（用户名或 "ai"）
	Detail     string    `json:"detail"`      // 操作详情（JSON 或文字描述）
	CreatedAt  time.Time `json:"created_at"`  // 操作时间
}

// AuditActionStat 操作行为的出现次数统计，供日志页的行为筛选下拉使用。
type AuditActionStat struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// ===== AI 检查相关类型 =====

// AIReviewResult AI 检查结果。
type AIReviewResult struct {
	ID              string        `json:"id"`
	QuestionID      string        `json:"question_id"`
	QuestionVersion int           `json:"question_version"` // 检查时的题目版本号（用于判断结果是否过期）
	Verdict         string        `json:"verdict"`          // "pass" / "issues_found" / "reject"
	Scores          ReviewScores  `json:"scores"`
	Issues          []ReviewIssue `json:"issues"`
	Suggestion      string        `json:"suggestion"`   // AI 的修改建议
	Model           string        `json:"model"`        // 使用的模型
	RawResponse     string        `json:"raw_response"` // 原始 LLM 响应
	CreatedAt       time.Time     `json:"created_at"`
}

// ReviewScores AI 检查各维度评分。
type ReviewScores struct {
	Scientific int `json:"scientific"` // 科学性 0-100
	Logic      int `json:"logic"`      // 逻辑性 0-100
	A2Fit      int `json:"a2_fit"`     // A2 适配度 0-100
	Answer     int `json:"answer"`     // 答案准确性 0-100
}

// ReviewIssue AI 检查发现的问题。
type ReviewIssue struct {
	Field    string `json:"field"`    // "stem" / "options" / "answer" / "explanation"
	Severity string `json:"severity"` // "error" / "warning" / "info"
	Message  string `json:"message"`
}

// AI 检查任务状态：持久化队列的生命周期。
const (
	AICheckTaskPending   = "pending"   // 待执行（含等待退避重试）
	AICheckTaskRunning   = "running"   // 执行中（持有租约）
	AICheckTaskSucceeded = "succeeded" // 已完成（结果见 ai_review_results）
	AICheckTaskExhausted = "exhausted" // 重试耗尽，最终失败
)

// AICheckTask AI 检查任务记录（持久化队列，见 ai_check_tasks 表）。
// 任务是"过程"，检查结论落在 AIReviewResult；题目粒度的重试与进度以任务为准。
type AICheckTask struct {
	ID              string    `json:"id"`
	QuestionID      string    `json:"question_id"`
	QuestionVersion int       `json:"question_version"` // 入队时题目版本（观测用）
	Status          string    `json:"status"`           // pending / running / succeeded / exhausted
	Attempts        int       `json:"attempts"`         // 已执行次数（抢占时递增）
	MaxAttempts     int       `json:"max_attempts"`     // 最大执行次数，超过即 exhausted
	LastError       string    `json:"last_error,omitempty"`
	LeasedUntil     time.Time `json:"leased_until,omitempty"` // running 租约到期时间；pending 态复用为退避重试时间
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// AICheckDiscard AI 检查淘汰记录：首次检查不通过的题目自动删除后保留的淘汰原因，
// 供生成页向出题人展示"本次生成具体情况"。题目本身已物理删除，不入题库。
type AICheckDiscard struct {
	ID          string        `json:"id"`
	QuestionID  string        `json:"question_id"`
	Verdict     string        `json:"verdict"` // issues_found / reject
	Scores      ReviewScores  `json:"scores"`
	Issues      []ReviewIssue `json:"issues"`
	Suggestion  string        `json:"suggestion"`
	Model       string        `json:"model"`
	StemSummary string        `json:"stem_summary"` // 题干摘要（题目已删除，供辨认）
	CreatedAt   time.Time     `json:"created_at"`
	// OwnerID 淘汰题的归属人（题目已删除，用于淘汰详情的越权校验）；
	// 旧记录无此字段（空串）时沿用历史行为：登录即可查看摘要与原因。
	OwnerID string `json:"-"`
	// Question 淘汰时的完整题目快照（含题干、选项、答案、解析），
	// 供前端"查看原题"使用；旧记录未留档，为 nil。
	Question *A2Question `json:"question,omitempty"`
}
