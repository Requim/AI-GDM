package chatcompletions

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestLiveGenerate(t *testing.T) {
	if os.Getenv("LLM_LIVE_TEST") != "1" {
		t.Skip("未启用真实 LLM 契约测试")
	}
	providerName := envOrDefault("LLM_PROVIDER_NAME", DefaultProviderName)
	requestedModel := requiredLiveEnv(t, "LLM_MODEL")
	provider, err := New(liveHTTPClient(), Config{
		ProviderName:        providerName,
		BaseURL:             requiredLiveEnv(t, "LLM_BASE_URL"),
		APIKey:              requiredLiveEnv(t, "LLM_API_KEY"),
		Model:               requestedModel,
		MaxCompletionTokens: liveIntEnv(t, "LLM_MAX_COMPLETION_TOKENS", defaultTokens),
		OutputAttempts:      liveIntEnv(t, "LLM_OUTPUT_ATTEMPTS", defaultAttempts),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := provider.Generate(ctx, report.NarrativeInput{
		AnalysisJSON:    []byte(`{"hazardType":"landslide","riskLevel":"high","dataStatus":"fresh"}`),
		ImmutableFields: []string{"hazardType", "riskLevel", "dataStatus"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Model != requestedModel || result.Source.Provider != providerName {
		t.Fatalf("真实 LLM 返回的审计元数据无效")
	}
}

func liveHTTPClient() *httpclient.Client {
	return httpclient.New(httpclient.Options{
		HTTPClient:  &http.Client{Timeout: 90 * time.Second},
		MaxAttempts: 1,
	})
}

func requiredLiveEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("真实 LLM 契约测试缺少环境变量 %s", name)
	}
	return value
}

func liveIntEnv(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("环境变量 %s 不是整数", name)
	}
	return value
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
