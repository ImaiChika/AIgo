// Package domain 定义 AIgo 系统的核心领域类型。
// 包括题目、知识点、选项、多模态素材、生成请求、评估报告等数据结构。
package domain

import "time"

// Difficulty 题目难度等级。
type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"   // 简单
	DifficultyMedium Difficulty = "medium" // 中等
	DifficultyHard   Difficulty = "hard"   // 困难
)

// KnowledgePoint 知识点，对应考试大纲中的一个考点。
type KnowledgePoint struct {
	ID         string   `json:"id"`          // 知识点唯一标识
	Subject    string   `json:"subject"`     // 科目，如"临床医学"
	System     string   `json:"system"`      // 所属系统，如"呼吸内科"、"心内科"
	Topic      string   `json:"topic"`       // 知识点名称，如"社区获得性肺炎"
	OutlineRef string   `json:"outline_ref"` // 考试大纲编号
	Keywords   []string `json:"keywords"`    // 关键词列表，用于搜索
}

// A2Question A2 型单选题（临床情境题）。
// 国家执业医师考试标准题型：给出临床情境，要求选择最佳诊断或处理。
type A2Question struct {
	ID              string           `json:"id"`               // 题目唯一标识
	ClinicalStem    string           `json:"clinical_stem"`    // 临床情境题干
	Options         []Option         `json:"options"`          // 选项列表（A-E）
	Answer          string           `json:"answer"`           // 正确答案标签（A/B/C/D/E）
	Explanation     string           `json:"explanation"`      // 答案解析（可选）
	SourceRefs      []SourceRef      `json:"source_refs"`      // 解析来源引用（可选）
	KnowledgePoints []KnowledgePoint `json:"knowledge_points"` // 关联的知识点
	MediaRefs       []MediaRef       `json:"media_refs"`       // 关联的多模态素材（图片、影像等）
	Difficulty      Difficulty       `json:"difficulty"`       // 难度等级
	Status          QuestionStatus   `json:"status"`           // 审核状态
	Version         int              `json:"version"`          // 版本号，每次修改递增
	CreatedAt       time.Time        `json:"created_at"`       // 创建时间
	UpdatedAt       time.Time        `json:"updated_at"`       // 最后更新时间
}

// Option 题目选项。
type Option struct {
	Label string `json:"label"` // 选项标签（A/B/C/D/E）
	Text  string `json:"text"`  // 选项内容
}

// SourceRef 来源引用，记录解析所依据的教材、指南或文献。
type SourceRef struct {
	Title string `json:"title"`           // 来源标题
	URL   string `json:"url,omitempty"`   // 来源链接（可选）
	Note  string `json:"note,omitempty"`  // 备注（可选）
}

// MediaKind 多模态素材类型。
type MediaKind string

const (
	MediaKindImage       MediaKind = "image"        // 普通图片
	MediaKindMedicalScan MediaKind = "medical_scan" // 医学影像（CT、MRI等）
	MediaKindDocument    MediaKind = "document"     // 文档
)

// MediaRef 多模态素材引用。
type MediaRef struct {
	ID          string            `json:"id"`                    // 素材唯一标识
	Kind        MediaKind         `json:"kind"`                  // 素材类型
	URI         string            `json:"uri"`                   // 素材地址（本地路径或URL）
	Description string            `json:"description,omitempty"` // 素材描述
	Metadata    map[string]string `json:"metadata,omitempty"`    // 附加元数据
}

// GenerationRequest 题目生成请求，发送给千问大模型。
type GenerationRequest struct {
	Subject         string           `json:"subject"`          // 专业或科目
	Difficulty      Difficulty       `json:"difficulty"`       // 期望难度
	KnowledgePoints []KnowledgePoint `json:"knowledge_points"` // 关联知识点
	MediaRefs       []MediaRef       `json:"media_refs"`       // 可选的多模态素材
	Count           int              `json:"count"`            // 生成数量
}

// EvaluationReport 题目质量评估报告。
type EvaluationReport struct {
	QuestionID      string    `json:"question_id"`      // 被评估的题目 ID
	ScientificScore int       `json:"scientific_score"` // 科学性得分
	LogicScore      int       `json:"logic_score"`      // 逻辑一致性得分
	A2FitScore      int       `json:"a2_fit_score"`     // A2 题型匹配度得分
	DifficultyHint  string    `json:"difficulty_hint"`  // 难度提示
	Issues          []string  `json:"issues"`           // 发现的问题列表
	CreatedAt       time.Time `json:"created_at"`       // 评估时间
}
