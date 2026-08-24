package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	defaultHTTPAddr        = ":8080"
	defaultEnvironment     = "development"
	defaultLogLevel        = "info"
	defaultShutdownTimeout = 10 * time.Second
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
}

// Load 从环境变量读取并校验配置。
func Load() (Config, error) {
	timeout, err := durationEnv("APP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	redisDB, err := intEnv("REDIS_DB", 0)
	if err != nil {
		return Config{}, err
	}

	return Config{
		HTTPAddr:        stringEnv("APP_HTTP_ADDR", defaultHTTPAddr),
		Environment:     stringEnv("APP_ENV", defaultEnvironment),
		LogLevel:        stringEnv("APP_LOG_LEVEL", defaultLogLevel),
		ShutdownTimeout: timeout,
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		RedisAddr:       os.Getenv("REDIS_ADDR"),
		RedisPassword:   os.Getenv("REDIS_PASSWORD"),
		RedisDB:         redisDB,
	}, nil
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
		return 0, fmt.Errorf("配置 %s 必须是正数时长: %q", name, value)
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
		return 0, fmt.Errorf("配置 %s 必须是非负整数: %q", name, value)
	}
	return parsed, nil
}
