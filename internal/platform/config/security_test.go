package config

import (
	"errors"
	"strings"
	"testing"
)

const validAdminToken = "A9v!3Kq#7mZ@2pL$8xR%5tC&1nH*6sW4"

const alternateValidAdminToken = "q7#L2v@N9!cR4$wX8%kT1&mP6*eH3^sD5"

func TestLoadRequiresAdminTokenInProduction(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Load() error = %v", err)
	}
	t.Setenv("APP_ADMIN_TOKEN", validAdminToken)
	got, err := Load()
	if err != nil || got.Security.AdminToken != validAdminToken {
		t.Fatalf("Load() = %+v error=%v", got.Security, err)
	}
}

func TestLoadRejectsInvalidAdminToken(t *testing.T) {
	for _, value := range []string{"short", strings.Repeat("x", 257), strings.Repeat("a", 31) + " ",
		strings.Repeat("a", 16) + "\n" + strings.Repeat("b", 16)} {
		t.Run(value, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("APP_ADMIN_TOKEN", value)
			if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadRejectsPredictableAdminTokenWithoutLeakingValue(t *testing.T) {
	tests := map[string]string{
		"单字符重复":          strings.Repeat("a", 32),
		"单字符近重复":         strings.Repeat("a", 31) + "B",
		"单字符两位偏差":        strings.Repeat("a", 30) + "BC",
		"单字符三位偏差":        "B" + strings.Repeat("a", 14) + "C" + strings.Repeat("a", 15) + "D",
		"开头字符近重复":        "B" + strings.Repeat("a", 31),
		"数字重复":           strings.Repeat("0", 32),
		"数字近重复":          strings.Repeat("0", 31) + "A",
		"十六字节周期重复":       strings.Repeat("0123456789abcdef", 2),
		"十六字节周期追加字符":     strings.Repeat("0123456789abcdef", 2) + "A",
		"十六字节周期中间突变":     "0123456789abcdef0123456789abcXef",
		"changeme 重复":    strings.Repeat("changeme", 4),
		"password 重复":    strings.Repeat("password", 4),
		"password 重复加数字": strings.Repeat("password", 4) + "1",
		"password 重复加三字母": strings.Repeat("password", 4) + "abc",
		"占位词加长数字装饰":      "password123456789012345678901234",
		"占位词拼接":          "Change-Me/password_admin.secret-example",
		"占位词加符号与短后缀":     "ChangeMe!Password@Admin#Secret!1x",
		"占位词加数字后缀":       "changeme-password-admin-secret-1234",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("APP_ENV", "production")
			t.Setenv("APP_ADMIN_TOKEN", value)
			err := loadError(t)
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "APP_ADMIN_TOKEN") {
				t.Fatalf("Load() error = %v", err)
			}
			if strings.Contains(err.Error(), value) {
				t.Fatalf("错误信息泄露管理员令牌: %v", err)
			}
		})
	}
}

func TestLoadAcceptsStrongAdminTokens(t *testing.T) {
	for _, value := range []string{validAdminToken, alternateValidAdminToken} {
		clearConfigEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("APP_ADMIN_TOKEN", value)
		got, err := Load()
		if err != nil || got.Security.AdminToken != value {
			t.Fatalf("Load() token length=%d error=%v", len(value), err)
		}
	}
}

func TestLoadRejectsUnsafeRateLimit(t *testing.T) {
	tests := []struct{ perMinute, burst string }{
		{perMinute: "60001", burst: "30"},
		{perMinute: "120", burst: "19"},
		{perMinute: "20", burst: "21"},
	}
	for _, test := range tests {
		clearConfigEnv(t)
		t.Setenv("APP_RATE_LIMIT_PER_MINUTE", test.perMinute)
		t.Setenv("APP_RATE_LIMIT_BURST", test.burst)
		if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("rate=%+v error=%v", test, err)
		}
	}
}

func TestLoadRejectsSecretReuseWithoutLeakingValue(t *testing.T) {
	clearConfigEnv(t)
	secret := "reused-secret-0123456789abcdef01234567"
	t.Setenv("APP_ADMIN_TOKEN", secret)
	t.Setenv("BOCHA_ENABLED", "true")
	t.Setenv("BOCHA_API_KEY", secret)
	err := loadError(t)
	if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsPlaceholderSecret(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("AMAP_ENABLED", "true")
	t.Setenv("AMAP_API_KEY", "secret")
	t.Setenv("AMAP_JSCODE", "server-jscode")
	err := loadError(t)
	if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "server-jscode") {
		t.Fatalf("Load() error = %v", err)
	}
}

func loadError(t *testing.T) error {
	t.Helper()
	_, err := Load()
	if err == nil {
		t.Fatal("Load() 未拒绝不安全配置")
	}
	return err
}
