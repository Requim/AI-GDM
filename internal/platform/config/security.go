package config

import (
	"crypto/sha256"
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	defaultRateLimitPerMinute = 120
	defaultRateLimitBurst     = 30
	minAdminTokenBytes        = 32
	maxSecretBytes            = 4096
	minimumPatternDeviations  = 3
	predictablePatternFactor  = 8
)

// SecurityConfig 保存入站请求安全边界；管理员令牌只允许从服务端环境变量读取。
type SecurityConfig struct {
	AdminToken         string
	RateLimitPerMinute int
	RateLimitBurst     int
}

func loadSecurity() (SecurityConfig, error) {
	perMinute, err := positiveIntEnv("APP_RATE_LIMIT_PER_MINUTE", defaultRateLimitPerMinute)
	if err != nil {
		return SecurityConfig{}, err
	}
	burst, err := positiveIntEnv("APP_RATE_LIMIT_BURST", defaultRateLimitBurst)
	if err != nil {
		return SecurityConfig{}, err
	}
	return SecurityConfig{
		AdminToken: os.Getenv("APP_ADMIN_TOKEN"), RateLimitPerMinute: perMinute, RateLimitBurst: burst,
	}, nil
}

func validateSecurity(config Config) error {
	switch config.Environment {
	case "development", "test", "production":
	default:
		return configError("APP_ENV 只能是 development、test 或 production")
	}
	security := config.Security
	if security.RateLimitPerMinute > 60_000 || security.RateLimitBurst < 20 || security.RateLimitBurst > 10_000 {
		return configError("入站限流配置超过安全上限")
	}
	if security.RateLimitBurst > security.RateLimitPerMinute {
		return configError("APP_RATE_LIMIT_BURST 不得大于每分钟请求上限")
	}
	if config.Environment == "production" && security.AdminToken == "" {
		return configError("生产环境必须配置 APP_ADMIN_TOKEN")
	}
	if security.AdminToken != "" {
		return validateAdminToken(security.AdminToken)
	}
	return nil
}

func validateAdminToken(value string) error {
	if len(value) < minAdminTokenBytes || len(value) > 256 || value != strings.TrimSpace(value) {
		return configError("APP_ADMIN_TOKEN 必须是 32 至 256 字节且首尾无空白")
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return configError("APP_ADMIN_TOKEN 只能包含可见 ASCII 字符")
		}
	}
	if predictableAdminToken(value) {
		return configError("APP_ADMIN_TOKEN 不得使用明显可预测的值")
	}
	return nil
}

func predictableAdminToken(value string) bool {
	normalized := strings.ToLower(value)
	return nearRepeatedTokenPattern(normalized) || placeholderDerivedToken(normalized)
}

func nearRepeatedTokenPattern(value string) bool {
	for size := 1; size <= len(value)/2; size++ {
		deviations := periodicDeviationCount(value, size)
		if deviations <= predictablePatternDeviations(len(value)) {
			return true
		}
	}
	return false
}

func periodicDeviationCount(value string, size int) int {
	deviations := 0
	for offset := 0; offset < size; offset++ {
		counts, total, maximum := [128]int{}, 0, 0
		for index := offset; index < len(value); index += size {
			counts[value[index]]++
			total++
			maximum = max(maximum, counts[value[index]])
		}
		deviations += total - maximum
	}
	return deviations
}

func predictablePatternDeviations(length int) int {
	allowed := length / predictablePatternFactor
	if allowed < minimumPatternDeviations {
		return minimumPatternDeviations
	}
	return allowed
}

func placeholderDerivedToken(value string) bool {
	normalized := normalizePlaceholderToken(value)
	matchedBytes := 0
	for index := 0; index < len(normalized); {
		prefix := placeholderPrefix(normalized[index:])
		if prefix != "" {
			matchedBytes += len(prefix)
			index += len(prefix)
			continue
		}
		index++
	}
	return matchedBytes >= 5 && matchedBytes*4 >= len(normalized)*3
}

func placeholderPrefix(value string) string {
	longest := ""
	for _, placeholder := range placeholderWords() {
		normalized := normalizePlaceholderToken(placeholder)
		if strings.HasPrefix(value, normalized) && len(normalized) > len(longest) {
			longest = normalized
		}
	}
	return longest
}

func normalizePlaceholderToken(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func validateSecretSet(config Config) error {
	secrets := configuredSecrets(config)
	seen := make(map[[sha256.Size]byte]string, len(secrets))
	for _, secret := range secrets {
		if err := validateSecretBoundary(secret); err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(secret.value))
		if previous, exists := seen[digest]; exists {
			return configError("敏感配置 %s 与 %s 不得复用", secret.name, previous)
		}
		seen[digest] = secret.name
	}
	return nil
}

type namedSecret struct {
	name   string
	value  string
	header bool
}

func configuredSecrets(config Config) []namedSecret {
	values := []namedSecret{
		{name: "APP_ADMIN_TOKEN", value: config.Security.AdminToken, header: true},
		{name: "REDIS_PASSWORD", value: config.RedisPassword},
		{name: "OPEN_METEO_API_KEY", value: config.Weather.APIKey, header: true},
		{name: "AMAP_API_KEY", value: config.Map.APIKey, header: true},
		{name: "AMAP_JSCODE", value: config.Map.SecurityCode, header: true},
		{name: "BOCHA_API_KEY", value: config.Search.APIKey, header: true},
		{name: "LLM_API_KEY", value: config.LLM.APIKey, header: true},
	}
	if password := databasePassword(config.DatabaseURL); password != "" {
		values = append(values, namedSecret{name: "DATABASE_URL password", value: password})
	}
	result := values[:0]
	for _, value := range values {
		if value.value != "" {
			result = append(result, value)
		}
	}
	return result
}

func validateSecretBoundary(secret namedSecret) error {
	if len(secret.value) > maxSecretBytes || !utf8.ValidString(secret.value) {
		return configError("敏感配置 %s 超过长度限制或不是 UTF-8", secret.name)
	}
	if placeholderSecret(secret.value) {
		return configError("敏感配置 %s 使用了占位值", secret.name)
	}
	for _, value := range secret.value {
		if unicode.IsControl(value) || secret.header && unicode.IsSpace(value) {
			return configError("敏感配置 %s 包含不允许的控制或空白字符", secret.name)
		}
	}
	return nil
}

func placeholderSecret(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, placeholder := range placeholderWords() {
		if normalized == placeholder {
			return true
		}
	}
	return false
}

func placeholderWords() []string {
	return []string{
		"admin", "changeme", "change-me", "example", "password", "replace-me", "secret", "your-api-key",
	}
}

func databasePassword(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return ""
	}
	password, _ := parsed.User.Password()
	return password
}
