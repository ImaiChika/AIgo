package domain

import "time"

type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

type KnowledgePoint struct {
	ID         string   `json:"id"`
	Subject    string   `json:"subject"`     // 科目，如"临床医学"
	System     string   `json:"system"`      // 系统，如"呼吸内科"
	Topic      string   `json:"topic"`       // 知识点名称，如"肺炎"
	OutlineRef string   `json:"outline_ref"` // 大纲编号
	Keywords   []string `json:"keywords"`    // 关键词
}

type A2Question struct {
	ID              string           `json:"id"`
	ClinicalStem    string           `json:"clinical_stem"`
	Options         []Option         `json:"options"`
	Answer          string           `json:"answer"`
	Explanation     string           `json:"explanation"`
	SourceRefs      []SourceRef      `json:"source_refs"`
	KnowledgePoints []KnowledgePoint `json:"knowledge_points"`
	MediaRefs       []MediaRef       `json:"media_refs"`
	Difficulty      Difficulty       `json:"difficulty"`
	Status          QuestionStatus   `json:"status"`
	Version         int              `json:"version"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type Option struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

type SourceRef struct {
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
	Note  string `json:"note,omitempty"`
}

type MediaKind string

const (
	MediaKindImage       MediaKind = "image"
	MediaKindMedicalScan MediaKind = "medical_scan"
	MediaKindDocument    MediaKind = "document"
)

type MediaRef struct {
	ID          string            `json:"id"`
	Kind        MediaKind         `json:"kind"`
	URI         string            `json:"uri"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type GenerationRequest struct {
	Subject         string           `json:"subject"`
	Difficulty      Difficulty       `json:"difficulty"`
	KnowledgePoints []KnowledgePoint `json:"knowledge_points"`
	MediaRefs       []MediaRef       `json:"media_refs"`
	Count           int              `json:"count"`
}

type EvaluationReport struct {
	QuestionID      string    `json:"question_id"`
	ScientificScore int       `json:"scientific_score"`
	LogicScore      int       `json:"logic_score"`
	A2FitScore      int       `json:"a2_fit_score"`
	DifficultyHint  string    `json:"difficulty_hint"`
	Issues          []string  `json:"issues"`
	CreatedAt       time.Time `json:"created_at"`
}
