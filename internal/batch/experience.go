package batch

import (
	"encoding/json"

	"aigo/internal/domain"
)

func decodeExperienceSnapshot(raw string) (*domain.PersonalExperienceSnapshot, error) {
	if raw == "" {
		return nil, nil
	}
	var snapshot domain.PersonalExperienceSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, err
	}
	if err := domain.ValidatePersonalExperienceSnapshot(&snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}
