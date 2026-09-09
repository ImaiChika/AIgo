package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"aigo/internal/llm"
)

func clearInferenceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AIGO_ENV_FILE", "-")
	for _, key := range []string{
		"DASHSCOPE_API_KEY",
		"QWEN_DEPLOYMENT",
		"QWEN_API_KEY",
		"QWEN_LOCAL_API_KEY",
		"QWEN_BASE_URL",
		"QWEN_MODEL",
		"QWEN_ENABLE_THINKING",
		"QWEN_BATCH_BACKEND",
		"QWEN_BATCH_API_KEY",
		"QWEN_BATCH_BASE_URL",
		"QWEN_BATCH_MODEL",
		"QWEN_BATCH_PROFILE",
		"QWEN_BATCH_ENABLE_THINKING",
		"DB_DSN",
		"AIGO_DB_HOST",
		"AIGO_DB_PORT",
		"AIGO_DB_USER",
		"AIGO_DB_PASSWORD",
		"AIGO_DB_NAME",
		"AIGO_DB_SSLMODE",
		"JWT_SECRET",
		"AIGO_ADMIN_PASSWORD",
		"AIGO_REGISTER_ENABLED",
		"AIGO_TRUST_PROXY_HEADERS",
	} {
		t.Setenv(key, "")
	}
	for _, name := range secretEnvironmentNames {
		t.Setenv(name+"_FILE", "")
	}
}

func TestRegistrationDefaultsOpenAndProxyHeadersDefaultUntrusted(t *testing.T) {
	clearInferenceEnv(t)
	cfg := FromEnv()
	if !cfg.RegisterEnabled {
		t.Fatal("self-registration should be enabled by default")
	}
	if cfg.TrustProxyHeaders {
		t.Fatal("proxy headers must be untrusted by default for direct deployments")
	}

	t.Setenv("AIGO_REGISTER_ENABLED", "false")
	t.Setenv("AIGO_TRUST_PROXY_HEADERS", "true")
	cfg = FromEnv()
	if cfg.RegisterEnabled || !cfg.TrustProxyHeaders {
		t.Fatalf("explicit auth deployment switches not applied: register=%v trustProxy=%v", cfg.RegisterEnabled, cfg.TrustProxyHeaders)
	}
}

func TestFromEnvProductionHTTPConfig(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("AIGO_HTTP_ADDR", "0.0.0.0:9080")
	t.Setenv("AIGO_WEB_DIST_DIR", "/opt/aigo/web")

	cfg := FromEnv()
	if cfg.HTTPAddr != "0.0.0.0:9080" {
		t.Errorf("HTTPAddr=%q", cfg.HTTPAddr)
	}
	if cfg.WebDistDir != "/opt/aigo/web" {
		t.Errorf("WebDistDir=%q", cfg.WebDistDir)
	}
}

func TestFromEnvCanLoadExplicitExternalFile(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("AIGO_HTTP_ADDR", "")
	t.Setenv("AIGO_WEB_DIST_DIR", "")
	envFile := filepath.Join(t.TempDir(), "production.env")
	if err := os.WriteFile(envFile, []byte("AIGO_HTTP_ADDR=127.0.0.1:9090\nAIGO_WEB_DIST_DIR=/srv/aigo/web\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIGO_ENV_FILE", envFile)

	cfg := FromEnv()
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.WebDistDir != "/srv/aigo/web" {
		t.Fatalf("external env file not loaded: addr=%q web=%q", cfg.HTTPAddr, cfg.WebDistDir)
	}
}

func TestLoadReadsSecretFilesAndEscapesDatabasePassword(t *testing.T) {
	clearInferenceEnv(t)
	secretDir := t.TempDir()
	jwtFile := filepath.Join(secretDir, "jwt")
	dbPasswordFile := filepath.Join(secretDir, "db-password")
	if err := os.WriteFile(jwtFile, []byte("jwt-from-secret-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPasswordFile, []byte("p@ss:/?#% value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SECRET_FILE", jwtFile)
	t.Setenv("AIGO_DB_PASSWORD_FILE", dbPasswordFile)
	t.Setenv("AIGO_DB_HOST", "postgres")
	t.Setenv("AIGO_DB_PORT", "5432")
	t.Setenv("AIGO_DB_USER", "aigo")
	t.Setenv("AIGO_DB_NAME", "aigo-prod")
	t.Setenv("AIGO_DB_SSLMODE", "disable")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != "jwt-from-secret-file" {
		t.Fatalf("JWT secret not loaded from file")
	}
	parsed, err := url.Parse(cfg.DB.DSN)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := parsed.User.Password()
	if !ok || password != "p@ss:/?#% value" {
		t.Fatalf("database password was not URL-escaped safely: ok=%v password=%q", ok, password)
	}
	if parsed.Host != "postgres:5432" || parsed.Path != "/aigo-prod" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("unexpected component DSN: %s", cfg.DB.DSN)
	}
}

func TestLoadRejectsAmbiguousOrMissingExternalConfiguration(t *testing.T) {
	t.Run("missing dotenv", func(t *testing.T) {
		clearInferenceEnv(t)
		t.Setenv("AIGO_ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))
		if _, err := Load(); err == nil {
			t.Fatal("explicit missing AIGO_ENV_FILE must fail")
		}
	})

	t.Run("secret value and file", func(t *testing.T) {
		clearInferenceEnv(t)
		secretFile := filepath.Join(t.TempDir(), "jwt")
		if err := os.WriteFile(secretFile, []byte("file-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("JWT_SECRET", "environment-secret")
		t.Setenv("JWT_SECRET_FILE", secretFile)
		if _, err := Load(); err == nil {
			t.Fatal("secret value and *_FILE ambiguity must fail")
		}
	})

	t.Run("dsn and components", func(t *testing.T) {
		clearInferenceEnv(t)
		t.Setenv("DB_DSN", "postgres://localhost/aigo")
		t.Setenv("AIGO_DB_HOST", "postgres")
		t.Setenv("AIGO_DB_PASSWORD", "secret")
		if _, err := Load(); err == nil {
			t.Fatal("DB_DSN and AIGO_DB_* ambiguity must fail")
		}
	})
}

func TestFromEnvCloudKeepsLegacyDefaults(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("DASHSCOPE_API_KEY", "dash-key")

	cfg := FromEnv()
	if cfg.Qwen.Deployment != llm.DeploymentCloud {
		t.Fatalf("deployment = %q", cfg.Qwen.Deployment)
	}
	if cfg.Qwen.APIKey != "dash-key" {
		t.Errorf("cloud Qwen key = %q", cfg.Qwen.APIKey)
	}
	if cfg.Qwen.BaseURL != DefaultDashScopeBaseURL {
		t.Errorf("cloud base URL = %q", cfg.Qwen.BaseURL)
	}
	if cfg.Qwen.Model != DefaultQwenCloudModel {
		t.Errorf("cloud model = %q", cfg.Qwen.Model)
	}
	if cfg.Batch.APIKey != "dash-key" || cfg.Batch.Model != DefaultQwenCloudModel {
		t.Errorf("batch config = %#v", cfg.Batch)
	}
	if !cfg.Batch.EnableThinking {
		t.Error("batch thinking default should stay enabled")
	}
}

func TestFromEnvCloudKeepsQwenAPIKeyAliasForDashScopeServices(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_API_KEY", "legacy-qwen-key")

	cfg := FromEnv()
	if cfg.Qwen.APIKey != "legacy-qwen-key" {
		t.Errorf("realtime key = %q", cfg.Qwen.APIKey)
	}
	if cfg.Batch.APIKey != "legacy-qwen-key" {
		t.Errorf("legacy key not preserved for DashScope batch: %q", cfg.Batch.APIKey)
	}
}

func TestFromEnvLocalDoesNotLeakDashScopeKeyAndKeepsCloudBatchIndependent(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "local")
	t.Setenv("QWEN_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("QWEN_MODEL", "Qwen/Qwen3.5-35B-A3B")
	t.Setenv("DASHSCOPE_API_KEY", "dash-secret")

	cfg := FromEnv()
	if cfg.Qwen.Deployment != llm.DeploymentLocal {
		t.Fatalf("deployment = %q", cfg.Qwen.Deployment)
	}
	if cfg.Qwen.APIKey != "" {
		t.Fatalf("local endpoint received DashScope key: %q", cfg.Qwen.APIKey)
	}
	if cfg.Qwen.Model != "Qwen/Qwen3.5-35B-A3B" {
		t.Errorf("local model = %q", cfg.Qwen.Model)
	}
	if cfg.Batch.APIKey != "dash-secret" {
		t.Errorf("batch key should remain DashScope key, got %q", cfg.Batch.APIKey)
	}
	if cfg.Batch.BaseURL != DefaultDashScopeBaseURL {
		t.Errorf("local realtime URL leaked into batch URL: %q", cfg.Batch.BaseURL)
	}
	if cfg.Batch.Model != DefaultQwenCloudModel {
		t.Errorf("local realtime model leaked into cloud batch model: %q", cfg.Batch.Model)
	}
	if cfg.Batch.Backend != "local" {
		t.Errorf("local deployment must not auto-enable cloud batch, backend = %q", cfg.Batch.Backend)
	}
	if cfg.Batch.APIKey != "dash-secret" {
		t.Errorf("DashScope batch key = %q", cfg.Batch.APIKey)
	}
}

func TestFromEnvMapsThinkingAndLocalGatewayKey(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "LOCAL")
	t.Setenv("QWEN_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("QWEN_MODEL", "aigo-question-generator")
	t.Setenv("QWEN_LOCAL_API_KEY", "gateway-key")
	t.Setenv("QWEN_ENABLE_THINKING", "false")
	t.Setenv("QWEN_BATCH_ENABLE_THINKING", "0")

	cfg := FromEnv()
	if cfg.Qwen.APIKey != "gateway-key" {
		t.Errorf("local gateway key = %q", cfg.Qwen.APIKey)
	}
	if cfg.Qwen.EnableThinking == nil || *cfg.Qwen.EnableThinking {
		t.Errorf("Qwen thinking = %#v", cfg.Qwen.EnableThinking)
	}
	if cfg.Batch.EnableThinking {
		t.Error("batch thinking should be disabled")
	}
}

func TestFromEnvLocalIgnoresLegacyCloudKeys(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "local")
	t.Setenv("QWEN_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("QWEN_MODEL", "aigo-question-generator")
	t.Setenv("QWEN_API_KEY", "old-cloud-key")
	t.Setenv("DASHSCOPE_API_KEY", "dash-key")

	cfg := FromEnv()
	if cfg.Qwen.APIKey != "" {
		t.Fatalf("legacy cloud key leaked to local endpoint: %q", cfg.Qwen.APIKey)
	}
}

func TestFromEnvCustomCloudGatewayKeyIsNotReusedForDashScope(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "cloud")
	t.Setenv("QWEN_BASE_URL", "https://model-gateway.internal/v1")
	t.Setenv("QWEN_MODEL", "aigo-question-generator")
	t.Setenv("QWEN_API_KEY", "gateway-key")

	cfg := FromEnv()
	if cfg.Qwen.APIKey != "gateway-key" {
		t.Errorf("realtime gateway key = %q", cfg.Qwen.APIKey)
	}
	if cfg.Batch.APIKey != "" {
		t.Fatalf("gateway key leaked to DashScope batch: %q", cfg.Batch.APIKey)
	}
}

func TestFromEnvCustomGatewayDoesNotReceiveDashScopeBatchKey(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "cloud")
	t.Setenv("QWEN_BASE_URL", "https://model-gateway.internal/v1")
	t.Setenv("QWEN_MODEL", "aigo-question-generator")
	t.Setenv("QWEN_API_KEY", "gateway-key")
	t.Setenv("DASHSCOPE_API_KEY", "dash-key")

	cfg := FromEnv()
	if cfg.Qwen.APIKey != "gateway-key" {
		t.Errorf("realtime key = %q", cfg.Qwen.APIKey)
	}
	if cfg.Batch.APIKey != "dash-key" {
		t.Errorf("batch key = %q", cfg.Batch.APIKey)
	}
	if cfg.Batch.BaseURL == "https://model-gateway.internal/v1" {
		t.Fatal("DashScope batch key would be sent to custom realtime gateway")
	}
	if cfg.Batch.BaseURL != DefaultDashScopeBaseURL {
		t.Errorf("batch base URL = %q", cfg.Batch.BaseURL)
	}
	if cfg.Batch.Model != DefaultQwenCloudModel {
		t.Errorf("custom served model leaked into DashScope batch model: %q", cfg.Batch.Model)
	}
}

func TestIsDashScopeURLAllowlist(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://dashscope.aliyuncs.com/compatible-mode/v1", true},
		{"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", true},
		{"https://dashscope-us.aliyuncs.com/compatible-mode/v1", true},
		{"https://workspace.ap-southeast-1.maas.aliyuncs.com/api/v1", true},
		{"https://batch.dashscope.aliyuncs.com/v1", true},
		{"https://evil-dashscope.aliyuncs.com/v1", false},
		{"https://maas.aliyuncs.com.evil.example/v1", false},
		{"https://model-gateway.internal/v1", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := isDashScopeURL(tt.url); got != tt.want {
				t.Errorf("isDashScopeURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInvalidDeploymentFailsClosedAndCannotAutoEnableCloudBatch(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "locla")
	t.Setenv("DASHSCOPE_API_KEY", "dash-key")

	cfg := FromEnv()
	if cfg.Batch.Backend != "local" {
		t.Fatalf("invalid non-cloud deployment auto-enabled backend %q", cfg.Batch.Backend)
	}
	if err := cfg.ValidateInference(); err == nil {
		t.Fatal("invalid deployment should fail validation")
	}
}
