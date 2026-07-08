package multimodal

import (
	"context"

	"aigo/internal/domain"
)

type OCRResult struct {
	Text       string            `json:"text"`
	MediaRef   domain.MediaRef   `json:"media_ref"`
	Confidence float64           `json:"confidence"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type ImageFeature struct {
	MediaRef    domain.MediaRef   `json:"media_ref"`
	Description string            `json:"description"`
	Labels      []string          `json:"labels,omitempty"`
	VectorRef   string            `json:"vector_ref,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type OCRService interface {
	ExtractText(ctx context.Context, media domain.MediaRef) (OCRResult, error)
}

type ImageFeatureExtractor interface {
	ExtractFeature(ctx context.Context, media domain.MediaRef) (ImageFeature, error)
}

type MedicalImageGenerator interface {
	GenerateIllustration(ctx context.Context, prompt string) (domain.MediaRef, error)
}
