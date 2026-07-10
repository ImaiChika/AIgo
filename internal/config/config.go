// Package config 负责从环境变量和 .env 文件加载系统配置。
package config

import (
	"bufio"
	"os"
	"strings"
	"time"

	"aigo/internal/llm"
)

// Config 系统配置，包含千问 API 配置和数据库配置。
type Config struct {
	Qwen llm.QwenConfig // 千问大模型 API 配置
	DB   DBConfig        // 数据库配置
}

// DBConfig 数据库配置。
type DBConfig struct {
	Driver string // 驱动类型："memory"（内存）或 "postgres"（PostgreSQL）
	DSN    string // 数据库连接字符串
}

// FromEnv 从环境变量加载配置。优先读取 .env 文件。
func FromEnv() Config {
	// 尝试加载 .env 文件中的环境变量（不覆盖已有的）
	loadDotEnv(".env")

	dbDriver := firstNonEmpty(os.Getenv("DB_DRIVER"), "memory")
	dbDSN := firstNonEmpty(os.Getenv("DB_DSN"), "postgres://localhost:5432/aigo?sslmode=disable")

	return Config{
		Qwen: llm.QwenConfig{
			APIKey:      firstNonEmpty(os.Getenv("DASHSCOPE_API_KEY"), os.Getenv("QWEN_API_KEY")),
			BaseURL:     firstNonEmpty(os.Getenv("QWEN_BASE_URL"), "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			Model:       firstNonEmpty(os.Getenv("QWEN_MODEL"), "qwen3.6-flash"),
			HTTPTimeout: 60 * time.Second,
		},
		DB: DBConfig{
			Driver: dbDriver,
			DSN:    dbDSN,
		},
	}
}

// loadDotEnv 从 .env 文件加载环境变量。只设置尚未存在的变量，不覆盖已有值。
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // 文件不存在则跳过
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过空行和注释行
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 按第一个 = 分割键值对
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// 只在环境变量尚未设置时才写入
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

// firstNonEmpty 返回参数中第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
