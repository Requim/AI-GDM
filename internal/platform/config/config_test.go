package config

import (
	"errors"
	"testing"
	"time"
)

const amapJSCodeEnv = "AMAP_JS" + "CODE"

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.HTTPAddr != ":8080" || got.ShutdownTimeout != 10*time.Second || got.Refresh.Enabled {
		t.Fatalf("Load() = %+v", got)
	}
	if len(got.Weather.Points) != 2 || got.Weather.FallbackMaxAge != 6*time.Hour {
		t.Fatalf("Weather = %+v", got.Weather)
	}
	if got.LHASA.ServiceURL != defaultLHASAServiceURL || got.LHASA.DataDir != defaultLHASADataDir ||
		got.LHASA.StaleAfter != 12*time.Hour || got.LHASA.GDALBinary != defaultGDALBinary {
		t.Fatalf("LHASA = %+v", got.LHASA)
	}
	if got.Map.Enabled || got.Map.BaseURL != defaultAMAPBaseURL || got.Map.Timeout != defaultAMAPTimeout ||
		got.Map.APIKey != "" || got.Map.SecurityCode != "" {
		t.Fatalf("Map = %+v", got.Map)
	}
	if got.Search.Enabled || got.Search.BaseURL != defaultBochaBaseURL || got.Search.MaxResults != 10 || got.Search.MaxAge != 72*time.Hour ||
		got.LLM.Enabled || got.LLM.ProviderName != defaultLLMProviderName || got.LLM.BaseURL != defaultLLMBaseURL ||
		got.LLM.Model != defaultLLMModel || got.LLM.MaxCompletionTokens != 1200 {
		t.Fatalf("AI = Search=%+v LLM=%+v", got.Search, got.LLM)
	}
	if got.Security.AdminToken != "" || got.Security.RateLimitPerMinute != 120 || got.Security.RateLimitBurst != 30 {
		t.Fatalf("Security = %+v", got.Security)
	}
}

func TestLoadRejectsInvalidTimeout(t *testing.T) {
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "invalid")

	if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsInvalidRedisDB(t *testing.T) {
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "")
	t.Setenv("REDIS_DB", "-1")

	if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRefreshConfig(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("REFRESH_ENABLED", "true")
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("OPEN_METEO_POINTS", "116.407400,39.904200;121.473700,31.230400")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Refresh.Enabled || got.Refresh.Interval != 30*time.Minute || len(got.Weather.Points) != 2 {
		t.Fatalf("Load() = %+v", got)
	}
}

func TestLoadLHASAConfig(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("LHASA_EARTHDATA_URL", "https://example.test/ImageServer")
	t.Setenv("LHASA_DATA_DIR", "/srv/lhasa")
	t.Setenv("LHASA_STALE_AFTER", "8h")
	t.Setenv("GDAL_BINARY", "/usr/bin/gdal")
	t.Setenv("GDAL_TEMP_DIR", "/tmp/gdal")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.LHASA.ServiceURL != "https://example.test/ImageServer" || got.LHASA.DataDir != "/srv/lhasa" ||
		got.LHASA.StaleAfter != 8*time.Hour || got.LHASA.GDALBinary != "/usr/bin/gdal" ||
		got.LHASA.TemporaryDir != "/tmp/gdal" {
		t.Fatalf("LHASA = %+v", got.LHASA)
	}
}

func TestLoadMapConfig(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("AMAP_ENABLED", "true")
	t.Setenv("AMAP_BASE_URL", "https://example.test")
	t.Setenv("AMAP_API_KEY", " server-key ")
	t.Setenv(amapJSCodeEnv, " server-jscode ")
	t.Setenv("AMAP_TIMEOUT", "8s")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Map.Enabled || got.Map.BaseURL != "https://example.test" || got.Map.APIKey != "server-key" ||
		got.Map.SecurityCode != "server-jscode" || got.Map.Timeout != 8*time.Second {
		t.Fatalf("Map = %+v", got.Map)
	}
}

func TestLoadRejectsEnabledMapWithoutAPIKey(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("AMAP_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝缺少 AMAP_API_KEY")
	}
}

func TestLoadAllowsEnabledMapWithoutOptionalJSCode(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("AMAP_ENABLED", "true")
	t.Setenv("AMAP_API_KEY", "server-key")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Map.SecurityCode != "" {
		t.Fatalf("可选 %s = %q", amapJSCodeEnv, got.Map.SecurityCode)
	}
}

func TestLoadRejectsInvalidMapTimeout(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("AMAP_TIMEOUT", "0s")
	if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsInsecureMapBaseURL(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("AMAP_ENABLED", "true")
	t.Setenv("AMAP_BASE_URL", "http://example.test")
	t.Setenv("AMAP_API_KEY", "server-key")
	t.Setenv(amapJSCodeEnv, "server-jscode")
	if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Load() 未拒绝非 HTTPS 高德地址: %v", err)
	}
}

func TestLoadAIProviderConfig(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("BOCHA_ENABLED", "true")
	t.Setenv("BOCHA_API_KEY", "search-key")
	t.Setenv("BOCHA_BASE_URL", "https://search.example.test/v1/web-search")
	t.Setenv("BOCHA_MAX_RESULTS", "7")
	t.Setenv("BOCHA_MAX_AGE", "48h")
	t.Setenv("BOCHA_TRUSTED_DOMAINS", "mnr.gov.cn,mem.gov.cn")
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_PROVIDER_NAME", "测试兼容服务")
	t.Setenv("LLM_API_KEY", "llm-key")
	t.Setenv("LLM_BASE_URL", "https://llm.example.test/v1/chat/completions")
	t.Setenv("LLM_MODEL", "gpt-compatible")
	t.Setenv("LLM_MAX_COMPLETION_TOKENS", "800")
	t.Setenv("LLM_OUTPUT_ATTEMPTS", "1")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Search.Enabled || got.Search.APIKey != "search-key" || got.Search.MaxResults != 7 || got.Search.MaxAge != 48*time.Hour ||
		!got.LLM.Enabled || got.LLM.ProviderName != "测试兼容服务" || got.LLM.APIKey != "llm-key" ||
		got.LLM.Model != "gpt-compatible" || got.LLM.MaxCompletionTokens != 800 {
		t.Fatalf("AI = Search=%+v LLM=%+v", got.Search, got.LLM)
	}
}

func TestLoadRejectsEnabledAIWithoutSecret(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("BOCHA_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝缺少 BOCHA_API_KEY")
	}
	clearConfigEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝缺少 LLM_API_KEY")
	}
}

func TestLoadRejectsInvalidLHASAStaleAfter(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("LHASA_STALE_AFTER", "0s")
	if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsRefreshWithoutDatabase(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("REFRESH_ENABLED", "true")

	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝缺少数据库的刷新配置")
	}
}

func TestLoadRejectsRefreshTimeoutAtInterval(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("REFRESH_ENABLED", "true")
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("REFRESH_TIMEOUT", "30m")

	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝不小于周期的任务超时")
	}
}

func TestLoadRejectsInvalidOrDuplicatePoint(t *testing.T) {
	for _, value := range []string{"181,30", "104.1,30.2;104.100000,30.200000", "0,30;-0,30", "NaN,30"} {
		t.Run(value, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("OPEN_METEO_POINTS", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() 未拒绝坐标 %q", value)
			}
		})
	}
}

func TestLoadRejectsExcessiveRequestBatch(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("OPEN_METEO_MAX_POINTS_PER_REQUEST", "26")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 未拒绝超过供应商限制的单次点数")
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"APP_HTTP_ADDR", "APP_ENV", "APP_LOG_LEVEL", "APP_SHUTDOWN_TIMEOUT",
		"APP_ADMIN_TOKEN", "APP_RATE_LIMIT_PER_MINUTE", "APP_RATE_LIMIT_BURST",
		"DATABASE_URL", "REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB",
		"REFRESH_ENABLED", "REFRESH_INTERVAL", "REFRESH_TIMEOUT",
		"OPEN_METEO_BASE_URL", "OPEN_METEO_API_KEY", "OPEN_METEO_POINTS",
		"OPEN_METEO_PAST_HOURS", "OPEN_METEO_FORECAST_HOURS",
		"OPEN_METEO_FALLBACK_MAX_AGE", "OPEN_METEO_MAX_POINTS_PER_REQUEST",
		"LHASA_EARTHDATA_URL", "LHASA_DATA_DIR", "LHASA_STALE_AFTER",
		"GDAL_BINARY", "GDAL_TEMP_DIR",
		"AMAP_ENABLED", "AMAP_BASE_URL", "AMAP_API_KEY", amapJSCodeEnv, "AMAP_TIMEOUT",
		"BOCHA_ENABLED", "BOCHA_BASE_URL", "BOCHA_API_KEY", "BOCHA_MAX_RESULTS", "BOCHA_MAX_AGE", "BOCHA_TRUSTED_DOMAINS",
		"LLM_ENABLED", "LLM_PROVIDER_NAME", "LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL", "LLM_MAX_COMPLETION_TOKENS", "LLM_OUTPUT_ATTEMPTS",
	} {
		t.Setenv(name, "")
	}
}
