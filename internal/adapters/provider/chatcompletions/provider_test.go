package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestGenerateSendsConstrainedJSONPrompt(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(`"enable_thinking"`)) {
			t.Fatalf("请求包含供应商专用字段: %s", body)
		}
		var request chatRequest
		if err = json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request.ResponseFormat.Type != "json_object" || request.MaxCompletionTokens != 800 {
			t.Fatalf("请求约束 = %+v", request)
		}
		if bytes.Contains(body, []byte(`"max_tokens"`)) ||
			!bytes.Contains(body, []byte(`"max_completion_tokens":800`)) {
			t.Fatalf("请求使用了不兼容的输出 token 字段: %s", body)
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, "不可信数据") ||
			!strings.Contains(request.Messages[1].Content, "riskLevel") ||
			!strings.Contains(request.Messages[1].Content, "json") ||
			!strings.Contains(request.Messages[1].Content, "字符串数组") {
			t.Fatalf("提示词缺少边界 = %+v", request.Messages)
		}
		w.Header().Set("X-Request-ID", "llm-request-1")
		writeJSON(w, validChatResponse())
	}))
	defer server.Close()

	provider := newFixtureProvider(t, server.URL, server.Client(), 800, 1)
	provider.now = func() time.Time { return time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC) }
	result, err := provider.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Model != DefaultModel || result.Source.ProviderRequestID != "llm-request-1" ||
		result.Source.Provider != "测试兼容服务" || result.Source.Dataset != DatasetName ||
		!strings.Contains(result.Source.Citation, "测试兼容服务") {
		t.Fatalf("结果 = %+v", result)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateMinimizesEvidenceBeforeExternalRequest(t *testing.T) {
	input := validInput()
	input.Evidence[0].Title = "救援对象张三"
	input.Evidence[0].Summary = "证件 E12345678，现居成都市某路27号"
	input.Evidence[0].URL = "https://zhangsan-e12345678.example.com/private"
	input.Evidence[0].Source.Provider = "private-provider-E12345678"
	input.Evidence[0].Source.Dataset = "person-address"
	input.Evidence[0].Source.DatasetVersion = "张三"
	input.Evidence[0].Source.License = "成都市武侯区人民南路四段27号"
	input.Evidence[0].Source.ProviderRequestID = "E12345678"
	input.Evidence[0].Source.SourceURI = "https://api.example.test/person/张三?token=secret"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request chatRequest
		if err = json.Unmarshal(body, &request); err != nil || len(request.Messages) != 2 {
			t.Fatalf("LLM 请求结构无效: messages=%d err=%v body=%s", len(request.Messages), err, body)
		}
		prompt := []byte(request.Messages[1].Content)
		for _, forbidden := range []string{
			"张三", "E12345678", "成都市某路27号", "zhangsan-e12345678.example.com",
			"人民南路四段27号", "/private", "token=secret", "private-provider",
			"person-address", "/person/", `"url"`,
		} {
			if bytes.Contains(prompt, []byte(forbidden)) {
				t.Fatalf("LLM 请求泄露原始证据 %q: %s", forbidden, prompt)
			}
		}
		for _, required := range []string{
			minimizedEvidenceTitle, minimizedEvidenceSummary,
			`"reference":"public-evidence-001"`,
			`"auditReference":"sha256:` + strings.Repeat("a", 64) + `"`, minimizedEvidenceSource,
		} {
			if !bytes.Contains(prompt, []byte(required)) {
				t.Fatalf("LLM 请求缺少最小化字段 %q: %s", required, prompt)
			}
		}
		writeJSON(w, validChatResponse())
	}))
	defer server.Close()

	provider := newFixtureProvider(t, server.URL, server.Client(), 800, 1)
	if _, err := provider.Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRejectsNonPublicEvidenceBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeJSON(w, validChatResponse())
	}))
	defer server.Close()
	input := validInput()
	input.Evidence[0].URL = "https://127.0.0.1/private"
	provider := newFixtureProvider(t, server.URL, server.Client(), 800, 1)
	_, err := provider.Generate(context.Background(), input)
	if !errors.Is(err, domain.ErrInvalidInput) || requests != 0 {
		t.Fatalf("非公开证据触发外部请求: requests=%d err=%v", requests, err)
	}
}

func TestGenerateRetriesMalformedOutputAndPreservesProviderError(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeJSON(w, malformedChatResponse())
	}))
	defer server.Close()
	provider := newFixtureProvider(t, server.URL, server.Client(), 800, 2)
	_, err := provider.Generate(context.Background(), validInput())
	if requests != 2 || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestGenerateRetriesOnlyMalformedStructuredOutputThenSucceeds(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			writeJSON(w, malformedChatResponse())
			return
		}
		writeJSON(w, validChatResponse())
	}))
	defer server.Close()
	provider := newFixtureProviderWithHTTPAttempts(t, server.URL, server.Client(), 3, 2)
	result, err := provider.Generate(context.Background(), validInput())
	if err != nil || requests != 2 || !result.Available {
		t.Fatalf("requests=%d result=%+v err=%v", requests, result, err)
	}
}

func TestGenerateDoesNotRetryProviderStatus(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	provider := newFixtureProviderWithHTTPAttempts(t, server.URL, server.Client(), 3, 2)
	_, err := provider.Generate(context.Background(), validInput())
	if requests != 1 || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestGenerateDoesNotRetryConnectionFailure(t *testing.T) {
	requests := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		credential := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		return nil, fmt.Errorf("connection closed after credential=%s", credential)
	})
	provider := newFixtureProviderWithHTTPAttempts(t, "https://llm.example.test/v1", &http.Client{Transport: transport}, 3, 2)
	_, err := provider.Generate(context.Background(), validInput())
	if requests != 1 || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestGenerateDoesNotRetryClientTimeout(t *testing.T) {
	requests := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client := &http.Client{Transport: transport, Timeout: 10 * time.Millisecond}
	provider := newFixtureProviderWithHTTPAttempts(t, "https://llm.example.test/v1", client, 3, 2)
	_, err := provider.Generate(context.Background(), validInput())
	if requests != 1 || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestGenerateDoesNotRetryMalformedProviderEnvelope(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeJSON(w, "not-json")
	}))
	defer server.Close()
	provider := newFixtureProviderWithHTTPAttempts(t, server.URL, server.Client(), 3, 2)
	_, err := provider.Generate(context.Background(), validInput())
	if requests != 1 || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestGeneratePOSTRejectsEveryRedirectWithoutReplaying(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			assertGenerateRedirectDenied(t, status)
		})
	}
}

func assertGenerateRedirectDenied(t *testing.T, status int) {
	t.Helper()
	initial, redirected := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected++
			writeJSON(w, validChatResponse())
			return
		}
		initial++
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(status)
	}))
	defer server.Close()
	provider := newFixtureProviderWithHTTPAttempts(t, server.URL, server.Client(), 3, 2)
	_, err := provider.Generate(context.Background(), validInput())
	if initial != 1 || redirected != 0 || !errors.Is(err, domain.ErrProviderUnavailable) ||
		strings.Contains(err.Error(), "test-key") {
		t.Fatalf("initial=%d redirected=%d err=%v", initial, redirected, err)
	}
}

func TestGenerateRejectsOversizedProviderModel(t *testing.T) {
	for _, size := range []int{maxResponseModelRunes + 1, (1 << 20) - 1024} {
		t.Run(fmt.Sprintf("长度_%d", size), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, chatResponseWithModel(strings.Repeat("m", size)))
			}))
			defer server.Close()
			provider := newFixtureProvider(t, server.URL, server.Client(), 800, 1)
			_, err := provider.Generate(context.Background(), validInput())
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("size=%d err=%v", size, err)
			}
		})
	}
}

func TestGenerateRejectsUnknownOutputFields(t *testing.T) {
	content := "{\"summary\":\"说明\",\"keyFindings\":[],\"actions\":[],\"caveats\":[],\"riskLevel\":\"high\"}"
	if _, err := decodeNarrative(content); err == nil {
		t.Fatal("decodeNarrative() 未拒绝核心字段")
	}
}

func TestGenerateRejectsTrailingJSON(t *testing.T) {
	content := "{\"summary\":\"说明\",\"keyFindings\":[],\"actions\":[],\"caveats\":[]} {\"extra\":true}"
	if _, err := decodeNarrative(content); err == nil {
		t.Fatal("decodeNarrative() 未拒绝尾随 JSON")
	}
}

func TestGenerateRejectsMissingNullAndWrongFieldTypes(t *testing.T) {
	for _, content := range []string{
		`{"summary":"说明","keyFindings":[],"actions":[]}`,
		`{"summary":"说明","keyFindings":[],"actions":[],"caveats":null}`,
		`{"summary":"说明","keyFindings":[],"actions":[],"caveats":"不替代官方预警"}`,
	} {
		if _, err := decodeNarrative(content); err == nil {
			t.Fatalf("decodeNarrative() 未拒绝 %s", content)
		}
	}
}

func TestNewRejectsInsecureEndpointAndMissingKey(t *testing.T) {
	for _, config := range []Config{
		{BaseURL: "http://example.test/chat", APIKey: "key"},
		{BaseURL: "https://example.test/chat"},
		{BaseURL: "https://example.test/chat", APIKey: "key", MaxCompletionTokens: 4097},
		{BaseURL: "https://example.test/chat", APIKey: "key", MaxCompletionTokens: -1},
		{BaseURL: "https://example.test/chat", APIKey: "key", OutputAttempts: 4},
		{BaseURL: "https://example.test/chat", APIKey: "key", OutputAttempts: -1},
	} {
		if _, err := New(nil, config); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("New(%+v) error = %v", config, err)
		}
	}
}

func validInput() report.NarrativeInput {
	return report.NarrativeInput{
		AnalysisJSON:    []byte("{\"riskLevel\":\"high\",\"amountCents\":1200}"),
		ImmutableFields: []string{"riskLevel", "amountCents"},
		Evidence: []report.Evidence{{
			Title: "公开通报", URL: "https://www.mnr.gov.cn/news/1", Summary: "降雨增加",
			Source: provenance.Provenance{
				Provider: "bocha", Dataset: "search", SourceURI: "https://api.bochaai.com/v1/web-search",
				SourceRevision: "sha256:" + strings.Repeat("a", 64), DataKind: provenance.DataKindObservation,
				FetchedAt: time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC),
			},
		}},
	}
}

func newFixtureProvider(t *testing.T, endpoint string, clientHTTP *http.Client,
	tokens, attempts int,
) *Provider {
	t.Helper()
	client := httpclient.New(httpclient.Options{HTTPClient: clientHTTP, MaxAttempts: 1})
	provider, err := New(client, Config{
		ProviderName: "测试兼容服务", BaseURL: endpoint, APIKey: "test-key", Model: DefaultModel,
		MaxCompletionTokens: tokens, OutputAttempts: attempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func newFixtureProviderWithHTTPAttempts(t *testing.T, endpoint string, value *http.Client,
	outputAttempts, httpAttempts int,
) *Provider {
	t.Helper()
	client := httpclient.New(httpclient.Options{HTTPClient: value, MaxAttempts: httpAttempts})
	provider, err := New(client, Config{
		ProviderName: "测试兼容服务", BaseURL: endpoint, APIKey: "test-key", Model: DefaultModel,
		MaxCompletionTokens: 800, OutputAttempts: outputAttempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func validChatResponse() string {
	return chatResponseWithModel(DefaultModel)
}

func chatResponseWithModel(model string) string {
	content, _ := json.Marshal(narrativePayload{
		Summary: "风险资料已完成结构化整理。", KeyFindings: []string{"请复核公开来源。"},
		Actions: []string{"由值班人员确认现场。"}, Caveats: []string{"不替代官方预警。"},
	})
	body, _ := json.Marshal(chatResponse{Model: model, Choices: []chatChoice{{
		FinishReason: "stop", Message: chatMessage{Content: string(content)},
	}}})
	return string(body)
}

func malformedChatResponse() string {
	body, _ := json.Marshal(chatResponse{Choices: []chatChoice{{
		FinishReason: "stop", Message: chatMessage{
			Content: `{"summary":"说明","keyFindings":[],"actions":[],"caveats":"类型错误"}`,
		},
	}}})
	return string(body)
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
