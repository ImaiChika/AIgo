package learning

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// Only identifiers are untyped and program-owned. Embedded semantic fields use
// the original domain types and strict decoder, without rewriting provider JSON.
type programRule struct {
	domain.LearningRule
	ID json.RawMessage `json:"id"`
}
type programCandidate struct {
	domain.LearningCandidate
	ID    json.RawMessage `json:"id"`
	Rules []programRule   `json:"rules"`
}

func parseCandidatesForJob(raw string, job *domain.LearningJob) ([]domain.LearningCandidate, error) {
	if job.OutputPolicy != domain.LearningProgramOutput {
		return ParseCandidates(raw)
	}
	if len(raw) > 96<<10 {
		return nil, fmt.Errorf("%w: 候选响应超限", storage.ErrLearningInvalid)
	}
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```json") && strings.HasSuffix(raw, "```") {
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "```json"), "```"))
	}
	var response struct {
		Candidates []programCandidate `json:"candidates"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return nil, candidateDecodeError(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%w: 候选JSON包含额外内容", storage.ErrLearningInvalid)
	}
	items := make([]domain.LearningCandidate, 0, len(response.Candidates))
	for _, wire := range response.Candidates {
		item := wire.LearningCandidate
		item.ID = item.Style
		item.Rules = nil
		for j, rule := range wire.Rules {
			value := rule.LearningRule
			value.ID = fmt.Sprintf("%s-r%d", item.Style, j+1)
			item.Rules = append(item.Rules, value)
		}
		items = append(items, item)
	}
	if err := validateCandidates(items, true); err != nil {
		return nil, err
	}
	return items, nil
}
