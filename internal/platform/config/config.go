package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// ErrInvalidConfig 表示环境配置缺失或不满足运行边界。
var ErrInvalidConfig = errors.New("配置无效")

const (
	defaultHTTPAddr        = ":8080"
	defaultEnvironment     = "development"
	defaultLogLevel        = "info"
	defaultShutdownTimeout = 10 * time.Second
	defaultRefreshInterval = 30 * time.Minute
	defaultRefreshTimeout  = 10 * time.Minute
	defaultWeatherBaseURL  = "https://api.open-meteo.com/v1/forecast"
	defaultWeatherPoints   = "104.066500,30.572300;102.712300,25.040600"
	defaultFallbackMaxAge  = 6 * time.Hour
	defaultLHASAServiceURL = "https://gis.earthdata.nasa.gov/gis01/rest/services/Landslides/LHASA_Hazard_Today/ImageServer"
	defaultLHASADataDir    = "data/raw/lhasa"
	defaultLHASAStaleAfter = 12 * time.Hour
	defaultGDALBinary      = "gdal"
	defaultAMAPBaseURL     = "https://restapi.amap.com"
	defaultAMAPTimeout     = 15 * time.Second
	defaultLLMProviderName = "Jojocode OpenAI 兼容服务"
	defaultLLMBaseURL      = "https://jojocode.com/v1/chat/completions"
	defaultLLMModel        = "gpt-5.6-terra"
	defaultPastHours       = 72
	defaultForecastHours   = 24
	defaultMaxPoints       = 25
	maxWeatherPoints       = 100
	maxPointsPerRequest    = 25
)

// Config 保存进程启动所需的基础配置。
type Config struct {
	HTTPAddr        string
	Environment     string
	LogLevel        string
	ShutdownTimeout time.Duration
	DatabaseURL     string
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	Refresh         RefreshConfig
	Weather         WeatherConfig
	LHASA           LHASAConfig
	Map             MapConfig
	Search          SearchConfig
	LLM             LLMConfig
}

// RefreshConfig 控制后台数据采集任务的生命周期。
type RefreshConfig struct {
	Enabled  bool
	Interval time.Duration
	Timeout  time.Duration
}

// WeatherConfig 保存 Open-Meteo 采集和回退边界。
type WeatherConfig struct {
	BaseURL             string
	APIKey              string
	Points              []spatial.Point
	PastHours           int
	ForecastHours       int
	FallbackMaxAge      time.Duration
	MaxPointsPerRequest int
}

// LHASAConfig 保存 NASA 风险制品和 GDAL 处理配置。
type LHASAConfig struct {
	ServiceURL   string
	DataDir      string
	StaleAfter   time.Duration
	GDALBinary   string
	TemporaryDir string
}

// MapConfig 保存高德服务端代理的连接和安全配置。
// APIKey 与 SecurityCode 只允许在服务端环境变量中提供，不会下发到浏览器。
type MapConfig struct {
	Enabled      bool
	BaseURL      string
	APIKey       string
	SecurityCode string
	Timeout      time.Duration
}

// SearchConfig 保存博查搜索服务端代理的连接和证据筛选边界。
type SearchConfig struct {
	Enabled        bool
	BaseURL        string
	APIKey         string
	MaxResults     int
	MaxAge         time.Duration
	TrustedDomains []string
}

// LLMConfig 保存 OpenAI 兼容解释性报告客户端配置；核心数值不经过大模型计算。
type LLMConfig struct {
	Enabled             bool
	ProviderName        string
	BaseURL             string
	APIKey              string
	Model               string
	MaxCompletionTokens int
	OutputAttempts      int
}

// Validate 检查博查搜索启用时所需的服务端配置。
func (config SearchConfig) Validate() error { return validateSearch(config) }

// Validate 检查 LLM 启用时所需的服务端配置。
func (config LLMConfig) Validate() error { return validateLLM(config) }

// Validate 检查高德地图启用时所需的服务端配置。
func (config MapConfig) Validate() error {
	return validateMap(config)
}

// Load 从环境变量读取并校验配置。
func Load() (Config, error) {
	base, err := loadBase()
	if err != nil {
		return Config{}, err
	}
	refresh, err := loadRefresh()
	if err != nil {
		return Config{}, err
	}
	weather, err := loadWeather()
	if err != nil {
		return Config{}, err
	}
	lhasa, err := loadLHASA()
	if err != nil {
		return Config{}, err
	}
	mapConfig, err := loadMap()
	if err != nil {
		return Config{}, err
	}
	search, err := loadSearch()
	if err != nil {
		return Config{}, err
	}
	llm, err := loadLLM()
	if err != nil {
		return Config{}, err
	}
	base.Refresh, base.Weather, base.LHASA, base.Map, base.Search, base.LLM = refresh, weather, lhasa, mapConfig, search, llm
	if err = validateRefresh(base); err != nil {
		return Config{}, err
	}
	if err = validateMap(base.Map); err != nil {
		return Config{}, err
	}
	if err = validateSearch(base.Search); err != nil {
		return Config{}, err
	}
	if err = validateLLM(base.LLM); err != nil {
		return Config{}, err
	}
	return base, nil
}

func loadSearch() (SearchConfig, error) {
	enabled, err := boolEnv("BOCHA_ENABLED", false)
	if err != nil {
		return SearchConfig{}, err
	}
	maxResults, err := positiveIntEnv("BOCHA_MAX_RESULTS", 10)
	if err != nil {
		return SearchConfig{}, err
	}
	maxAge, err := durationEnv("BOCHA_MAX_AGE", 72*time.Hour)
	if err != nil {
		return SearchConfig{}, err
	}
	trusted := splitList(stringEnv("BOCHA_TRUSTED_DOMAINS", "gov.cn,mnr.gov.cn,mem.gov.cn,cma.cn,earthdata.nasa.gov"))
	return SearchConfig{Enabled: enabled, BaseURL: stringEnv("BOCHA_BASE_URL", "https://api.bochaai.com/v1/web-search"), APIKey: strings.TrimSpace(os.Getenv("BOCHA_API_KEY")), MaxResults: maxResults, MaxAge: maxAge, TrustedDomains: trusted}, nil
}

func loadLLM() (LLMConfig, error) {
	enabled, err := boolEnv("LLM_ENABLED", false)
	if err != nil {
		return LLMConfig{}, err
	}
	tokens, err := positiveIntEnv("LLM_MAX_COMPLETION_TOKENS", 1200)
	if err != nil {
		return LLMConfig{}, err
	}
	attempts, err := positiveIntEnv("LLM_OUTPUT_ATTEMPTS", 2)
	if err != nil {
		return LLMConfig{}, err
	}
	return LLMConfig{
		Enabled: enabled, ProviderName: stringEnv("LLM_PROVIDER_NAME", defaultLLMProviderName),
		BaseURL: stringEnv("LLM_BASE_URL", defaultLLMBaseURL), APIKey: strings.TrimSpace(os.Getenv("LLM_API_KEY")),
		Model: stringEnv("LLM_MODEL", defaultLLMModel), MaxCompletionTokens: tokens, OutputAttempts: attempts,
	}, nil
}

func loadLHASA() (LHASAConfig, error) {
	staleAfter, err := durationEnv("LHASA_STALE_AFTER", defaultLHASAStaleAfter)
	if err != nil {
		return LHASAConfig{}, err
	}
	return LHASAConfig{
		ServiceURL: stringEnv("LHASA_EARTHDATA_URL", defaultLHASAServiceURL),
		DataDir:    stringEnv("LHASA_DATA_DIR", defaultLHASADataDir), StaleAfter: staleAfter,
		GDALBinary: stringEnv("GDAL_BINARY", defaultGDALBinary), TemporaryDir: os.Getenv("GDAL_TEMP_DIR"),
	}, nil
}

func loadMap() (MapConfig, error) {
	enabled, err := boolEnv("AMAP_ENABLED", false)
	if err != nil {
		return MapConfig{}, err
	}
	timeout, err := durationEnv("AMAP_TIMEOUT", defaultAMAPTimeout)
	if err != nil {
		return MapConfig{}, err
	}
	return MapConfig{
		Enabled:      enabled,
		BaseURL:      stringEnv("AMAP_BASE_URL", defaultAMAPBaseURL),
		APIKey:       strings.TrimSpace(os.Getenv("AMAP_API_KEY")),
		SecurityCode: strings.TrimSpace(os.Getenv("AMAP_JSCODE")),
		Timeout:      timeout,
	}, nil
}

func loadBase() (Config, error) {
	timeout, err := durationEnv("APP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	redisDB, err := intEnv("REDIS_DB", 0)
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPAddr: stringEnv("APP_HTTP_ADDR", defaultHTTPAddr), Environment: stringEnv("APP_ENV", defaultEnvironment),
		LogLevel: stringEnv("APP_LOG_LEVEL", defaultLogLevel), ShutdownTimeout: timeout,
		DatabaseURL: os.Getenv("DATABASE_URL"), RedisAddr: os.Getenv("REDIS_ADDR"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"), RedisDB: redisDB,
	}, nil
}

func loadRefresh() (RefreshConfig, error) {
	enabled, err := boolEnv("REFRESH_ENABLED", false)
	if err != nil {
		return RefreshConfig{}, err
	}
	interval, err := durationEnv("REFRESH_INTERVAL", defaultRefreshInterval)
	if err != nil {
		return RefreshConfig{}, err
	}
	timeout, err := durationEnv("REFRESH_TIMEOUT", defaultRefreshTimeout)
	return RefreshConfig{Enabled: enabled, Interval: interval, Timeout: timeout}, err
}

func loadWeather() (WeatherConfig, error) {
	points, err := pointListEnv("OPEN_METEO_POINTS", defaultWeatherPoints)
	if err != nil {
		return WeatherConfig{}, err
	}
	pastHours, err := positiveIntEnv("OPEN_METEO_PAST_HOURS", defaultPastHours)
	if err != nil {
		return WeatherConfig{}, err
	}
	forecastHours, err := positiveIntEnv("OPEN_METEO_FORECAST_HOURS", defaultForecastHours)
	if err != nil {
		return WeatherConfig{}, err
	}
	maxPoints, err := positiveIntEnv("OPEN_METEO_MAX_POINTS_PER_REQUEST", defaultMaxPoints)
	if err != nil {
		return WeatherConfig{}, err
	}
	if len(points) > maxWeatherPoints || maxPoints > maxPointsPerRequest {
		return WeatherConfig{}, configError("Open-Meteo 点数超过限制：总计 %d、单次 %d", maxWeatherPoints, maxPointsPerRequest)
	}
	maxAge, err := durationEnv("OPEN_METEO_FALLBACK_MAX_AGE", defaultFallbackMaxAge)
	return WeatherConfig{
		BaseURL: stringEnv("OPEN_METEO_BASE_URL", defaultWeatherBaseURL), APIKey: os.Getenv("OPEN_METEO_API_KEY"),
		Points: points, PastHours: pastHours, ForecastHours: forecastHours,
		FallbackMaxAge: maxAge, MaxPointsPerRequest: maxPoints,
	}, err
}

func validateRefresh(config Config) error {
	if !config.Refresh.Enabled {
		return nil
	}
	if config.DatabaseURL == "" {
		return configError("启用刷新时必须配置 DATABASE_URL")
	}
	if config.Refresh.Timeout >= config.Refresh.Interval {
		return configError("REFRESH_TIMEOUT 必须小于 REFRESH_INTERVAL")
	}
	if len(config.Weather.Points) == 0 {
		return configError("启用刷新时必须配置 OPEN_METEO_POINTS")
	}
	return nil
}

func validateMap(config MapConfig) error {
	if !config.Enabled {
		return nil
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return configError("启用高德地图时必须配置 AMAP_BASE_URL")
	}
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil {
		return configError("启用高德地图时 AMAP_BASE_URL 必须是无用户信息的 HTTPS 地址")
	}
	if config.APIKey == "" {
		return configError("启用高德地图时必须配置 AMAP_API_KEY")
	}
	if config.SecurityCode == "" {
		return configError("启用高德地图时必须配置 AMAP_JSCODE")
	}
	if config.Timeout <= 0 {
		return configError("启用高德地图时 AMAP_TIMEOUT 必须为正数时长")
	}
	return nil
}

func validateSearch(config SearchConfig) error {
	if !config.Enabled {
		return nil
	}
	if err := validateHTTPSURL(config.BaseURL, "启用博查搜索时"); err != nil {
		return err
	}
	if config.APIKey == "" {
		return configError("启用博查搜索时必须配置 BOCHA_API_KEY")
	}
	if config.MaxResults <= 0 || config.MaxResults > 50 {
		return configError("BOCHA_MAX_RESULTS 必须在 1 至 50 之间")
	}
	if config.MaxAge <= 0 {
		return configError("BOCHA_MAX_AGE 必须为正数时长")
	}
	if len(config.TrustedDomains) == 0 {
		return configError("BOCHA_TRUSTED_DOMAINS 不能为空")
	}
	return nil
}

func validateLLM(config LLMConfig) error {
	if !config.Enabled {
		return nil
	}
	if strings.TrimSpace(config.ProviderName) == "" || strings.ContainsAny(config.ProviderName, "\r\n") ||
		len([]rune(config.ProviderName)) > 128 {
		return configError("LLM_PROVIDER_NAME 必须是 1 至 128 个字符的单行名称")
	}
	if err := validateHTTPSURL(config.BaseURL, "启用 LLM 时"); err != nil {
		return err
	}
	if config.APIKey == "" {
		return configError("启用 LLM 时必须配置 LLM_API_KEY")
	}
	if strings.TrimSpace(config.Model) == "" {
		return configError("启用 LLM 时必须配置 LLM_MODEL")
	}
	if config.MaxCompletionTokens <= 0 || config.MaxCompletionTokens > 4096 {
		return configError("LLM_MAX_COMPLETION_TOKENS 必须在 1 至 4096 之间")
	}
	if config.OutputAttempts <= 0 || config.OutputAttempts > 3 {
		return configError("LLM_OUTPUT_ATTEMPTS 必须在 1 至 3 之间")
	}
	return nil
}

func validateHTTPSURL(raw, prefix string) error {
	if strings.TrimSpace(raw) == "" {
		return configError("%s 必须配置 HTTPS 地址", prefix)
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return configError("%s 地址必须是无用户信息的 HTTPS 地址", prefix)
	}
	return nil
}

func stringEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, configError("配置 %s 必须是正数时长: %q", name, value)
	}
	return duration, nil
}

func intEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, configError("配置 %s 必须是非负整数: %q", name, value)
	}
	return parsed, nil
}

func positiveIntEnv(name string, fallback int) (int, error) {
	value, err := intEnv(name, fallback)
	if err != nil || value == 0 {
		if err != nil {
			return 0, err
		}
		return 0, configError("配置 %s 必须是正整数", name)
	}
	return value, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, configError("配置 %s 必须是布尔值: %q", name, value)
	}
	return parsed, nil
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func pointListEnv(name, fallback string) ([]spatial.Point, error) {
	value := stringEnv(name, fallback)
	parts := strings.Split(value, ";")
	points := make([]spatial.Point, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		point, key, err := parsePoint(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("%w: 配置 %s: %w", ErrInvalidConfig, name, err)
		}
		if _, exists := seen[key]; exists {
			return nil, configError("配置 %s 包含重复坐标 %s", name, part)
		}
		seen[key], points = struct{}{}, append(points, point)
	}
	return points, nil
}

func parsePoint(value string) (spatial.Point, string, error) {
	longitudeText, latitudeText, ok := strings.Cut(value, ",")
	if !ok || strings.Contains(latitudeText, ",") {
		return spatial.Point{}, "", fmt.Errorf("坐标必须使用 经度,纬度 格式: %q", value)
	}
	longitude, err := strconv.ParseFloat(strings.TrimSpace(longitudeText), 64)
	if err != nil {
		return spatial.Point{}, "", fmt.Errorf("经度无效: %q", longitudeText)
	}
	latitude, err := strconv.ParseFloat(strings.TrimSpace(latitudeText), 64)
	if err != nil {
		return spatial.Point{}, "", fmt.Errorf("纬度无效: %q", latitudeText)
	}
	point := spatial.Point{Longitude: longitude, Latitude: latitude}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || math.IsNaN(latitude) || math.IsInf(latitude, 0) {
		return spatial.Point{}, "", fmt.Errorf("坐标必须是有限数值")
	}
	if err = point.Validate(); err != nil {
		return spatial.Point{}, "", err
	}
	return point, fmt.Sprintf("%.6f,%.6f", canonicalCoordinate(longitude), canonicalCoordinate(latitude)), nil
}

func configError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, arguments...))
}

func canonicalCoordinate(value float64) float64 {
	value = math.Round(value*1_000_000) / 1_000_000
	if value == 0 {
		return 0
	}
	return value
}
