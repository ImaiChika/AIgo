// Package image 提供图片相关的业务服务。
// 包括结构化生图提示词生成、候选图生成和图片审核。
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
)

// ImageGenerator 外部生图模型接口。
type ImageGenerator interface {
	Generate(ctx context.Context, prompt domain.ImagePrompt) ([]domain.GeneratedImage, error)
}

// Service 图片服务。
type Service struct {
	questionStore storage.QuestionStore // 题目存储
	imageStore    storage.ImageStore    // 图片存储
	llmClient     llm.Client            // LLM 客户端（用于生成提示词）
	imgGen        ImageGenerator        // 生图模型
}

// NewService 创建图片服务实例。
func NewService(questionStore storage.QuestionStore, imageStore storage.ImageStore, llmClient llm.Client, imgGen ImageGenerator) *Service {
	return &Service{
		questionStore: questionStore,
		imageStore:    imageStore,
		llmClient:     llmClient,
		imgGen:        imgGen,
	}
}

// GeneratePrompt 为题目生成结构化生图提示词。
// 调用千问分析题干，输出图片用途、类型、主题、必须出现/不能出现的要素等。
func (s *Service) GeneratePrompt(ctx context.Context, questionID string) (*domain.ImagePrompt, error) {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, fmt.Errorf("题目 %s 不存在", questionID)
	}

	// 检查是否已有提示词
	existing, _ := s.imageStore.GetPromptByQuestionID(ctx, questionID)
	if existing != nil {
		return existing, nil
	}

	// 构建知识点文本
	kpText := ""
	if len(q.KnowledgePoints) > 0 {
		kpText = q.KnowledgePoints[0].Topic
	}

	// 调千问生成结构化提示词
	prompt := fmt.Sprintf(`你是一个医学考试配图提示词专家。
根据以下A2型试题，生成一张医学教学示意图的结构化提示词。

题目：%s
正确答案：%s
知识点：%s

请输出严格JSON，字段如下：
{
  "purpose": "执业医师考试A2型题配图",
  "image_type": "医学教学示意图（如：解剖图/病理图/影像图/流程图/心电图）",
  "subject": "图片的医学主题，简短描述",
  "must_include": ["必须出现的医学要素1", "要素2", "要素3"],
  "must_exclude": ["不能出现的要素1", "要素2"],
  "style": "风格要求，如：医学教材示意图，清晰简洁，非照片",
  "review_focus": "审核重点，如：病变位置是否正确"
}

只输出JSON，不要其他文字。`, q.ClinicalStem, q.Answer, kpText)

	raw, err := s.llmClient.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: "你是医学考试配图提示词专家，只输出严格JSON。"},
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: 0.3, MaxTokens: 500})
	if err != nil {
		return nil, fmt.Errorf("生成提示词失败: %w", err)
	}

	// 解析 JSON
	var parsed struct {
		Purpose     string   `json:"purpose"`
		ImageType   string   `json:"image_type"`
		Subject     string   `json:"subject"`
		MustInclude []string `json:"must_include"`
		MustExclude []string `json:"must_exclude"`
		Style       string   `json:"style"`
		ReviewFocus string   `json:"review_focus"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("解析提示词JSON失败: %w", err)
	}

	imagePrompt := domain.ImagePrompt{
		ID:             fmt.Sprintf("prompt-%s-%d", questionID, time.Now().UnixNano()),
		QuestionID:     questionID,
		Purpose:        parsed.Purpose,
		ImageType:      parsed.ImageType,
		Subject:        parsed.Subject,
		MustInclude:    parsed.MustInclude,
		MustExclude:    parsed.MustExclude,
		Style:          parsed.Style,
		KnowledgePoint: kpText,
		ReviewFocus:    parsed.ReviewFocus,
		CreatedAt:      time.Now(),
	}

	if err := s.imageStore.SavePrompt(ctx, imagePrompt); err != nil {
		return nil, err
	}

	return &imagePrompt, nil
}

// GenerateImages 为题目生成候选图。
func (s *Service) GenerateImages(ctx context.Context, questionID string, count int) ([]domain.GeneratedImage, error) {
	prompt, err := s.imageStore.GetPromptByQuestionID(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if prompt == nil {
		return nil, fmt.Errorf("题目 %s 尚未生成提示词，请先运行 img-prompt", questionID)
	}

	if count < 1 {
		count = 1
	}
	if count > 5 {
		count = 5
	}

	// 调用生图模型
	var images []domain.GeneratedImage
	for i := 0; i < count; i++ {
		imgs, err := s.imgGen.Generate(ctx, *prompt)
		if err != nil {
			return images, fmt.Errorf("第%d张生成失败: %w", i+1, err)
		}
		images = append(images, imgs...)
	}

	// 保存到数据库
	for i := range images {
		images[i].PromptID = prompt.ID
		images[i].QuestionID = questionID
		images[i].Status = domain.ImageStatusPending
		if err := s.imageStore.SaveImage(ctx, images[i]); err != nil {
			return images, err
		}
	}

	return images, nil
}

// ListImages 列出题目的所有候选图。
func (s *Service) ListImages(ctx context.Context, questionID string) ([]domain.GeneratedImage, error) {
	return s.imageStore.ListImagesByQuestionID(ctx, questionID)
}

// GetImageByID 按 ID 获取候选图（权限校验用）。
func (s *Service) GetImageByID(ctx context.Context, imageID string) (*domain.GeneratedImage, error) {
	return s.imageStore.GetImage(ctx, imageID)
}

// ReviewImage 审核候选图（通过/驳回）。
func (s *Service) ReviewImage(ctx context.Context, imageID string, expertID string, action domain.ImageStatus, opinion string) error {
	img, err := s.imageStore.GetImage(ctx, imageID)
	if err != nil {
		return err
	}
	if img == nil {
		return fmt.Errorf("图片 %s 不存在", imageID)
	}
	if action != domain.ImageStatusApproved && action != domain.ImageStatusRejected {
		return fmt.Errorf("无效的审核动作: %s", action)
	}

	// 更新图片状态
	if err := s.imageStore.UpdateImageStatus(ctx, imageID, action, opinion); err != nil {
		return err
	}

	// 保存审核记录
	record := domain.ImageReviewRecord{
		ID:         fmt.Sprintf("imgrec-%s-%s-%d", imageID, expertID, time.Now().UnixNano()),
		ImageID:    imageID,
		ExpertID:   expertID,
		Conclusion: action,
		Opinion:    opinion,
		CreatedAt:  time.Now(),
	}
	return s.imageStore.SaveReviewRecord(ctx, record)
}

// GetPrompt 获取题目的生图提示词。
func (s *Service) GetPrompt(ctx context.Context, questionID string) (*domain.ImagePrompt, error) {
	return s.imageStore.GetPromptByQuestionID(ctx, questionID)
}
