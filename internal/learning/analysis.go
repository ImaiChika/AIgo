package learning

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// CandidateJSONError keeps diagnostics bounded without retaining response text.
type CandidateJSONError struct {
	Kind   string
	Offset int64
}

func (e *CandidateJSONError) Error() string {
	return fmt.Sprintf("%v: 候选JSON格式错误", storage.ErrLearningInvalid)
}
func (e *CandidateJSONError) Unwrap() error { return storage.ErrLearningInvalid }
func candidateDecodeError(err error) error {
	e := &CandidateJSONError{Kind: "json_object"}
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		e.Kind = "json_syntax"
		e.Offset = syntax.Offset
	case errors.As(err, &shape):
		e.Kind = "json_type"
		e.Offset = shape.Offset
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		e.Kind = "unknown_field"
	}
	return e
}

func ParseCandidates(raw string) ([]domain.LearningCandidate, error) {
	if len(raw) > 96<<10 {
		return nil, fmt.Errorf("%w: 候选响应超限", storage.ErrLearningInvalid)
	}
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```json") && strings.HasSuffix(raw, "```") {
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "```json"), "```"))
	}
	var response struct {
		Candidates []domain.LearningCandidate `json:"candidates"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return nil, candidateDecodeError(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%w: 候选JSON包含额外内容", storage.ErrLearningInvalid)
	}
	if err := validateCandidates(response.Candidates, true); err != nil {
		return nil, err
	}
	return response.Candidates, nil
}

func CompareContent(original, current domain.A2Question) ([]domain.LearningFieldChange, string, string) {
	a, b := contentFields(original), contentFields(current)
	changes := []domain.LearningFieldChange{}
	for _, field := range contentFieldOrder {
		before, _ := json.Marshal(a[field])
		after, _ := json.Marshal(b[field])
		if !bytes.Equal(before, after) {
			changes = append(changes, domain.LearningFieldChange{Field: field, Before: before, After: after})
		}
	}
	first, _ := json.Marshal(a)
	last, _ := json.Marshal(b)
	return changes, hash(first), hash(last)
}

var contentFieldOrder = []string{"clinical_stem", "options", "answer", "explanation", "source_refs", "knowledge_points", "difficulty", "cognitive_level", "exam_points", "outline_code", "profession", "system"}

func contentFields(q domain.A2Question) map[string]any {
	options := q.Options
	if options == nil {
		options = []domain.Option{}
	}
	refs := q.SourceRefs
	if refs == nil {
		refs = []domain.SourceRef{}
	}
	points := append([]domain.KnowledgePoint{}, q.KnowledgePoints...)
	for i := range points {
		if points[i].Keywords == nil {
			points[i].Keywords = []string{}
		}
	}
	return map[string]any{"clinical_stem": q.ClinicalStem, "options": options, "answer": q.Answer, "explanation": q.Explanation, "source_refs": refs, "knowledge_points": points, "difficulty": q.Difficulty, "cognitive_level": q.CognitiveLevel, "exam_points": q.ExamPoints, "outline_code": q.OutlineCode, "profession": q.Profession, "system": q.System}
}
func hash(raw []byte) string       { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func payloadHash(value any) string { raw, _ := json.Marshal(value); return hash(raw) }
func bounded(s string, min, max int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= min && n <= max && !strings.ContainsRune(s, 0)
}
func allowedField(field string) bool {
	for _, name := range contentFieldOrder {
		if name == field {
			return true
		}
	}
	return false
}
func validateScope(scope domain.ExperienceScope) error {
	switch scope.Kind {
	case "all":
		if len(scope.PointIDs) != 0 || scope.Profession != "" {
			return fmt.Errorf("%w: 全部临床题范围不能同时指定考点/专业", storage.ErrLearningInvalid)
		}
	case "point":
		if len(scope.PointIDs) == 0 || len(scope.PointIDs) > 20 || scope.Profession != "" {
			return fmt.Errorf("%w: 考点范围无效", storage.ErrLearningInvalid)
		}
		seen := map[string]bool{}
		for _, id := range scope.PointIDs {
			if !bounded(id, 1, 160) || seen[id] {
				return fmt.Errorf("%w: 考点ID无效或重复", storage.ErrLearningInvalid)
			}
			seen[id] = true
		}
	case "profession":
		if !bounded(scope.Profession, 1, 100) || len(scope.PointIDs) > 0 {
			return fmt.Errorf("%w: 专业范围无效", storage.ErrLearningInvalid)
		}
	default:
		return fmt.Errorf("%w: 经验适用范围无效", storage.ErrLearningInvalid)
	}
	return nil
}
func validateRules(rules []domain.LearningRule) error {
	if len(rules) < 1 || len(rules) > 6 {
		return fmt.Errorf("%w: 每版经验须含1—6条规则", storage.ErrLearningInvalid)
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		if !bounded(rule.ID, 1, 100) || seen[rule.ID] || !bounded(rule.Text, 1, 600) || !bounded(rule.Reason, 0, 600) || !bounded(rule.Before, 0, 600) || !bounded(rule.After, 0, 600) || !allowedField(rule.Field) {
			return fmt.Errorf("%w: 经验规则字段无效或超限", storage.ErrLearningInvalid)
		}
		seen[rule.ID] = true
	}
	return nil
}
func validateCandidates(items []domain.LearningCandidate, requireThree bool) error {
	if len(items) < 1 || len(items) > 3 || requireThree && len(items) != 3 {
		return fmt.Errorf("%w: 需要三版候选", storage.ErrLearningInvalid)
	}
	ids, styles := map[string]bool{}, map[string]bool{}
	for _, candidate := range items {
		if !bounded(candidate.ID, 1, 100) || ids[candidate.ID] || styles[candidate.Style] || !bounded(candidate.Title, 1, 100) || (candidate.Style != "concise" && candidate.Style != "detailed" && candidate.Style != "checklist") {
			return fmt.Errorf("%w: 候选标识、风格或标题无效", storage.ErrLearningInvalid)
		}
		ids[candidate.ID], styles[candidate.Style] = true, true
		if err := validateScope(candidate.Scope); err != nil {
			return err
		}
		if err := validateRules(candidate.Rules); err != nil {
			return err
		}
	}
	return nil
}
func hasMedical(rules []domain.LearningRule) bool {
	for _, rule := range rules {
		if rule.MedicalChange || rule.Field == "answer" {
			return true
		}
	}
	return false
}
func contentUnchanged(a, b domain.AuthoringExperience) bool {
	return reflect.DeepEqual(a.Scope, b.Scope) && reflect.DeepEqual(a.Rules, b.Rules)
}
