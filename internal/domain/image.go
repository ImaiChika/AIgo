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
// 由千问根据题目内容自动生成，包含图片用途、类型、主题、必须出现/不能出现的要素等。
type ImagePrompt struct {
	ID             string   `json:"id"`              // 提示词唯一标识
	QuestionID     string   `json:"question_id"`     // 关联的题目 ID
	Purpose        string   `json:"purpose"`         // 图片用途（如"执业医师考试A2型题配图"）
	ImageType      string   `json:"image_type"`      // 图片类型（教学示意图/流程图/解剖图）
	Subject        string   `json:"subject"`         // 医学主题（如"右下肺炎症影"）
	MustInclude    []string `json:"must_include"`    // 必须出现的要素
	MustExclude    []string `json:"must_exclude"`    // 不能出现的要素
	Style          string   `json:"style"`           // 风格要求
	KnowledgePoint string   `json:"knowledge_point"` // 对应知识点
	ReviewFocus    string   `json:"review_focus"`    // 审核重点
	CreatedAt      time.Time `json:"created_at"`     // 创建时间
}

// GeneratedImage AI 生成的候选图。
type GeneratedImage struct {
	ID           string      `json:"id"`            // 候选图唯一标识
	PromptID     string      `json:"prompt_id"`     // 关联的提示词 ID
	QuestionID   string      `json:"question_id"`   // 关联的题目 ID
	ImagePath    string      `json:"image_path"`    // 图片本地路径
	ModelName    string      `json:"model_name"`    // 生图模型名称
	ModelVersion string      `json:"model_version"` // 模型版本
	Status       ImageStatus `json:"status"`        // 审核状态
	ReviewNote   string      `json:"review_note"`   // 审核意见
	CreatedAt    time.Time   `json:"created_at"`    // 创建时间
}

// ImageReviewRecord 图片审核记录。
type ImageReviewRecord struct {
	ID         string      `json:"id"`          // 记录唯一标识
	ImageID    string      `json:"image_id"`    // 被审核的图片 ID
	ExpertID   string      `json:"expert_id"`   // 审核人 ID
	Conclusion ImageStatus `json:"conclusion"`  // 审核结论
	Opinion    string      `json:"opinion"`     // 审核意见
	CreatedAt  time.Time   `json:"created_at"`  // 审核时间
}
