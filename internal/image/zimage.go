package image

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"aigo/internal/domain"
)

// ZImageConfig 通义万相生图配置。
type ZImageConfig struct {
	APIKey  string // 阿里云百炼 API Key
	BaseURL string // 接口地址
	OutputDir string // 图片保存目录
}

// ZImageGenerator 通义万相生图器。
type ZImageGenerator struct {
	cfg        ZImageConfig
	httpClient *http.Client
}

// NewZImageGenerator 创建通义万相生图器。
func NewZImageGenerator(cfg ZImageConfig) *ZImageGenerator {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation"
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "output/images"
	}
	return &ZImageGenerator{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Generate 根据提示词生成一张图片。
// 流程：构建提示词 → 调 API → 等待完成 → 下载图片 → 返回结果。
func (g *ZImageGenerator) Generate(ctx context.Context, prompt domain.ImagePrompt) ([]domain.GeneratedImage, error) {
	// 构建生图提示词
	promptText := buildImagePrompt(prompt)

	// 调用 API（同步模式）
	imageURL, err := g.callAPI(ctx, promptText)
	if err != nil {
		return nil, fmt.Errorf("生图失败: %w", err)
	}

	// 下载图片到本地
	localPath, err := g.downloadImage(ctx, imageURL, prompt.QuestionID)
	if err != nil {
		return nil, fmt.Errorf("下载图片失败: %w", err)
	}

	imgID := fmt.Sprintf("img-%s-%d", prompt.QuestionID, time.Now().UnixNano())
	return []domain.GeneratedImage{{
		ID:           imgID,
		ImagePath:    localPath,
		ModelName:    "qwen-image-2.0",
		ModelVersion: "v1",
		Status:       domain.ImageStatusPending,
		CreatedAt:    time.Now(),
	}}, nil
}

// buildImagePrompt 将结构化提示词转为生图用的文本。
func buildImagePrompt(p domain.ImagePrompt) string {
	// 基础描述
	text := fmt.Sprintf("%s，%s。", p.Style, p.Subject)

	// 必须出现的要素
	if len(p.MustInclude) > 0 {
		text += "必须出现："
		for i, item := range p.MustInclude {
			if i > 0 {
				text += "、"
			}
			text += item
		}
		text += "。"
	}

	// 不能出现的要素
	if len(p.MustExclude) > 0 {
		text += "不能出现："
		for i, item := range p.MustExclude {
			if i > 0 {
				text += "、"
			}
			text += item
		}
		text += "。"
	}

	return text
}

// callAPI 调用生图 API（同步模式），返回图片 URL。
func (g *ZImageGenerator) callAPI(ctx context.Context, prompt string) (string, error) {
	// 截断过长的提示词
	if len(prompt) > 300 {
		prompt = prompt[:300]
	}

	reqBody := map[string]interface{}{
		"model": "qwen-image-2.0",
		"input": map[string]interface{}{
			"messages": []map[string]interface{}{
				{
					"role": "user",
					"content": []map[string]string{
						{"text": prompt},
					},
				},
			},
		},
		"parameters": map[string]interface{}{
			"n":    1,
			"size": "1024*1024",
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("API status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// 解析同步响应（格式：output.choices[0].message.content[0].image）
	var result struct {
		Output struct {
			Choices []struct {
				Message struct {
					Content []struct {
						Image string `json:"image"`
					} `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w, 响应: %s", err, string(bodyBytes[:min(200, len(bodyBytes))]))
	}

	if len(result.Output.Choices) > 0 {
		msg := result.Output.Choices[0].Message
		if len(msg.Content) > 0 && msg.Content[0].Image != "" {
			return msg.Content[0].Image, nil
		}
	}

	return "", fmt.Errorf("未返回图片URL, 响应: %s", string(bodyBytes[:min(300, len(bodyBytes))]))
}

// downloadImage 下载图片到本地。
func (g *ZImageGenerator) downloadImage(ctx context.Context, url string, questionID string) (string, error) {
	os.MkdirAll(g.cfg.OutputDir, 0755)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	filename := fmt.Sprintf("%s-%d.png", questionID, time.Now().UnixNano())
	path := filepath.Join(g.cfg.OutputDir, filename)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}

	return path, nil
}
