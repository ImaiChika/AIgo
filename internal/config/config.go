// Package config 负责从环境变量和 .env 文件加载系统配置。
package config

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"aigo/internal/llm"
)

// DefaultJWTSecret 开发环境兜底密钥。生产环境必须通过 JWT_SECRET 显式设置，
// 使用默认密钥启动 serve 会被拒绝（见 Config.IsDefaultJWTSecret）。
const DefaultJWTSecret = "aigo-jwt-secret-default"

const (
	DefaultDashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	DefaultQwenCloudModel   = "qwen3.5-flash"
)

// AICheckConfig 描述 AI 质量检查的自动执行配置。
// 检查调用同样走 llm.Client（OpenAI 兼容），Client 默认完全沿用 Qwen 实时配置；
// 未来自部署检查服务可通过 AIGO_AICHECK_* 独立指定，实现生成与检查用不同模型/端点。
type AICheckConfig struct {
	AutoEnabled bool           // 生成/导入/新建后自动触发检查（AIGO_AICHECK_AUTO，默认开）
	Concurrency int            // 后台检查 worker 并发数（AIGO_AICHECK_CONCURRENCY，默认 2）
	Timeout     time.Duration  // 单次检查调用超时（AIGO_AICHECK_TIMEOUT，默认 90s；任务租约=超时+30s）
	MaxAttempts int            // 失败重试上限（AIGO_AICHECK_MAX_ATTEMPTS，默认 3，耗尽标记最终失败）
	Client      llm.QwenConfig // 检查专用 LLM 配置；未设置 AIGO_AICHECK_* 时与 Qwen 实时配置一致
}

// Config 系统配置，包含千问 API 配置和数据库配置。
type Config struct {
	Qwen              llm.QwenConfig // 千问实时推理配置（云端或本地）
	AICheck           AICheckConfig  // AI 质量检查自动执行配置
	ReviewRequireAI   bool           // 送审是否强制要求 AI 检查通过（AIGO_REVIEW_REQUIRE_AI_CHECK，默认开）
	DB                DBConfig       // 数据库配置
	JWTSecret         string         // JWT 签名密钥
	CORSOrigins       []string       // 允许的跨域来源列表（空=禁止跨域，开发用 Vite 代理为同源）
	RegisterEnabled   bool           // 是否开放用户自助注册（默认开启，可用 AIGO_REGISTER_ENABLED=0 关闭）
	TrustProxyHeaders bool           // 是否信任受控反向代理写入的 X-AIgo-Client-IP
	HTTPAddr          string         // HTTP 监听地址（生产可配置，默认仅监听本机）
	WebDistDir        string         // Vite 生产构建目录；空表示仅提供 API
}

// IsDefaultJWTSecret 判断 JWT 密钥是否为内置默认值（未显式配置）。
func (c Config) IsDefaultJWTSecret() bool {
	return c.JWTSecret == "" || c.JWTSecret == DefaultJWTSecret
}

// ValidateInference 对可能导致误路由或数据外发的部署配置做 fail-closed 校验。
func (c Config) ValidateInference() error {
	switch c.Qwen.Deployment {
	case llm.DeploymentCloud:
	case llm.DeploymentLocal:
		if strings.TrimSpace(c.Qwen.BaseURL) == "" || strings.TrimSpace(c.Qwen.Model) == "" {
			return fmt.Errorf("本地推理必须设置 QWEN_BASE_URL 和 QWEN_MODEL")
		}
	default:
		return fmt.Errorf("QWEN_DEPLOYMENT 仅支持 cloud 或 local，当前为 %q", c.Qwen.Deployment)
	}
	switch c.AICheck.Client.Deployment {
	case llm.DeploymentCloud:
	case llm.DeploymentLocal:
		if strings.TrimSpace(c.AICheck.Client.BaseURL) == "" || strings.TrimSpace(c.AICheck.Client.Model) == "" {
			return fmt.Errorf("本地 AI 检查端点必须设置 AIGO_AICHECK_BASE_URL 和 AIGO_AICHECK_MODEL")
		}
	default:
		return fmt.Errorf("AIGO_AICHECK_DEPLOYMENT 仅支持 cloud 或 local，当前为 %q", c.AICheck.Client.Deployment)
	}
	return nil
}

// DBConfig 数据库配置。
type DBConfig struct {
	DSN string // PostgreSQL 连接字符串
}

var secretEnvironmentNames = []string{
	"JWT_SECRET",
	"AIGO_ADMIN_PASSWORD",
	"AIGO_DB_PASSWORD",
	"DASHSCOPE_API_KEY",
	"QWEN_API_KEY",
	"QWEN_LOCAL_API_KEY",
	"AIGO_AICHECK_API_KEY",
}

// Load 从 dotenv、进程环境和 *_FILE Secret 加载配置，并返回不能安全忽略的错误。
func Load() (Config, error) {
	// 默认读取工作目录 .env；生产可用 AIGO_ENV_FILE 指向外部只读配置，
	// 或设置为 "-" 完全禁用 dotenv、只接受进程环境变量/Secrets。
	envFile := strings.TrimSpace(os.Getenv("AIGO_ENV_FILE"))
	explicitEnvFile := envFile != ""
	if envFile == "" {
		envFile = ".env"
	}
	if envFile != "-" {
		if err := loadDotEnv(envFile); err != nil && explicitEnvFile {
			return Config{}, fmt.Errorf("读取 AIGO_ENV_FILE 失败: %w", err)
		}
	}
	for _, name := range secretEnvironmentNames {
		if err := loadSecretFile(name); err != nil {
			return Config{}, err
		}
	}

	deployment := llm.DeploymentMode(strings.ToLower(strings.TrimSpace(firstNonEmpty(os.Getenv("QWEN_DEPLOYMENT"), string(llm.DeploymentCloud)))))
	qwenBaseURL := strings.TrimSpace(os.Getenv("QWEN_BASE_URL"))
	qwenModel := strings.TrimSpace(os.Getenv("QWEN_MODEL"))
	if deployment == llm.DeploymentCloud {
		qwenBaseURL = firstNonEmpty(qwenBaseURL, DefaultDashScopeBaseURL)
		qwenModel = firstNonEmpty(qwenModel, DefaultQwenCloudModel)
	}

	dashScopeAPIKey := strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY"))
	var qwenAPIKey string
	if deployment == llm.DeploymentLocal {
		// 本地只认独立凭证，避免把旧 QWEN_API_KEY / DASHSCOPE_API_KEY
		// 意外发送到新的内网端点。
		qwenAPIKey = strings.TrimSpace(os.Getenv("QWEN_LOCAL_API_KEY"))
	} else {
		qwenAPIKey = strings.TrimSpace(os.Getenv("QWEN_API_KEY"))
		if isDashScopeURL(qwenBaseURL) {
			// 只在端点已确认属于百炼时保留历史别名兼容。
			qwenAPIKey = firstNonEmpty(qwenAPIKey, dashScopeAPIKey)
			dashScopeAPIKey = firstNonEmpty(dashScopeAPIKey, qwenAPIKey)
		}
	}

	// AI 检查端点默认与实时推理共用一套配置；设置任一 AIGO_AICHECK_* 即启用独立覆盖。
	checkClient := llm.QwenConfig{
		Deployment:     deployment,
		APIKey:         qwenAPIKey,
		BaseURL:        qwenBaseURL,
		Model:          qwenModel,
		EnableThinking: optionalBool(os.Getenv("QWEN_ENABLE_THINKING")),
		HTTPTimeout:    300 * time.Second,
	}
	checkDeployment := strings.TrimSpace(os.Getenv("AIGO_AICHECK_DEPLOYMENT"))
	checkBaseURL := strings.TrimSpace(os.Getenv("AIGO_AICHECK_BASE_URL"))
	checkModel := strings.TrimSpace(os.Getenv("AIGO_AICHECK_MODEL"))
	checkAPIKey := strings.TrimSpace(os.Getenv("AIGO_AICHECK_API_KEY"))
	if checkDeployment != "" || checkBaseURL != "" || checkModel != "" || checkAPIKey != "" {
		switch {
		case checkDeployment != "":
			checkClient.Deployment = llm.DeploymentMode(strings.ToLower(checkDeployment))
		case checkBaseURL != "" && !isDashScopeURL(checkBaseURL):
			// 显式指定非百炼端点时按本地部署处理，避免把云端凭证发到自建端点
			checkClient.Deployment = llm.DeploymentLocal
		}
		if checkBaseURL != "" {
			checkClient.BaseURL = checkBaseURL
		}
		if checkModel != "" {
			checkClient.Model = checkModel
		}
		if checkClient.Deployment == llm.DeploymentLocal {
			// 本地只认独立凭证；主端点同为本地时才允许沿用其凭证
			if checkAPIKey != "" {
				checkClient.APIKey = checkAPIKey
			} else if deployment != llm.DeploymentLocal {
				checkClient.APIKey = ""
			}
		} else {
			checkClient.APIKey = firstNonEmpty(checkAPIKey, qwenAPIKey)
		}
	}

	databaseDSN, err := databaseDSNFromEnv()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Qwen: llm.QwenConfig{
			Deployment:     deployment,
			APIKey:         qwenAPIKey,
			BaseURL:        qwenBaseURL,
			Model:          qwenModel,
			EnableThinking: optionalBool(os.Getenv("QWEN_ENABLE_THINKING")),
			HTTPTimeout:    300 * time.Second,
		},
		AICheck: AICheckConfig{
			AutoEnabled: boolWithDefault(os.Getenv("AIGO_AICHECK_AUTO"), true),
			Concurrency: intWithDefault(os.Getenv("AIGO_AICHECK_CONCURRENCY"), 2, 1, 32),
			Timeout:     optionalDuration(os.Getenv("AIGO_AICHECK_TIMEOUT"), 90*time.Second, 10*time.Second, 10*time.Minute),
			MaxAttempts: intWithDefault(os.Getenv("AIGO_AICHECK_MAX_ATTEMPTS"), 3, 1, 10),
			Client:      checkClient,
		},
		ReviewRequireAI: boolWithDefault(os.Getenv("AIGO_REVIEW_REQUIRE_AI_CHECK"), true),
		DB: DBConfig{
			DSN: databaseDSN,
		},
		JWTSecret:   firstNonEmpty(os.Getenv("JWT_SECRET"), DefaultJWTSecret),
		CORSOrigins: splitComma(os.Getenv("CORS_ALLOWED_ORIGINS")),
		// 默认开启用户自助注册（用户需求）；可用 AIGO_REGISTER_ENABLED=0/false 关闭
		RegisterEnabled:   os.Getenv("AIGO_REGISTER_ENABLED") != "0" && os.Getenv("AIGO_REGISTER_ENABLED") != "false",
		TrustProxyHeaders: boolWithDefault(os.Getenv("AIGO_TRUST_PROXY_HEADERS"), false),
		HTTPAddr:          firstNonEmpty(strings.TrimSpace(os.Getenv("AIGO_HTTP_ADDR")), "127.0.0.1:8080"),
		WebDistDir:        strings.TrimSpace(os.Getenv("AIGO_WEB_DIST_DIR")),
	}, nil
}

// FromEnv 保留给现有库调用和测试；命令入口应使用 Load 以处理 Secret 错误。
func FromEnv() Config {
	cfg, _ := Load()
	return cfg
}

// optionalBool 解析可选布尔值。空值或 auto 返回 nil，交给模型服务默认配置。
func optionalBool(value string) *bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		v := true
		return &v
	case "0", "false", "no", "off":
		v := false
		return &v
	default:
		return nil
	}
}

func boolWithDefault(value string, fallback bool) bool {
	parsed := optionalBool(value)
	if parsed == nil {
		return fallback
	}
	return *parsed
}

// intWithDefault 解析整数配置；非法或超出 [min, max] 时使用默认值。
func intWithDefault(value string, fallback, min, max int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < min || parsed > max {
		return fallback
	}
	return parsed
}

// optionalDuration 解析时长配置，接受 "90s"/"2m" 或纯秒数 "90"；
// 非法或超出 [min, max] 时使用默认值。
func optionalDuration(value string, fallback, min, max time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		seconds, serr := strconv.Atoi(value)
		if serr != nil {
			return fallback
		}
		parsed = time.Duration(seconds) * time.Second
	}
	if parsed < min || parsed > max {
		return fallback
	}
	return parsed
}

func isDashScopeURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "dashscope.aliyuncs.com" ||
		host == "dashscope-intl.aliyuncs.com" ||
		host == "dashscope-us.aliyuncs.com" ||
		strings.HasSuffix(host, ".dashscope.aliyuncs.com") ||
		strings.HasSuffix(host, ".maas.aliyuncs.com")
}

// splitComma 按逗号分割字符串列表（去空白、去空项）。
func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadDotEnv 从 .env 文件加载环境变量。只设置尚未存在的变量，不覆盖已有值。
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
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
			if err := os.Setenv(key, val); err != nil {
				return fmt.Errorf("设置环境变量 %s 失败: %w", key, err)
			}
		}
	}
	return scanner.Err()
}

func loadSecretFile(name string) error {
	fileName := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if fileName == "" {
		return nil
	}
	if os.Getenv(name) != "" {
		return fmt.Errorf("%s 与 %s_FILE 不能同时设置", name, name)
	}
	info, err := os.Stat(fileName)
	if err != nil {
		return fmt.Errorf("读取 %s_FILE 失败: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return fmt.Errorf("%s_FILE 必须是小于 64KiB 的普通文件", name)
	}
	contents, err := os.ReadFile(fileName)
	if err != nil {
		return fmt.Errorf("读取 %s_FILE 失败: %w", name, err)
	}
	value := strings.TrimRight(string(contents), "\r\n")
	if value == "" {
		return fmt.Errorf("%s_FILE 不能为空", name)
	}
	if err := os.Setenv(name, value); err != nil {
		return fmt.Errorf("加载 %s_FILE 失败: %w", name, err)
	}
	return nil
}

func databaseDSNFromEnv() (string, error) {
	dsn := strings.TrimSpace(os.Getenv("DB_DSN"))
	host := strings.TrimSpace(os.Getenv("AIGO_DB_HOST"))
	componentNames := []string{"AIGO_DB_HOST", "AIGO_DB_PORT", "AIGO_DB_USER", "AIGO_DB_PASSWORD", "AIGO_DB_NAME", "AIGO_DB_SSLMODE"}
	componentsConfigured := false
	for _, name := range componentNames {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			componentsConfigured = true
			break
		}
	}
	if dsn != "" && componentsConfigured {
		return "", fmt.Errorf("DB_DSN 与 AIGO_DB_* 分项配置不能同时设置")
	}
	if dsn != "" {
		return dsn, nil
	}
	if !componentsConfigured {
		return "postgres://localhost:5432/aigo?sslmode=disable", nil
	}
	if host == "" {
		return "", fmt.Errorf("使用 AIGO_DB_* 分项配置时必须设置 AIGO_DB_HOST")
	}
	port := firstNonEmpty(strings.TrimSpace(os.Getenv("AIGO_DB_PORT")), "5432")
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("AIGO_DB_PORT 必须是 1-65535 的端口号")
	}
	username := firstNonEmpty(strings.TrimSpace(os.Getenv("AIGO_DB_USER")), "aigo")
	password := os.Getenv("AIGO_DB_PASSWORD")
	if password == "" {
		return "", fmt.Errorf("使用 AIGO_DB_* 分项配置时必须设置 AIGO_DB_PASSWORD 或 AIGO_DB_PASSWORD_FILE")
	}
	databaseName := firstNonEmpty(strings.TrimSpace(os.Getenv("AIGO_DB_NAME")), "aigo")
	sslMode := firstNonEmpty(strings.TrimSpace(os.Getenv("AIGO_DB_SSLMODE")), "disable")
	databaseURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + databaseName,
	}
	query := databaseURL.Query()
	query.Set("sslmode", sslMode)
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String(), nil
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
