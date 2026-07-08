package image

import (
	"context"
	"testing"

	"aigo/internal/domain"
)

func TestMockImageGenerator(t *testing.T) {
	gen := NewMockImageGenerator("/tmp/test_images")
	prompt := domain.ImagePrompt{
		ID:         "test-prompt-1",
		QuestionID: "test-q-1",
		Purpose:    "执业医师考试A2型题配图",
		ImageType:  "医学教学示意图",
		Subject:    "右下肺炎症影",
		MustInclude: []string{"右下肺区域", "炎症阴影"},
		MustExclude: []string{"真实患者信息"},
		Style:       "医学教材示意图",
		ReviewFocus: "病变位置是否正确",
	}

	images, err := gen.Generate(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}

	img := images[0]
	if img.ID == "" {
		t.Error("image ID is empty")
	}
	if img.ImagePath == "" {
		t.Error("image path is empty")
	}
	if img.ModelName != "mock-image-generator" {
		t.Errorf("unexpected model name: %s", img.ModelName)
	}
	if img.Status != domain.ImageStatusPending {
		t.Errorf("unexpected status: %s", img.Status)
	}

	t.Logf("Generated: ID=%s, Path=%s", img.ID, img.ImagePath)
}
