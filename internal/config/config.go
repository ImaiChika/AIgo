package config

import (
	"os"
	"time"

	"aigo/internal/llm"
)

type Config struct {
	Qwen llm.QwenConfig
}

func FromEnv() Config {
	return Config{
		Qwen: llm.QwenConfig{
			APIKey:      firstNonEmpty(os.Getenv("DASHSCOPE_API_KEY"), os.Getenv("QWEN_API_KEY")),
			BaseURL:     firstNonEmpty(os.Getenv("QWEN_BASE_URL"), "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			Model:       firstNonEmpty(os.Getenv("QWEN_MODEL"), "qwen-plus"),
			HTTPTimeout: 60 * time.Second,
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
