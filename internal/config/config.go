package config

import (
	"bufio"
	"os"
	"strings"
	"time"

	"aigo/internal/llm"
)

type Config struct {
	Qwen llm.QwenConfig
}

func FromEnv() Config {
	loadDotEnv(".env")

	return Config{
		Qwen: llm.QwenConfig{
			APIKey:      firstNonEmpty(os.Getenv("DASHSCOPE_API_KEY"), os.Getenv("QWEN_API_KEY")),
			BaseURL:     firstNonEmpty(os.Getenv("QWEN_BASE_URL"), "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			Model:       firstNonEmpty(os.Getenv("QWEN_MODEL"), "qwen3.6-flash"),
			HTTPTimeout: 60 * time.Second,
		},
	}
}

// loadDotEnv 从 .env 文件加载环境变量（不覆盖已有的）。
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
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
