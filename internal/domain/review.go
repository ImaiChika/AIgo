package domain

import "time"

// QuestionStatus 题目生命周期状态。
type QuestionStatus string

const (
	StatusAIDraft          QuestionStatus = "ai_draft"           // AI 草稿（刚生成）
	StatusAutoChecked      QuestionStatus = "auto_checked"       // 自动初评完成
	StatusReviewing        QuestionStatus = "reviewing"          // 审核中
	StatusRevisionRequired QuestionStatus = "revision_required"  // 需要修改
	StatusRejected         QuestionStatus = "rejected"           // 驳回
	StatusApproved         QuestionStatus = "approved"           // 审核通过
	StatusPublished        QuestionStatus = "published"          // 进入正式题库
	StatusArchived         QuestionStatus = "archived"           // 归档
)

// Expert 专家信息。
type Expert struct {
	ID          string   `json:"id"`           // 专家唯一标识
	Name        string   `json:"name"`         // 姓名
	Department  string   `json:"department"`   // 所属科室或专业方向
	Title       string   `json:"title"`        // 职称
	Specialties []string `json:"specialties"`  // 擅长知识点
	ExpertTypes []string `json:"expert_types"` // 可审核题型
	Contact     string   `json:"contact,omitempty"` // 联系方式
	Enabled     bool     `json:"enabled"`      // 是否启用
}

// ReviewFlowConfig 审核流程配置。
// 管理员可配置多个流程，每个流程包含多轮审核。
type ReviewFlowConfig struct {
	ID          string         `json:"id"`          // 流程唯一标识
	Name        string         `json:"name"`        // 流程名称
	Description string         `json:"description,omitempty"` // 描述
	Subject     string         `json:"subject"`     // 适用专业
	Rounds      []RoundConfig  `json:"rounds"`      // 各轮配置
	CreatedAt   time.Time      `json:"created_at"`  // 创建时间
}

// RoundConfig 单轮审核配置。
type RoundConfig struct {
	RoundNumber   int      `json:"round_number"`   // 第几轮
	Name          string   `json:"name"`           // 轮次名称（如"命题教师初审"）
	ExpertIDs     []string `json:"expert_ids"`     // 本轮审核人 ID 列表
	RequiredCount int      `json:"required_count"` // 需要几位通过才算本轮通过（0=全部）
	CanModify     bool     `json:"can_modify"`     // 是否允许直接修改题目
	PassCondition string   `json:"pass_condition"` // 通过条件说明
	IsRequired    bool     `json:"is_required"`    // 是否必审
}

// ReviewTask 审核任务。
// 一道题提交到审核流程后生成一个任务，跟踪各轮审核进度。
type ReviewTask struct {
	ID           string         `json:"id"`            // 任务唯一标识
	QuestionID   string         `json:"question_id"`   // 关联的题目 ID
	FlowID       string         `json:"flow_id"`       // 使用的审核流程 ID
	CurrentRound int            `json:"current_round"` // 当前轮次
	Status       QuestionStatus `json:"status"`        // 任务状态
	AssignedTo   []string       `json:"assigned_to"`   // 当前轮审核人 ID
	RoundResults []RoundResult  `json:"round_results"` // 各轮审核结果
	CreatedAt    time.Time      `json:"created_at"`    // 创建时间
	UpdatedAt    time.Time      `json:"updated_at"`    // 更新时间
}

// RoundResult 记录某一轮的审核结果。
type RoundResult struct {
	RoundNumber   int            `json:"round_number"`   // 轮次
	Reviews       []ExpertReview `json:"reviews"`        // 每位专家的审核
	ApprovedCount int            `json:"approved_count"` // 通过数
	RejectedCount int            `json:"rejected_count"` // 驳回数
	Passed        bool           `json:"passed"`         // 本轮是否通过
}

// ExpertReview 单位专家的审核结果。
type ExpertReview struct {
	ExpertID   string         `json:"expert_id"`   // 审核人 ID
	Conclusion QuestionStatus `json:"conclusion"`  // 审核结论
	Opinion    string         `json:"opinion"`     // 审核意见
	ReviewedAt time.Time      `json:"reviewed_at"` // 审核时间
}

// FlowsConfigFile 审核流程配置文件结构，对应 review_flows.json。
type FlowsConfigFile struct {
	Flows []ReviewFlowConfig `json:"flows"`
}

// ReviewRecord 审核记录，每轮审核留痕，不可修改。
type ReviewRecord struct {
	ID             string         `json:"id"`                       // 记录唯一标识
	TaskID         string         `json:"task_id"`                  // 关联的审核任务 ID
	QuestionID     string         `json:"question_id"`              // 关联的题目 ID
	RoundNumber    int            `json:"round_number"`             // 第几轮
	ExpertID       string         `json:"expert_id"`                // 审核人 ID
	Conclusion     QuestionStatus `json:"review_status"`            // 审核结论
	Opinion        string         `json:"opinion"`                  // 审核意见
	BeforeSnapshot string         `json:"before_snapshot,omitempty"` // 修改前快照
	AfterSnapshot  string         `json:"after_snapshot,omitempty"`  // 修改后快照
	CreatedAt      time.Time      `json:"created_at"`               // 审核时间
}

// QuestionVersion 题目版本记录。
// 每次题目内容变更都保存一个快照，用于追溯修改历史。
type QuestionVersion struct {
	ID         string    `json:"id"`          // 版本记录 ID
	QuestionID string    `json:"question_id"` // 题目 ID
	Version    int       `json:"version"`     // 版本号
	Snapshot   string    `json:"snapshot"`    // 题目 JSON 快照
	ChangeNote string    `json:"change_note"` // 变更说明
	ChangedBy  string    `json:"changed_by"`  // 修改人（"ai" 或专家 ID）
	CreatedAt  time.Time `json:"created_at"`  // 创建时间
}
