// Package multimodal 定义多模态（OCR、图像理解、医学图片生成）的接口。
// 当前为 stub 实现，预留架构扩展点。
// 真实的多模态能力需要接入外部服务（如千问 VL、PaddleOCR 等）。
package multimodal

import (
	"context"

	"aigo/internal/domain"
)

// OCRResult OCR 识别结果。
type OCRResult struct {
	Text       string            `json:"text"`        // 识别出的文本
	MediaRef   domain.MediaRef   `json:"media_ref"`   // 原始素材引用
	Confidence float64           `json:"confidence"`  // 置信度（0-1）
	Metadata   map[string]string `json:"metadata,omitempty"` // 附加信息
}

// ImageFeature 图像特征提取结果。
type ImageFeature struct {
	MediaRef    domain.MediaRef   `json:"media_ref"`    // 原始素材引用
	Description string            `json:"description"`  // 图像描述
	Labels      []string          `json:"labels,omitempty"` // 标签列表
	VectorRef   string            `json:"vector_ref,omitempty"` // 向量引用（用于检索）
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// OCRService OCR 识别接口。
type OCRService interface {
	ExtractText(ctx context.Context, media domain.MediaRef) (OCRResult, error)
}

// ImageFeatureExtractor 图像特征提取接口。
type ImageFeatureExtractor interface {
	ExtractFeature(ctx context.Context, media domain.MediaRef) (ImageFeature, error)
}

// MedicalImageGenerator 医学图片生成接口。
type MedicalImageGenerator interface {
	GenerateIllustration(ctx context.Context, prompt string) (domain.MediaRef, error)
}
