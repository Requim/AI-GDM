package bocha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
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
	if values[0].Source.ProviderRequestID != "search-1" ||
		!strings.HasPrefix(values[0].Source.SourceRevision, "sha256:") || values[0].Source.SHA256 == "" {
		t.Fatalf("Source = %+v", values[0].Source)
	}
	if !strings.Contains(strings.Join(values[0].Source.QualityFlags, "|"),
		report.TrustedDomainQualityFlagPrefix+"mnr.gov.cn") {
		t.Fatalf("可信基域未绑定配置命中项: %+v", values[0].Source.QualityFlags)
	}
	if err = values[0].Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestItemRevisionSeparatesStableIdentityFromResponseAudit(t *testing.T) {
	now := time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC)
	provider := &Provider{trustedDomains: []string{"mnr.gov.cn"}, maxAge: 24 * time.Hour}
	item := searchItem{
		Name: "四川地质灾害公开通报", URL: "https://www.mnr.gov.cn/news/notice-1",
		Summary: "首次搜索摘要", DatePublished: "2026-08-27T03:00:00Z",
		DateLastCrawled: "2026-08-27T03:30:00Z",
	}
	first, _, ok := provider.itemEvidence(item, now, DefaultBaseURL,
		httpclient.Response{FetchedAt: now, RequestID: "batch-1"}, strings.Repeat("a", 64))
	item.DateLastCrawled = "2026-08-27T03:50:00Z"
	second, _, secondOK := provider.itemEvidence(item, now, DefaultBaseURL,
		httpclient.Response{FetchedAt: now, RequestID: "batch-2"}, strings.Repeat("b", 64))
	if !ok || !secondOK || first.Source.SourceRevision != second.Source.SourceRevision ||
		first.Source.SHA256 == second.Source.SHA256 || first.Source.ProviderRequestID == second.Source.ProviderRequestID {
		t.Fatalf("条目身份与批次审计未分离: first=%+v second=%+v", first.Source, second.Source)
	}
	item.Summary = "正文摘要已发生稳定内容变化"
	changed, _, changedOK := provider.itemEvidence(item, now, DefaultBaseURL,
		httpclient.Response{FetchedAt: now, RequestID: "batch-3"}, strings.Repeat("c", 64))
	if !changedOK || changed.Source.SourceRevision == first.Source.SourceRevision {
		t.Fatalf("稳定正文身份变化未生成新条目修订: first=%+v changed=%+v", first.Source, changed.Source)
	}
	identical, _, identicalOK := provider.itemEvidence(item, now, DefaultBaseURL,
		httpclient.Response{FetchedAt: now.Add(time.Minute), RequestID: "batch-4"}, strings.Repeat("d", 64))
	if !identicalOK || identical.Source.SourceRevision != changed.Source.SourceRevision {
		t.Fatalf("完全相同条目未保持稳定修订: changed=%+v identical=%+v", changed.Source, identical.Source)
	}
}

func TestTrustedDomainUsesMostSpecificConfiguredBase(t *testing.T) {
	provider := &Provider{trustedDomains: []string{"gov.cn", "mnr.gov.cn"}}
	domainName, ok := provider.trustedDomain("https://zhangsan-e12345678.mnr.gov.cn/report")
	if !ok || domainName != "mnr.gov.cn" {
		t.Fatalf("trustedDomain() = %q, %t", domainName, ok)
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

func TestSearchSkipsItemsWithOverBudgetMetadata(t *testing.T) {
	now := time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC)
	items := []searchItem{
		{Name: "站点过长", URL: "https://www.mnr.gov.cn/site", Summary: "摘要", SiteName: strings.Repeat("站", 257)},
		{Name: "冗余字段过长", URL: "https://www.mnr.gov.cn/unused", Summary: "摘要", Description: strings.Repeat("x", maxItemSummaryRunes+1)},
		{Name: "有效结果", URL: "https://www.mnr.gov.cn/good", Summary: "有效摘要", SiteName: "自然资源部"},
	}
	server := searchFixtureServer(t, items, "search-1")
	defer server.Close()
	provider := newFixtureProvider(t, server.URL, server.Client(), now, time.Hour)
	values, err := provider.Search(context.Background(), "滑坡", 10)
	if err != nil || len(values) != 1 || values[0].Title != "有效结果" {
		t.Fatalf("Search() = %+v, error = %v", values, err)
	}
}

func TestSearchReturnsNonNilEmptyEvidenceForOversizedRequestID(t *testing.T) {
	items := []searchItem{{Name: "有效结果", URL: "https://www.mnr.gov.cn/good", Summary: "有效摘要"}}
	server := searchFixtureServer(t, items, strings.Repeat("r", 257))
	defer server.Close()
	provider := newFixtureProvider(t, server.URL, server.Client(), time.Now().UTC(), time.Hour)
	values, err := provider.Search(context.Background(), "滑坡", 10)
	if err != nil || values == nil || len(values) != 0 {
		t.Fatalf("Search() = %+v, error = %v", values, err)
	}
}

func TestSearchRejectsResponseBeyondByteBudget(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxBodyBytes+1)))
	}))
	defer server.Close()
	provider := newFixtureProvider(t, server.URL, server.Client(), time.Now().UTC(), time.Hour)
	_, err := provider.Search(context.Background(), "滑坡", 1)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestSearchPOSTDoesNotRetryProviderFailure(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	provider := newFixtureProviderWithAttempts(t, server.URL, server.Client(), 2)
	_, err := provider.Search(context.Background(), "滑坡", 1)
	if requests != 1 || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestSearchPOSTDoesNotRetryConnectionFailure(t *testing.T) {
	requests := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		credential := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		return nil, fmt.Errorf("connection closed after credential=%s", credential)
	})
	provider := newFixtureProviderWithAttempts(t, "https://search.example.test/v1", &http.Client{Transport: transport}, 2)
	_, err := provider.Search(context.Background(), "滑坡", 1)
	if requests != 1 || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestSearchPOSTDoesNotRetryClientTimeout(t *testing.T) {
	requests := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client := &http.Client{Transport: transport, Timeout: 10 * time.Millisecond}
	provider := newFixtureProviderWithAttempts(t, "https://search.example.test/v1", client, 2)
	_, err := provider.Search(context.Background(), "滑坡", 1)
	if requests != 1 || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestSearchPOSTRejectsEveryRedirectWithoutReplaying(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			assertSearchRedirectDenied(t, status)
		})
	}
}

func assertSearchRedirectDenied(t *testing.T, status int) {
	t.Helper()
	initial, redirected := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected++
			writeFixture(w, fixtureSearchResponse())
			return
		}
		initial++
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(status)
	}))
	defer server.Close()
	provider := newFixtureProviderWithAttempts(t, server.URL, server.Client(), 2)
	_, err := provider.Search(context.Background(), "滑坡", 1)
	if initial != 1 || redirected != 0 || !errors.Is(err, domain.ErrProviderUnavailable) ||
		strings.Contains(err.Error(), "test-key") {
		t.Fatalf("initial=%d redirected=%d err=%v", initial, redirected, err)
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

func newFixtureProviderWithAttempts(t *testing.T, endpoint string, value *http.Client, attempts int) *Provider {
	t.Helper()
	client := httpclient.New(httpclient.Options{HTTPClient: value, MaxAttempts: attempts})
	provider, err := New(client, Config{
		BaseURL: endpoint, APIKey: "test-key", MaxAge: time.Hour,
		TrustedDomains: []string{"mnr.gov.cn", "mem.gov.cn"},
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

func writeFixture(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func searchFixtureServer(t *testing.T, items []searchItem, requestID string) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(searchResponse{WebPages: webPages{Value: items}})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", requestID)
		writeFixture(w, string(body))
	}))
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
