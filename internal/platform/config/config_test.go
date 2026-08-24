package config

import (
	"errors"
	"testing"
	"time"
)

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
	if got.LHASA.BaseURL != defaultLHASABaseURL || got.LHASA.DataDir != defaultLHASADataDir ||
		got.LHASA.StaleAfter != 12*time.Hour || got.LHASA.GDALBinary != defaultGDALBinary {
		t.Fatalf("LHASA = %+v", got.LHASA)
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
	t.Setenv("LHASA_BASE_URL", "https://example.test/lhasa")
	t.Setenv("LHASA_DATA_DIR", "/srv/lhasa")
	t.Setenv("LHASA_STALE_AFTER", "8h")
	t.Setenv("GDAL_BINARY", "/usr/bin/gdal")
	t.Setenv("GDAL_TEMP_DIR", "/tmp/gdal")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.LHASA.BaseURL != "https://example.test/lhasa" || got.LHASA.DataDir != "/srv/lhasa" ||
		got.LHASA.StaleAfter != 8*time.Hour || got.LHASA.GDALBinary != "/usr/bin/gdal" ||
		got.LHASA.TemporaryDir != "/tmp/gdal" {
		t.Fatalf("LHASA = %+v", got.LHASA)
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
		"DATABASE_URL", "REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB",
		"REFRESH_ENABLED", "REFRESH_INTERVAL", "REFRESH_TIMEOUT",
		"OPEN_METEO_BASE_URL", "OPEN_METEO_API_KEY", "OPEN_METEO_POINTS",
		"OPEN_METEO_PAST_HOURS", "OPEN_METEO_FORECAST_HOURS",
		"OPEN_METEO_FALLBACK_MAX_AGE", "OPEN_METEO_MAX_POINTS_PER_REQUEST",
		"LHASA_BASE_URL", "LHASA_DATA_DIR", "LHASA_STALE_AFTER",
		"GDAL_BINARY", "GDAL_TEMP_DIR",
	} {
		t.Setenv(name, "")
	}
}
