package bocha

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
)

func TestSearchFiltersTrustedFreshAndDuplicateResults(t *testing.T) {
	now := time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		var request searchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Freshness != "oneDay" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Request-ID", "search-1")
		writeFixture(w, fixtureSearchResponse())
	}))
	defer server.Close()
	provider := newFixtureProvider(t, server.URL, server.Client(), now, 24*time.Hour)
	values, err := provider.Search(context.Background(), "四川 滑坡", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || strings.Contains(values[0].URL, "utm_source") {
		t.Fatalf("Search() = %+v", values)
	}
	if values[0].Source.ProviderRequestID != "search-1" || values[0].Source.SourceRevision == "" {
		t.Fatalf("Source = %+v", values[0].Source)
	}
	if err = values[0].Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRejectsInvalidQueryBeforeRequest(t *testing.T) {
	provider := &Provider{maxResults: 10}
	_, err := provider.Search(context.Background(), " ", 1)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestNewRejectsInsecureEndpointAndMissingKey(t *testing.T) {
	for _, config := range []Config{{BaseURL: "http://example.test/search", APIKey: "key"}, {BaseURL: "https://example.test/search"}, {BaseURL: "https://example.test/search", APIKey: "key", MaxResults: 51}, {BaseURL: "https://example.test/search", APIKey: "key", MaxResults: -1}, {BaseURL: "https://example.test/search", APIKey: "key", MaxAge: -time.Hour}} {
		if _, err := New(nil, config); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("New(%+v) error = %v", config, err)
		}
	}
}

func TestSearchMapsMalformedResponseToProviderUnavailable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not-json")) }))
	defer server.Close()
	provider := newFixtureProvider(t, server.URL, server.Client(), time.Now().UTC(), time.Hour)
	_, err := provider.Search(context.Background(), "滑坡", 1)
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestSearchRejectsTrailingJSON(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{\"webPages\":{\"value\":[]}} {\"unexpected\":true}"))
	}))
	defer server.Close()
	provider := newFixtureProvider(t, server.URL, server.Client(), time.Now().UTC(), time.Hour)
	_, err := provider.Search(context.Background(), "滑坡", 1)
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Search() error = %v", err)
	}
}

func newFixtureProvider(t *testing.T, endpoint string, httpClient *http.Client, now time.Time, maxAge time.Duration) *Provider {
	t.Helper()
	client := httpclient.New(httpclient.Options{HTTPClient: httpClient, MaxAttempts: 1})
	provider, err := New(client, Config{BaseURL: endpoint, APIKey: "test-key", MaxAge: maxAge, TrustedDomains: []string{"mnr.gov.cn", "mem.gov.cn"}})
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return now }
	return provider
}

func writeFixture(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func fixtureSearchResponse() string {
	body, _ := json.Marshal(searchResponse{WebPages: webPages{Value: []searchItem{
		{Name: "国土部门通报", URL: "https://news.mnr.gov.cn/item?id=1&utm_source=x", Summary: "滑坡巡查信息", SiteName: "自然资源部", DatePublished: "2026-08-27T02:00:00Z"},
		{Name: "重复结果", URL: "https://news.mnr.gov.cn/item?id=1", Summary: "重复摘要", DatePublished: "2026-08-27T02:00:00Z"},
		{Name: "不可信来源", URL: "https://example.com/item", Summary: "不应返回", DatePublished: "2026-08-27T02:00:00Z"},
		{Name: "过期结果", URL: "https://www.mem.gov.cn/old", Summary: "过期", DatePublished: "2026-08-20T02:00:00Z"},
	}}})
	return string(body)
}
