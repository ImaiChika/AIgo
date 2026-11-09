package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"database/sql"
)

func (s *Store) GetModelSamplingConfig(ctx context.Context) (domain.ModelSamplingConfig, error) {
	c := domain.DefaultModelSamplingConfig()
	err := s.queryRowContext(ctx, `SELECT generation_temperature,check_temperature,revision FROM model_sampling_config WHERE singleton=TRUE`).Scan(&c.GenerationTemperature, &c.CheckTemperature, &c.Revision)
	if err == sql.ErrNoRows {
		return domain.DefaultModelSamplingConfig(), nil
	}
	return c, err
}

func (s *Store) SaveModelSamplingConfig(ctx context.Context, c domain.ModelSamplingConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var next int64
	var err error
	if c.Revision == 0 {
		err = s.queryRowContext(ctx, `INSERT INTO model_sampling_config(singleton,generation_temperature,check_temperature,revision) VALUES(TRUE,$1,$2,1) ON CONFLICT(singleton) DO NOTHING RETURNING revision`, c.GenerationTemperature, c.CheckTemperature).Scan(&next)
	} else {
		err = s.queryRowContext(ctx, `UPDATE model_sampling_config SET generation_temperature=$1,check_temperature=$2,revision=revision+1 WHERE singleton=TRUE AND revision=$3 RETURNING revision`, c.GenerationTemperature, c.CheckTemperature, c.Revision).Scan(&next)
	}
	if err == sql.ErrNoRows {
		return storage.ErrModelSamplingConflict
	}
	return err
}
