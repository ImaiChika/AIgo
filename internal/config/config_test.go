package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		"AIGO_DB_MAX_OPEN_CONNS", "AIGO_DB_MAX_IDLE_CONNS", "AIGO_DB_CONN_MAX_LIFETIME_SECONDS",
		"AIGO_DB_HOST",
		"AIGO_DB_PORT",
		"AIGO_DB_USER",
		"AIGO_DB_PASSWORD",
		"AIGO_DB_NAME",
		"AIGO_DB_SSLMODE",
		"JWT_SECRET",
		"AIGO_ADMIN_PASSWORD",
		"AIGO_GENERATION_CONCURRENCY",
		"AIGO_BATCH_CONCURRENCY",
		"AIGO_AICHECK_TIMEOUT",
		"AIGO_REGISTER_ENABLED",
		"AIGO_TRUST_PROXY_HEADERS",
		"AIGO_LOG_FORMAT",
		"AIGO_HTTP_MAX_INFLIGHT",
		"AIGO_HTTP_EXPENSIVE_MAX_INFLIGHT",
		"AIGO_HTTP_HEAVY_MAX_INFLIGHT",
		"AIGO_HTTP_RETRY_AFTER_SECONDS",
	} {
		t.Setenv(key, "")
	}
	for _, name := range secretEnvironmentNames {
		t.Setenv(name+"_FILE", "")
	}
}

func TestHTTPConcurrencyLimitsUseSafeDefaultsAndBounds(t *testing.T) {
	clearInferenceEnv(t)
	cfg := FromEnv()
	if cfg.HTTPMaxInFlight != 64 || cfg.HTTPExpensiveMax != 8 || cfg.HTTPHeavyMax != 2 || cfg.HTTPRetryAfterSec != 1 {
		t.Fatalf("defaults=%+v", cfg)
	}

	t.Setenv("AIGO_HTTP_MAX_INFLIGHT", "32")
	t.Setenv("AIGO_HTTP_EXPENSIVE_MAX_INFLIGHT", "4")
	t.Setenv("AIGO_HTTP_HEAVY_MAX_INFLIGHT", "1")
	t.Setenv("AIGO_HTTP_RETRY_AFTER_SECONDS", "3")
	cfg = FromEnv()
	if cfg.HTTPMaxInFlight != 32 || cfg.HTTPExpensiveMax != 4 || cfg.HTTPHeavyMax != 1 || cfg.HTTPRetryAfterSec != 3 {
		t.Fatalf("configured limits=%+v", cfg)
	}

	t.Setenv("AIGO_HTTP_MAX_INFLIGHT", "0")
	t.Setenv("AIGO_HTTP_HEAVY_MAX_INFLIGHT", "999")
	cfg = FromEnv()
	if cfg.HTTPMaxInFlight != 64 || cfg.HTTPHeavyMax != 2 {
		t.Fatalf("invalid limits must use defaults: %+v", cfg)
	}
}

func TestLogFormatDefaultsToTextAndAcceptsJSON(t *testing.T) {
	clearInferenceEnv(t)
	if got := FromEnv().LogFormat; got != "text" {
		t.Fatalf("default log format=%q", got)
	}
	t.Setenv("AIGO_LOG_FORMAT", "JSON")
	if got := FromEnv().LogFormat; got != "json" {
		t.Fatalf("configured log format=%q", got)
	}
	t.Setenv("AIGO_LOG_FORMAT", "unsupported")
	if got := FromEnv().LogFormat; got != "text" {
		t.Fatalf("invalid log format must fail safe to text, got %q", got)
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

func TestGenerationConcurrencyConfig(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        int
	}{{"default", "", 2}, {"serial", "1", 1}, {"parallel", "4", 4}, {"zero", "0", 2}, {"too large", "33", 2}, {"invalid", "bad", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			clearInferenceEnv(t)
			t.Setenv("AIGO_GENERATION_CONCURRENCY", tc.value)
			t.Setenv("AIGO_BATCH_CONCURRENCY", tc.value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.GenerationConcurrency != tc.want || cfg.BatchConcurrency != tc.want {
				t.Fatalf("limits: generation=%d batch=%d want=%d", cfg.GenerationConcurrency, cfg.BatchConcurrency, tc.want)
			}
		})
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
}

func TestFromEnvCloudKeepsQwenAPIKeyAliasForDashScopeServices(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_API_KEY", "legacy-qwen-key")

	cfg := FromEnv()
	if cfg.Qwen.APIKey != "legacy-qwen-key" {
		t.Errorf("realtime key = %q", cfg.Qwen.APIKey)
	}
}

func TestFromEnvLocalDoesNotLeakDashScopeKey(t *testing.T) {
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
}

func TestFromEnvMapsThinkingAndLocalGatewayKey(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "LOCAL")
	t.Setenv("QWEN_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("QWEN_MODEL", "aigo-question-generator")
	t.Setenv("QWEN_LOCAL_API_KEY", "gateway-key")
	t.Setenv("QWEN_ENABLE_THINKING", "false")

	cfg := FromEnv()
	if cfg.Qwen.APIKey != "gateway-key" {
		t.Errorf("local gateway key = %q", cfg.Qwen.APIKey)
	}
	if cfg.Qwen.EnableThinking == nil || *cfg.Qwen.EnableThinking {
		t.Errorf("Qwen thinking = %#v", cfg.Qwen.EnableThinking)
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

func TestInvalidDeploymentFailsClosed(t *testing.T) {
	clearInferenceEnv(t)
	t.Setenv("QWEN_DEPLOYMENT", "locla")
	t.Setenv("DASHSCOPE_API_KEY", "dash-key")

	cfg := FromEnv()
	if err := cfg.ValidateInference(); err == nil {
		t.Fatal("invalid deployment should fail validation")
	}
}

func TestAICheckTimeoutBudget(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        time.Duration
	}{
		{"default", "", 120 * time.Second}, {"explicit default", "120s", 120 * time.Second}, {"large configured", "25m", 25 * time.Minute},
		{"upper bound", "30m", 30 * time.Minute}, {"test lower bound", "10s", 10 * time.Second},
		{"too large", "31m", 120 * time.Second}, {"invalid", "bad", 120 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearInferenceEnv(t)
			t.Setenv("AIGO_AICHECK_TIMEOUT", tc.value)
			cfg := FromEnv()
			if cfg.AICheck.Timeout != tc.want {
				t.Fatalf("timeout=%s want=%s", cfg.AICheck.Timeout, tc.want)
			}
			if cfg.AICheck.Client.HTTPTimeout < cfg.AICheck.Timeout {
				t.Fatalf("HTTP timeout %s truncates check budget %s", cfg.AICheck.Client.HTTPTimeout, cfg.AICheck.Timeout)
			}
			if cfg.AICheck.Client.HTTPTimeout != cfg.AICheck.Timeout+30*time.Second {
				t.Fatalf("HTTP timeout=%s want budget+30s", cfg.AICheck.Client.HTTPTimeout)
			}
			if cfg.Qwen.HTTPTimeout != 5*time.Minute {
				t.Fatalf("generation timeout changed: %s", cfg.Qwen.HTTPTimeout)
			}
		})
	}
}

func TestIndependentAICheckConfigIsAnExplicitCompleteRoute(t *testing.T) {
	for _, field := range []string{"", "AIGO_AICHECK_BASE_URL", "AIGO_AICHECK_MODEL", "AIGO_AICHECK_DEPLOYMENT", "AIGO_AICHECK_API_KEY"} {
		t.Run("override_"+field, func(t *testing.T) {
			clearInferenceEnv(t)
			for _, key := range []string{"AIGO_AICHECK_BASE_URL", "AIGO_AICHECK_MODEL", "AIGO_AICHECK_DEPLOYMENT", "AIGO_AICHECK_API_KEY"} {
				t.Setenv(key, "")
			}
			t.Setenv("QWEN_DEPLOYMENT", "cloud")
			t.Setenv("QWEN_MODEL", "generation")
			t.Setenv("DASHSCOPE_API_KEY", "cloud-test-key")
			t.Setenv("AIGO_AICHECK_TIMEOUT", "10m")
			values := map[string]string{"AIGO_AICHECK_BASE_URL": "http://check.internal/v1", "AIGO_AICHECK_MODEL": "checker", "AIGO_AICHECK_DEPLOYMENT": "local", "AIGO_AICHECK_API_KEY": "check-test-key"}
			if field != "" {
				t.Setenv(field, values[field])
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AICheck.Independent != (field != "") {
				t.Fatalf("explicit=%v for %s", cfg.AICheck.Independent, field)
			}
			if field == "AIGO_AICHECK_BASE_URL" && cfg.AICheck.Client.APIKey != "" {
				t.Fatal("cloud credential leaked to independent local endpoint")
			}
			if cfg.AICheck.Client.HTTPTimeout != 630*time.Second {
				t.Fatal("independent routing changed timeout")
			}
		})
	}
}

func TestDatabasePoolDefaultsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, open, idle, life string
		wantOpen, wantIdle     int
		wantLife               time.Duration
	}{
		{"defaults", "", "", "", 10, 5, 5 * time.Minute}, {"configured", "24", "8", "120", 24, 8, 2 * time.Minute}, {"idle_clamped", "3", "5", "30", 3, 3, 30 * time.Second}, {"no_idle", "4", "0", "3600", 4, 0, time.Hour}, {"invalid", "999", "-1", "1", 10, 5, 5 * time.Minute},
		{"no_nested_budget", "1", "1", "300", 10, 1, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearInferenceEnv(t)
			t.Setenv("AIGO_DB_MAX_OPEN_CONNS", tc.open)
			t.Setenv("AIGO_DB_MAX_IDLE_CONNS", tc.idle)
			t.Setenv("AIGO_DB_CONN_MAX_LIFETIME_SECONDS", tc.life)
			cfg := FromEnv()
			if cfg.DB.MaxOpenConns != tc.wantOpen || cfg.DB.MaxIdleConns != tc.wantIdle || cfg.DB.ConnMaxLifetime != tc.wantLife {
				t.Fatalf("wrong pool bounds: %d/%d/%v", cfg.DB.MaxOpenConns, cfg.DB.MaxIdleConns, cfg.DB.ConnMaxLifetime)
			}
		})
	}
}
