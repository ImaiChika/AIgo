package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"github.com/lib/pq"
)

func scanAuthoringExperience(row rowScanner) (*domain.AuthoringExperience, error) {
	var raw []byte
	var owner string
	err := row.Scan(&raw, &owner)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var card domain.AuthoringExperience
	if err = json.Unmarshal(raw, &card); err != nil {
		return nil, err
	}
	card.OwnerID = owner
	return &card, nil
}
func (s *Store) ListAuthoringExperiences(ctx context.Context, owner string, includeArchived bool) ([]domain.AuthoringExperience, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	rows, err := s.queryContext(ctx, `SELECT body,owner_id FROM personal_experiences WHERE owner_id=$1 AND ($2 OR status<>'archived') ORDER BY updated_at DESC,id LIMIT 500`, owner, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cards := []domain.AuthoringExperience{}
	for rows.Next() {
		card, e := scanAuthoringExperience(rows)
		if e != nil {
			return nil, e
		}
		cards = append(cards, *card)
	}
	return cards, rows.Err()
}
func (s *Store) GetAuthoringExperience(ctx context.Context, owner, id string) (*domain.AuthoringExperience, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	return scanAuthoringExperience(s.queryRowContext(ctx, `SELECT body,owner_id FROM personal_experiences WHERE owner_id=$1 AND id=$2`, owner, id))
}
func (s *Store) FindExperienceBySource(ctx context.Context, owner, job, candidate string) (*domain.AuthoringExperience, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	// Source-key replay and insertion share one short transaction. Serialize
	// duplicate browser requests before reading, rather than returning an
	// avoidable uniqueness conflict after two callers both observed no record.
	if _, err := s.execContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "personal-knowledge:"+owner+"\x1f"+job+"\x1f"+candidate); err != nil {
		return nil, err
	}
	return scanAuthoringExperience(s.queryRowContext(ctx, `SELECT body,owner_id FROM personal_experiences WHERE owner_id=$1 AND source_job_id=$2 AND source_candidate_id=$3`, owner, job, candidate))
}
func (s *Store) SaveAuthoringExperience(ctx context.Context, card domain.AuthoringExperience, expected int) error {
	if err := learningOwner(ctx, card.OwnerID); err != nil {
		return err
	}
	if card.Kind != "authoring_experience" || card.ScopeType != "personal" || (card.Status != "draft" && card.Status != "active" && card.Status != "archived") || card.Revision != expected+1 {
		return storage.ErrLearningInvalid
	}
	if expected == 0 && card.Source.Kind == "manual" {
		source := card.Source
		key := strings.TrimPrefix(source.CandidateID, "manual:")
		hash, e := hex.DecodeString(source.OriginRequestHash)
		if !strings.HasPrefix(source.CandidateID, "manual:") || strings.TrimSpace(key) != key || len([]rune(key)) < 1 || len([]rune(key)) > 120 || e != nil || len(hash) != 32 || source.JobID != "" || source.QuestionID != "" || source.OriginalVersion != 0 || source.CurrentVersion != 0 || source.OriginalHash != "" || source.CurrentHash != "" || source.OriginID != "" || source.OriginType != "" || source.OriginOrder != 0 || source.OriginDeletedAt != nil {
			return storage.ErrLearningInvalid
		}
	} else if expected == 0 {
		if card.Source.Kind != "" && card.Source.Kind != "learning" {
			return storage.ErrLearningInvalid
		}
		if card.Source.OriginID != "" || card.Source.OriginType != "" || card.Source.OriginOrder != 0 || card.Source.OriginDeletedAt != nil || card.Source.OriginRequestHash != "" {
			return storage.ErrLearningInvalid
		}
		job, err := s.GetLearningJob(ctx, card.OwnerID, card.Source.JobID)
		if err != nil {
			return err
		}
		if job == nil || job.Status != "succeeded" {
			return storage.ErrLearningConflict
		}
		found := false
		for _, candidate := range job.Candidates {
			if candidate.ID == card.Source.CandidateID {
				found = true
			}
		}
		if !found || card.Source.QuestionID != job.QuestionID || card.Source.OriginalVersion != job.OriginalVersion || card.Source.CurrentVersion != job.CurrentVersion || card.Source.OriginalHash != job.OriginalHash || card.Source.CurrentHash != job.CurrentHash {
			return storage.ErrLearningConflict
		}
		source, err := s.LoadLearningSource(ctx, card.OwnerID, job.QuestionID)
		if err != nil {
			return err
		}
		if !validLearningSource(source) || job.Source == nil || job.Source.Original == nil || job.Source.Current == nil || source.Current.Version != job.CurrentVersion ||
			!domain.QuestionContentEqual(*source.Original, *job.Source.Original) || !domain.QuestionContentEqual(*source.Current, *job.Source.Current) {
			return storage.ErrLearningConflict
		}
	} else {
		old, err := s.GetAuthoringExperience(ctx, card.OwnerID, card.ID)
		if err != nil {
			return err
		}
		if old == nil {
			return storage.ErrLearningNotFound
		}
		if old.Revision != expected || !reflect.DeepEqual(old.Source, card.Source) {
			return storage.ErrLearningConflict
		}
		card.CreatedAt = old.CreatedAt
	}
	if card.CreatedAt.IsZero() {
		card.CreatedAt = time.Now().UTC()
	}
	if card.UpdatedAt.IsZero() {
		card.UpdatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(card)
	if err != nil {
		return err
	}
	var result sql.Result
	if expected == 0 {
		result, err = s.execContext(ctx, `INSERT INTO personal_experiences(id,owner_id,kind,scope_type,revision,status,source_job_id,source_candidate_id,body,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, card.ID, card.OwnerID, card.Kind, card.ScopeType, card.Revision, card.Status, card.Source.JobID, card.Source.CandidateID, raw, card.CreatedAt, card.UpdatedAt)
	} else {
		result, err = s.execContext(ctx, `UPDATE personal_experiences SET revision=$3,status=$4,body=$5,updated_at=$6 WHERE owner_id=$1 AND id=$2 AND revision=$7`, card.OwnerID, card.ID, card.Revision, card.Status, raw, card.UpdatedAt, expected)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storage.ErrLearningConflict
	}
	if _, err = s.execContext(ctx, `INSERT INTO personal_experience_versions(owner_id,experience_id,revision,body) VALUES($1,$2,$3,$4)`, card.OwnerID, card.ID, card.Revision, raw); err != nil {
		return err
	}
	return s.SaveLog(ctx, domain.AuditLog{ID: fmt.Sprintf("experience-%s-%d", card.ID, card.Revision), Action: "personal_experience_" + card.Status, Actor: card.OwnerID, Detail: fmt.Sprintf("experience=%s revision=%d", card.ID, card.Revision), CreatedAt: card.UpdatedAt})
}
func (s *Store) ListAuthoringExperienceVersions(ctx context.Context, owner, id string) ([]domain.AuthoringExperience, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	rows, err := s.queryContext(ctx, `SELECT body,owner_id FROM personal_experience_versions WHERE owner_id=$1 AND experience_id=$2 ORDER BY revision DESC LIMIT 100`, owner, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.AuthoringExperience{}
	for rows.Next() {
		card, e := scanAuthoringExperience(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, *card)
	}
	return result, rows.Err()
}
func (s *Store) GetLearningImportOperation(ctx context.Context, owner, key string) (*storage.LearningImportOperation, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	var operation storage.LearningImportOperation
	var raw []byte
	err := s.queryRowContext(ctx, `SELECT owner_id,import_key,request_hash,job_id,experience_ids,created_at FROM learning_import_operations WHERE owner_id=$1 AND import_key=$2`, owner, key).Scan(&operation.OwnerID, &operation.ImportKey, &operation.RequestHash, &operation.JobID, &raw, &operation.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &operation.ExperienceIDs); err != nil {
		return nil, err
	}
	return &operation, nil
}
func (s *Store) SaveLearningImportOperation(ctx context.Context, operation storage.LearningImportOperation) error {
	if err := learningOwner(ctx, operation.OwnerID); err != nil {
		return err
	}
	raw, err := json.Marshal(operation.ExperienceIDs)
	if err != nil {
		return err
	}
	if operation.ImportKey == "" || operation.RequestHash == "" || len(operation.ExperienceIDs) == 0 {
		return storage.ErrLearningInvalid
	}
	result, err := s.execContext(ctx, `INSERT INTO learning_import_operations(owner_id,import_key,request_hash,job_id,experience_ids) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, operation.OwnerID, operation.ImportKey, operation.RequestHash, operation.JobID, raw)
	if err != nil {
		var pg *pq.Error
		if errors.As(err, &pg) && pg.Code == "23505" {
			return storage.ErrLearningConflict
		}
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storage.ErrLearningConflict
	}
	return nil
}
