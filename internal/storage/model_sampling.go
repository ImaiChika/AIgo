package storage

import (
	"aigo/internal/domain"
	"context"
	"errors"
)

var ErrModelSamplingConflict = errors.New("调用参数已被其他窗口修改，请重新读取后保存")

type ModelSamplingStore interface {
	GetModelSamplingConfig(context.Context) (domain.ModelSamplingConfig, error)
	SaveModelSamplingConfig(context.Context, domain.ModelSamplingConfig) error
}

func FreezeGenerationSampling(ctx context.Context, store any, snapshot *domain.GenerationPromptSnapshot) (*domain.GenerationPromptSnapshot, error) {
	if snapshot == nil {
		snapshot = &domain.GenerationPromptSnapshot{}
	}
	config := domain.DefaultModelSamplingConfig()
	if s, ok := store.(ModelSamplingStore); ok {
		var err error
		config, err = s.GetModelSamplingConfig(ctx)
		if err != nil {
			return nil, err
		}
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	value := config.GenerationTemperature
	snapshot.GenerationTemperature = &value
	return snapshot, nil
}
