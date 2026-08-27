package qwen

import (
	"context"
	"encoding/json"
	"errors"
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
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ResponseFormat.Type != "json_object" || request.EnableThinking || request.MaxTokens != 800 {
			t.Fatalf("请求约束 = %+v", request)
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, "不可信数据") ||
			!strings.Contains(request.Messages[1].Content, "riskLevel") {
			t.Fatalf("提示词缺少边界 = %+v", request.Messages)
		}
		w.Header().Set("X-Request-ID", "qwen-request-1")
		writeJSON(w, validChatResponse())
	}))
	defer server.Close()

	provider := newFixtureProvider(t, server.URL, server.Client(), 800, 1)
	provider.now = func() time.Time { return time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC) }
	result, err := provider.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Model != "qwen-plus" || result.Source.ProviderRequestID != "qwen-request-1" {
		t.Fatalf("结果 = %+v", result)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
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
				DataKind:  provenance.DataKindObservation,
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
		BaseURL: endpoint, APIKey: "test-key", Model: "qwen-plus",
		MaxCompletionTokens: tokens, OutputAttempts: attempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func validChatResponse() string {
	content, _ := json.Marshal(narrativePayload{
		Summary: "风险资料已完成结构化整理。", KeyFindings: []string{"请复核公开来源。"},
		Actions: []string{"由值班人员确认现场。"}, Caveats: []string{"不替代官方预警。"},
	})
	body, _ := json.Marshal(chatResponse{Model: "qwen-plus", Choices: []chatChoice{{
		FinishReason: "stop", Message: chatMessage{Content: string(content)},
	}}})
	return string(body)
}

func malformedChatResponse() string {
	body, _ := json.Marshal(chatResponse{Choices: []chatChoice{{
		FinishReason: "stop", Message: chatMessage{Content: "不是 JSON"},
	}}})
	return string(body)
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
