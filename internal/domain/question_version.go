package domain

import (
	"errors"
	"reflect"
	"time"
)

var (
	// ErrQuestionVersionConflict 表示调用方基于旧版本写入，或内容变化时未递增版本号。
	ErrQuestionVersionConflict = errors.New("题目版本冲突")
)

// QuestionVersion 是题目内容的不可变快照。状态和题库归属可随业务流转，
// Restore 时只恢复题目内容字段，并生成一个新的版本号。
type QuestionVersion struct {
	QuestionID string     `json:"question_id"`
	Version    int        `json:"version"`
	Snapshot   A2Question `json:"snapshot"`
	Actor      string     `json:"actor"`
	ChangeType string     `json:"change_type"`
	ChangeNote string     `json:"change_note"`
	CreatedAt  time.Time  `json:"created_at"`
}

// QuestionContentEqual 只比较需要经过版本管理的题目内容。
// 审核状态、题库归属和时间戳不属于内容版本。
func QuestionContentEqual(a, b A2Question) bool {
	a = normalizeQuestionContent(a)
	b = normalizeQuestionContent(b)
	return a.ClinicalStem == b.ClinicalStem &&
		reflect.DeepEqual(a.Options, b.Options) &&
		a.Answer == b.Answer &&
		a.Explanation == b.Explanation &&
		reflect.DeepEqual(a.SourceRefs, b.SourceRefs) &&
		reflect.DeepEqual(a.KnowledgePoints, b.KnowledgePoints) &&
		a.Difficulty == b.Difficulty &&
		a.CognitiveLevel == b.CognitiveLevel &&
		a.ExamPoints == b.ExamPoints &&
		a.OutlineCode == b.OutlineCode &&
		a.Profession == b.Profession &&
		a.System == b.System
}

func normalizeQuestionContent(q A2Question) A2Question {
	if q.Options == nil {
		q.Options = []Option{}
	}
	if q.SourceRefs == nil {
		q.SourceRefs = []SourceRef{}
	}
	if q.KnowledgePoints == nil {
		q.KnowledgePoints = []KnowledgePoint{}
	} else {
		q.KnowledgePoints = append([]KnowledgePoint(nil), q.KnowledgePoints...)
	}
	for i := range q.KnowledgePoints {
		if q.KnowledgePoints[i].Keywords == nil {
			q.KnowledgePoints[i].Keywords = []string{}
		}
	}
	return q
}

// RestoreQuestionContent 将历史快照的内容复制到当前题目，保留当前 ID、题库归属和创建时间。
func RestoreQuestionContent(current A2Question, snapshot A2Question) A2Question {
	current.ClinicalStem = snapshot.ClinicalStem
	current.Options = append([]Option(nil), snapshot.Options...)
	current.Answer = snapshot.Answer
	current.Explanation = snapshot.Explanation
	current.SourceRefs = append([]SourceRef(nil), snapshot.SourceRefs...)
	current.KnowledgePoints = append([]KnowledgePoint(nil), snapshot.KnowledgePoints...)
	current.Difficulty = snapshot.Difficulty
	current.CognitiveLevel = snapshot.CognitiveLevel
	current.ExamPoints = snapshot.ExamPoints
	current.OutlineCode = snapshot.OutlineCode
	current.Profession = snapshot.Profession
	current.System = snapshot.System
	return current
}
