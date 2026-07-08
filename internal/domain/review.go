package domain

import "time"

// QuestionStatus 题目状态。
type QuestionStatus string

const (
	StatusAIDraft         QuestionStatus = "ai_draft"          // AI 草稿
	StatusAutoChecked     QuestionStatus = "auto_checked"      // 自动初评完成
	StatusReviewing       QuestionStatus = "reviewing"         // 审核中
	StatusRevisionRequired QuestionStatus = "revision_required" // 需要修改
	StatusRejected        QuestionStatus = "rejected"          // 驳回
	StatusApproved        QuestionStatus = "approved"          // 审核通过
	StatusPublished       QuestionStatus = "published"         // 进入正式题库
	StatusArchived        QuestionStatus = "archived"          // 归档
)

// Expert 专家信息。
type Expert struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Department string   `json:"department"`  // 所属科室或专业方向
	Title      string   `json:"title"`       // 职称
	Specialties []string `json:"specialties"` // 擅长知识点
	ExpertTypes []string `json:"expert_types"` // 可审核题型
	Contact    string   `json:"contact,omitempty"`
	Enabled    bool     `json:"enabled"`
}

// ReviewFlowConfig 审核流程配置。
type ReviewFlowConfig struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Subject     string         `json:"subject"`      // 适用专业
	Rounds      []RoundConfig  `json:"rounds"`       // 各轮配置
	CreatedAt   time.Time      `json:"created_at"`
}

// RoundConfig 单轮审核配置。
type RoundConfig struct {
	RoundNumber    int      `json:"round_number"`    // 第几轮
	Name           string   `json:"name"`            // 轮次名称，如"命题教师初审"
	ExpertIDs      []string `json:"expert_ids"`      // 本轮审核人 ID 列表
	RequiredCount  int      `json:"required_count"`  // 需要几位专家通过才算本轮通过，0表示全部
	CanModify      bool     `json:"can_modify"`      // 是否允许直接修改题目
	PassCondition  string   `json:"pass_condition"`  // 通过条件说明
	IsRequired     bool     `json:"is_required"`     // 是否必审
}

// ReviewTask 审核任务，一道题进入某个审核流程后生成。
type ReviewTask struct {
	ID           string         `json:"id"`
	QuestionID   string         `json:"question_id"`
	FlowID       string         `json:"flow_id"`
	CurrentRound int            `json:"current_round"`
	Status       QuestionStatus `json:"status"`
	AssignedTo   []string       `json:"assigned_to"` // 当前轮审核人 ID
	RoundResults []RoundResult  `json:"round_results"` // 各轮审核结果
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// RoundResult 记录某一轮的审核结果。
type RoundResult struct {
	RoundNumber    int                `json:"round_number"`
	Reviews        []ExpertReview     `json:"reviews"`         // 每位专家的审核
	ApprovedCount  int                `json:"approved_count"`
	RejectedCount  int                `json:"rejected_count"`
	Passed         bool               `json:"passed"`          // 本轮是否通过
}

// ExpertReview 单位专家的审核结果。
type ExpertReview struct {
	ExpertID   string         `json:"expert_id"`
	Conclusion QuestionStatus `json:"conclusion"` // approved / rejected / revision_required
	Opinion    string         `json:"opinion"`
	ReviewedAt time.Time      `json:"reviewed_at"`
}

// FlowsConfigFile 审核流程配置文件结构。
type FlowsConfigFile struct {
	Flows []ReviewFlowConfig `json:"flows"`
}

// ReviewRecord 审核记录，每轮审核留痕。
type ReviewRecord struct {
	ID             string         `json:"id"`
	TaskID         string         `json:"task_id"`
	QuestionID     string         `json:"question_id"`
	RoundNumber    int            `json:"round_number"`
	ExpertID       string         `json:"expert_id"`
	Conclusion     QuestionStatus `json:"review_status"` // approved / rejected / revision_required
	Opinion        string         `json:"opinion"`       // 审核意见
	BeforeSnapshot string         `json:"before_snapshot,omitempty"` // 修改前内容快照
	AfterSnapshot  string         `json:"after_snapshot,omitempty"`  // 修改后内容快照
	CreatedAt      time.Time      `json:"created_at"`
}

// QuestionVersion 题目版本记录。
type QuestionVersion struct {
	ID         string    `json:"id"`
	QuestionID string    `json:"question_id"`
	Version    int       `json:"version"`
	Snapshot   string    `json:"snapshot"`   // 题目 JSON 快照
	ChangeNote string    `json:"change_note"`
	ChangedBy  string    `json:"changed_by"` // 专家 ID 或 "ai"
	CreatedAt  time.Time `json:"created_at"`
}
