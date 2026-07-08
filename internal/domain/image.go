package domain

import "time"

// ImageStatus 图片审核状态。
type ImageStatus string

const (
	ImageStatusPending  ImageStatus = "pending"  // 待审核
	ImageStatusApproved ImageStatus = "approved"  // 审核通过
	ImageStatusRejected ImageStatus = "rejected"  // 驳回
)

// ImagePrompt 结构化生图提示词。
type ImagePrompt struct {
	ID             string   `json:"id"`
	QuestionID     string   `json:"question_id"`
	Purpose        string   `json:"purpose"`         // 图片用途
	ImageType      string   `json:"image_type"`      // 图片类型：教学示意图/流程图/解剖图
	Subject        string   `json:"subject"`          // 医学主题
	MustInclude    []string `json:"must_include"`     // 必须出现的要素
	MustExclude    []string `json:"must_exclude"`     // 不能出现的要素
	Style          string   `json:"style"`            // 风格要求
	KnowledgePoint string   `json:"knowledge_point"`  // 对应知识点
	ReviewFocus    string   `json:"review_focus"`     // 审核重点
	CreatedAt      time.Time `json:"created_at"`
}

// GeneratedImage AI 生成的候选图。
type GeneratedImage struct {
	ID           string      `json:"id"`
	PromptID     string      `json:"prompt_id"`
	QuestionID   string      `json:"question_id"`
	ImagePath    string      `json:"image_path"`     // 图片本地路径
	ModelName    string      `json:"model_name"`     // 生图模型名称
	ModelVersion string      `json:"model_version"`  // 模型版本
	Status       ImageStatus `json:"status"`
	ReviewNote   string      `json:"review_note"`    // 审核意见
	CreatedAt    time.Time   `json:"created_at"`
}

// ImageReviewRecord 图片审核记录。
type ImageReviewRecord struct {
	ID         string      `json:"id"`
	ImageID    string      `json:"image_id"`
	ExpertID   string      `json:"expert_id"`
	Conclusion ImageStatus `json:"conclusion"` // approved / rejected
	Opinion    string      `json:"opinion"`
	CreatedAt  time.Time   `json:"created_at"`
}
