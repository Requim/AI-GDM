package chatcompletions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/report"
)

func TestProbeLiveStructuredClassifiesOnlyExactUpstreamError(t *testing.T) {
	valid := `{"model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"summary\":\"ok\",\"keyFindings\":[],\"actions\":[],\"caveats\":[]}"}}]}`
	cases := []struct {
		name     string
		status   int
		body     string
		redirect bool
		degraded bool
		wantErr  bool
	}{
		{"generated", 200, valid, false, false, false},
		{"exact upstream", 400, `{"error":{"message":"Upstream request failed","type":"upstream_error","code":null}}`, false, true, false},
		{"upstream code", 400, `{"error":{"message":"Upstream request failed","type":"upstream_error","code":"upstream_error"}}`, false, true, false},
		{"invalid key", 401, `{"error":{"type":"invalid_api_key"}}`, false, false, true},
		{"model missing", 400, `{"error":{"type":"model_not_found"}}`, false, false, true},
		{"contradictory invalid key", 400, `{"error":{"message":"Upstream request failed","type":"upstream_error","code":"invalid_api_key"}}`, false, false, true},
		{"contradictory model", 400, `{"error":{"message":"Upstream request failed","type":"upstream_error","code":"model_not_found"}}`, false, false, true},
		{"duplicate type", 400, `{"error":{"message":"Upstream request failed","type":"invalid_api_key","type":"upstream_error"}}`, false, false, true},
		{"duplicate message", 400, `{"error":{"message":"other","message":"Upstream request failed","type":"upstream_error"}}`, false, false, true},
		{"duplicate code", 400, `{"error":{"message":"Upstream request failed","type":"upstream_error","code":null,"code":"upstream_error"}}`, false, false, true},
		{"type alias", 400, `{"error":{"message":"Upstream request failed","Type":"upstream_error"}}`, false, false, true},
		{"code alias", 400, `{"error":{"message":"Upstream request failed","type":"upstream_error","Code":null}}`, false, false, true},
		{"unknown bad request", 400, `{"error":{"type":"upstream_error","message":"different"}}`, false, false, true},
		{"provider failure", 503, `{"error":{"type":"upstream_error","message":"Upstream request failed"}}`, false, false, true},
		{"malformed success", 200, `{`, false, false, true},
		{"redirect", 307, ``, true, false, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server, calls, sinkCalls := newLiveProbeServer(t, test.status, test.body, test.redirect)
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			provider := &Provider{endpoint: server.URL + "/v1/chat/completions", apiKey: "test-key",
				model: "test-model", maxCompletionTokens: 1200}
			degraded, err := probeLiveStructured(context.Background(), client, provider, validLiveInput())
			if degraded != test.degraded || (err != nil) != test.wantErr {
				t.Fatalf("分类错误: degraded=%v error=%v", degraded, err)
			}
			if *calls != 1 || *sinkCalls != 0 {
				t.Fatalf("在线结构化 POST 次数无效: calls=%d sink=%d", *calls, *sinkCalls)
			}
		})
	}
}

func TestRequireLiveModelRejectsAuthMissingAndRedirect(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		redirect bool
		wantErr  bool
	}{
		{"present", 200, `{"data":[{"id":"test-model"}]}`, false, false},
		{"unauthorized", 401, `{"error":"unauthorized"}`, false, true},
		{"forbidden", 403, `{"error":"forbidden"}`, false, true},
		{"missing", 200, `{"data":[{"id":"other-model"}]}`, false, true},
		{"malformed", 200, `{`, false, true},
		{"redirect", 302, ``, true, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server, calls, sinkCalls := newLiveProbeServer(t, test.status, test.body, test.redirect)
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			err := requireLiveModel(context.Background(), client,
				server.URL+"/v1/chat/completions", "test-key", "test-model")
			if (err != nil) != test.wantErr || *calls != 1 || *sinkCalls != 0 {
				t.Fatalf("模型目录分类错误: error=%v calls=%d sink=%d", err, *calls, *sinkCalls)
			}
		})
	}
}

func newLiveProbeServer(t *testing.T, status int, body string, redirect bool) (*httptest.Server, *int, *int) {
	t.Helper()
	calls, sinkCalls := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/sink" {
			sinkCalls++
			writer.WriteHeader(http.StatusOK)
			return
		}
		calls++
		if request.Header.Get("Authorization") != "Bearer test-key" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodPost {
			assertLiveRequestShape(t, request)
		}
		if redirect {
			http.Redirect(writer, request, "/sink", status)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	return server, &calls, &sinkCalls
}

func assertLiveRequestShape(t *testing.T, request *http.Request) {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Errorf("生产同形 LLM 请求不是合法 JSON: %v", err)
		return
	}
	format, ok := payload["response_format"].(map[string]any)
	messages, messagesOK := payload["messages"].([]any)
	if !ok || !messagesOK || payload["model"] != "test-model" ||
		payload["max_completion_tokens"] != float64(1200) || format["type"] != "json_object" ||
		len(messages) != 2 {
		t.Errorf("生产同形 LLM 请求字段漂移: %+v", payload)
	}
}

func validLiveInput() report.NarrativeInput {
	return report.NarrativeInput{
		AnalysisJSON:    []byte(`{"hazardType":"landslide","riskLevel":"high","dataStatus":"fresh"}`),
		ImmutableFields: []string{"hazardType", "riskLevel", "dataStatus"},
		Evidence:        []report.Evidence{},
	}
}

func TestDoLiveRequestRejectsOversizedBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(strings.Repeat("x", maxBodyBytes+1)))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Second
	_, _, err := doLiveRequest(context.Background(), client, http.MethodGet, server.URL, "test-key", nil)
	if err == nil {
		t.Fatal("超量 LLM 在线响应未被拒绝")
	}
}
