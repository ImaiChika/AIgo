package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"aigo/internal/domain"
)

func (s *Store) saveQuestionPersonalExperiences(ctx context.Context, executor sqlExecer, q domain.A2Question) error {
	snapshot := q.PersonalExperiences
	if snapshot == nil {
		return nil
	}
	if snapshot.Version != domain.ExperienceProtocolVersion || snapshot.OwnerID == "" || snapshot.OwnerID != q.OwnerID || len(snapshot.Entries) == 0 || len(snapshot.Entries) > 5 {
		return fmt.Errorf("题目个人经验冻结依据无效")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO question_experience_contexts(question_id,owner_id,snapshot) VALUES($1,$2,$3)`, q.ID, q.OwnerID, raw)
	return err
}

func (s *Store) GetQuestionPersonalExperiences(ctx context.Context, id string) (*domain.PersonalExperienceSnapshot, error) {
	var raw []byte
	err := s.queryRowContext(ctx, `SELECT snapshot FROM question_experience_contexts WHERE question_id=$1`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot domain.PersonalExperienceSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}
