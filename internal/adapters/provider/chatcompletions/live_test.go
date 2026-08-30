package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

const (
	liveRecoveredMarker = "LLM_LIVE_RECOVERED"
	liveDegradedMarker  = "LLM_UPSTREAM_DEGRADED"
)

func TestLiveGenerate(t *testing.T) {
	if os.Getenv("LLM_LIVE_TEST") != "1" {
		t.Skip("未启用真实 LLM 契约测试")
	}
	provider, providerName, requestedModel := newLiveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := generateLiveNarrative(ctx, provider)
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

func TestLiveProviderState(t *testing.T) {
	if os.Getenv("LLM_LIVE_TEST") != "1" {
		t.Skip("未启用真实 LLM 契约测试")
	}
	provider, _, requestedModel := newLiveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	baseURL, apiKey := requiredLiveEnv(t, "LLM_BASE_URL"), requiredLiveEnv(t, "LLM_API_KEY")
	rawClient := liveRawHTTPClient()
	if err := requireLiveModel(ctx, rawClient, baseURL, apiKey, requestedModel); err != nil {
		t.Fatal(err)
	}
	input := report.NarrativeInput{
		AnalysisJSON:    []byte(`{"hazardType":"landslide","riskLevel":"high","dataStatus":"fresh"}`),
		ImmutableFields: []string{"hazardType", "riskLevel", "dataStatus"},
	}
	degraded, err := probeLiveStructured(ctx, rawClient, provider, input)
	if err != nil {
		t.Fatal(err)
	}
	if degraded {
		t.Log(liveDegradedMarker)
		return
	}
	t.Log(liveRecoveredMarker)
}

func newLiveProvider(t *testing.T) (*Provider, string, string) {
	t.Helper()
	providerName := envOrDefault("LLM_PROVIDER_NAME", DefaultProviderName)
	requestedModel := requiredLiveEnv(t, "LLM_MODEL")
	provider, err := New(liveHTTPClient(), Config{
		ProviderName: providerName, BaseURL: requiredLiveEnv(t, "LLM_BASE_URL"),
		APIKey: requiredLiveEnv(t, "LLM_API_KEY"), Model: requestedModel,
		MaxCompletionTokens: liveIntEnv(t, "LLM_MAX_COMPLETION_TOKENS", defaultTokens),
		OutputAttempts:      liveIntEnv(t, "LLM_OUTPUT_ATTEMPTS", defaultAttempts),
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider, providerName, requestedModel
}

func generateLiveNarrative(ctx context.Context, provider *Provider) (report.Narrative, error) {
	return provider.Generate(ctx, report.NarrativeInput{
		AnalysisJSON:    []byte(`{"hazardType":"landslide","riskLevel":"high","dataStatus":"fresh"}`),
		ImmutableFields: []string{"hazardType", "riskLevel", "dataStatus"},
	})
}

func requireLiveModel(ctx context.Context, client *http.Client, baseURL, apiKey, model string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil || !strings.HasSuffix(parsed.Path, "/chat/completions") {
		return fmt.Errorf("LLM 模型目录地址无效")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/chat/completions") + "/models"
	status, body, err := doLiveRequest(ctx, client, http.MethodGet, parsed.String(), apiKey, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("LLM 模型目录返回 HTTP %d", status)
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &catalog); err != nil {
		return fmt.Errorf("LLM 模型目录不是合法 JSON: %w", err)
	}
	for _, item := range catalog.Data {
		if item.ID == model {
			return nil
		}
	}
	return fmt.Errorf("LLM 模型目录未包含所配置模型")
}

func probeLiveStructured(ctx context.Context, client *http.Client, provider *Provider,
	input report.NarrativeInput,
) (bool, error) {
	request, err := buildRequest(provider.model, provider.maxCompletionTokens, input)
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return false, err
	}
	status, responseBody, err := doLiveRequest(ctx, client, http.MethodPost, provider.endpoint, provider.apiKey, body)
	if err != nil {
		return false, err
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		content, model, finishReason, decodeErr := decodeResponse(responseBody)
		if decodeErr != nil {
			return false, decodeErr
		}
		if finishReason == "length" || finishReason == "content_filter" {
			return false, fmt.Errorf("LLM 在线结构化请求完成原因 %q", finishReason)
		}
		if model != "" && model != provider.model {
			return false, fmt.Errorf("LLM 在线响应模型与配置不一致")
		}
		if _, decodeErr = decodeNarrative(content); decodeErr != nil {
			return false, fmt.Errorf("LLM 在线结构化输出无效: %w", decodeErr)
		}
		return false, nil
	}
	if status == http.StatusBadRequest && isLiveUpstreamError(responseBody) {
		return true, nil
	}
	return false, fmt.Errorf("LLM 最小请求返回未识别状态 HTTP %d", status)
}

func isLiveUpstreamError(payload []byte) bool {
	root, err := decodeLiveObject(payload, map[string]struct{}{"error": {}})
	if err != nil || len(root) != 1 {
		return false
	}
	fields, err := decodeLiveObject(root["error"], map[string]struct{}{
		"message": {}, "type": {}, "param": {}, "code": {},
	})
	if err != nil || !liveStringEquals(fields["message"], "Upstream request failed") ||
		!liveStringEquals(fields["type"], "upstream_error") || !liveNullableString(fields["param"], true) {
		return false
	}
	code, exists := fields["code"]
	return !exists || string(bytes.TrimSpace(code)) == "null" || liveStringEquals(code, "upstream_error")
}

func decodeLiveObject(payload []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("LLM 错误包不是 JSON 对象")
	}
	values := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		key, keyOK := keyToken.(string)
		if tokenErr != nil || !keyOK {
			return nil, fmt.Errorf("LLM 错误包字段名无效")
		}
		if _, known := allowed[key]; !known {
			return nil, fmt.Errorf("LLM 错误包包含未知字段")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("LLM 错误包字段重复")
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("LLM 错误包字段无效: %w", err)
		}
		values[key] = raw
	}
	if _, err = decoder.Token(); err != nil {
		return nil, fmt.Errorf("LLM 错误包未闭合: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("LLM 错误包包含多个 JSON 值")
	}
	return values, nil
}

func liveStringEquals(raw json.RawMessage, expected string) bool {
	var value string
	return len(raw) != 0 && json.Unmarshal(raw, &value) == nil && value == expected
}

func liveNullableString(raw json.RawMessage, optional bool) bool {
	if len(raw) == 0 {
		return optional
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func doLiveRequest(ctx context.Context, client *http.Client, method, endpoint, apiKey string,
	body []byte,
) (int, []byte, error) {
	if client == nil {
		return 0, nil, fmt.Errorf("LLM 在线合同 HTTP 客户端为空")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("请求 LLM 在线合同: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil || len(payload) > maxBodyBytes {
		return 0, nil, fmt.Errorf("读取 LLM 在线合同响应失败或超量")
	}
	return response.StatusCode, payload, nil
}

func liveRawHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       90 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
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
