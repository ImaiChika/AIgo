// Package domain 定义 AIgo 系统的核心领域类型。
// 包括题目、知识点、选项、生成请求、评估报告等数据结构。
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
	ID          string   `json:"id"` // 跨版本唯一标识，不等同于大纲代码
	VersionID   string   `json:"version_id,omitempty"`
	VersionName string   `json:"version_name,omitempty"` // 出题时随知识点一起保存快照
	VersionYear int      `json:"version_year,omitempty"`
	Revision    int      `json:"revision,omitempty"`
	Category    string   `json:"category"`     // 分类：基础医学 / 临床综合
	Subject     string   `json:"subject"`      // 专业/系统，如"病理"、"呼吸系统"
	Unit        string   `json:"unit"`         // 单元，如"二、局部血液循环障碍"
	SubItem     string   `json:"sub_item"`     // 细目，如"1．充血和淤血"
	Topic       string   `json:"topic"`        // 要点，如"（1）充血的概念和类型"
	OutlineCode string   `json:"outline_code"` // 大纲代码，如"110.2.6.2.1.1"
	Keywords    []string `json:"keywords"`     // 关键词列表，用于搜索（兼容旧字段）
}

// A2Question A2 型单选题（临床情境题）。
// 国家执业医师考试标准题型：给出临床情境，要求选择最佳诊断或处理。
type A2Question struct {
	ID              string           `json:"id"`               // 题目唯一标识
	ClinicalStem    string           `json:"clinical_stem"`    // 临床情境题干
	Options         []Option         `json:"options"`          // 选项列表（A-E）
	Answer          string           `json:"answer"`           // 正确答案标签（A/B/C/D/E）
	Explanation     string           `json:"explanation"`      // 答案解析
	SourceRefs      []SourceRef      `json:"source_refs"`      // 解析来源引用（可选）
	KnowledgePoints []KnowledgePoint `json:"knowledge_points"` // 关联的知识点
	Difficulty      Difficulty       `json:"difficulty"`       // 难度等级（0-1，如"0.65"）
	CognitiveLevel  string           `json:"cognitive_level"`  // 认知层次：记忆/理解/简单应用/综合应用
	ExamPoints      string           `json:"exam_points"`      // 考核要点，如"诊断与鉴别诊断，临床表现"
	OutlineCode     string           `json:"outline_code"`     // 大纲代码（标到最后一级）
	Profession      string           `json:"profession"`       // 专业，如"消化"、"生理，消化"
	System          string           `json:"system"`           // 系统，如"消化系统"
	BankIDs         []string         `json:"bank_ids"`         // 所属题库列表（一道题可属于多个题库，空=未分类）
	Status          QuestionStatus   `json:"status"`           // 审核状态
	Version         int              `json:"version"`          // 版本号，每次修改递增
	CreatedBy       string           `json:"-"`                // 生成者（仅审计/内部追溯，题目接口不向前端暴露）
	OwnerID         string           `json:"-"`                // 个人题库归属用户 ID（仅服务端范围过滤）
	CreatedAt       time.Time        `json:"created_at"`       // 创建时间
	UpdatedAt       time.Time        `json:"updated_at"`       // 最后更新时间
}

// QuestionShareStatus 是个人题目进入全局题库的单次申请状态。
// 状态同时决定全局题库分层：pending=待审核、approved=正式、rejected=淘汰。
type QuestionShareStatus string

const (
	QuestionSharePending  QuestionShareStatus = "pending"
	QuestionShareApproved QuestionShareStatus = "approved"
	QuestionShareRejected QuestionShareStatus = "rejected"
)

// QuestionShareRequest 个人题目分享申请。一个 question_id 只能有一条记录，
// 申请一旦审批即结束，不能重复申请或重新提交。
type QuestionShareRequest struct {
	ID         string              `json:"id"`
	QuestionID string              `json:"question_id"`
	OwnerID    string              `json:"-"`
	Status     QuestionShareStatus `json:"status"`
	ReviewedBy string              `json:"-"`
	ReviewNote string              `json:"review_note,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
	ReviewedAt *time.Time          `json:"reviewed_at,omitempty"`
}

// Option 题目选项。
type Option struct {
	Label string `json:"label"` // 选项标签（A/B/C/D/E）
	Text  string `json:"text"`  // 选项内容
}

// SourceRef 来源引用，记录解析所依据的教材、指南或文献。
type SourceRef struct {
	Title string `json:"title"`          // 来源标题
	URL   string `json:"url,omitempty"`  // 来源链接（可选）
	Note  string `json:"note,omitempty"` // 备注（可选）
}

// GenerationRequest 题目生成请求，发送给千问大模型。
type GenerationRequest struct {
	Subject         string           `json:"subject"`          // 专业或科目
	Difficulty      Difficulty       `json:"difficulty"`       // 期望难度
	KnowledgePoints []KnowledgePoint `json:"knowledge_points"` // 关联知识点
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
