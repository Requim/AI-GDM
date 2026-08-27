// Package bocha 实现博查公开搜索 API 的受控适配器。
package bocha

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	// DefaultBaseURL 是博查 Web Search API 的公开兼容端点。
	DefaultBaseURL = "https://api.bochaai.com/v1/web-search"
	ProviderName   = "博查 AI 搜索"
	DatasetName    = "Bocha Web Search"
	defaultMaxAge  = 72 * time.Hour
	defaultResults = 10
	maxResults     = 50
	maxQueryRunes  = 512
	maxBodyBytes   = 2 << 20
)

var defaultTrustedDomains = []string{
	"gov.cn", "mnr.gov.cn", "mem.gov.cn", "cma.cn", "earthdata.nasa.gov",
}

// Config 配置博查端点、搜索时效和可信来源域名。
type Config struct {
	BaseURL        string
	APIKey         string
	MaxResults     int
	MaxAge         time.Duration
	TrustedDomains []string
}

// Provider 把博查响应转换为带时效和来源的领域证据。
type Provider struct {
	client         *httpclient.Client
	endpoint       string
	apiKey         string
	maxResults     int
	maxAge         time.Duration
	trustedDomains []string
	now            func() time.Time
}

var _ ports.EvidenceSearcher = (*Provider)(nil)

// New 创建博查搜索适配器；密钥只保存在服务端内存中。
func New(client *httpclient.Client, config Config) (*Provider, error) {
	endpoint, err := validateEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: 博查搜索密钥不能为空", domain.ErrInvalidInput)
	}
	if client == nil {
		client = httpclient.New(httpclient.Options{})
	}
	maxAge := config.MaxAge
	if maxAge < 0 {
		return nil, fmt.Errorf("%w: 博查搜索时效不能为负数", domain.ErrInvalidInput)
	}
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	resultLimit := config.MaxResults
	if resultLimit < 0 {
		return nil, fmt.Errorf("%w: 博查搜索结果上限不能为负数", domain.ErrInvalidInput)
	}
	if resultLimit <= 0 {
		resultLimit = defaultResults
	}
	if resultLimit > maxResults {
		return nil, fmt.Errorf("%w: 博查搜索结果上限不能超过 %d", domain.ErrInvalidInput, maxResults)
	}
	domains, err := normalizeTrustedDomains(config.TrustedDomains)
	if err != nil {
		return nil, err
	}
	return &Provider{
		client: client, endpoint: endpoint, apiKey: apiKey,
		maxResults: resultLimit, maxAge: maxAge, trustedDomains: domains,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Search 返回可信、去重且未超过本地时效窗口的公开证据。
func (p *Provider) Search(ctx context.Context, query string, limit int) ([]report.Evidence, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("%w: 博查搜索客户端未配置", domain.ErrInvalidInput)
	}
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > maxQueryRunes {
		return nil, fmt.Errorf("%w: 搜索关键词长度无效", domain.ErrInvalidInput)
	}
	limit = p.normalizeLimit(limit)
	body, err := json.Marshal(searchRequest{
		Query: query, Count: limit, Freshness: freshness(p.maxAge), Summary: true,
	})
	if err != nil {
		return nil, fmt.Errorf("编码博查搜索请求: %w", err)
	}
	response, err := p.client.Do(ctx, httpclient.Request{
		Method: http.MethodPost, URL: p.endpoint, Body: body,
		Headers: http.Header{
			"Accept": {"application/json"}, "Authorization": {"Bearer " + p.apiKey},
			"Content-Type": {"application/json"},
		}, MaxBodyBytes: maxBodyBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("请求博查搜索: %w", err)
	}
	items, logID, err := decodeResponse(response.Body)
	if err != nil {
		return nil, err
	}
	if logID != "" && response.RequestID == "" {
		response.RequestID = logID
	}
	return p.toEvidence(items, response, limit), nil
}

func (p *Provider) normalizeLimit(value int) int {
	if value <= 0 || value > p.maxResults {
		return p.maxResults
	}
	return value
}

func (p *Provider) toEvidence(items []searchItem, response httpclient.Response,
	limit int,
) []report.Evidence {
	if len(items) == 0 {
		return []report.Evidence{}
	}
	now := p.now().UTC()
	digest := sha256.Sum256(response.Body)
	sourceURI := httpclient.RedactURL(p.endpoint)
	result := make([]report.Evidence, 0, minInt(limit, len(items)))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		evidence, key, ok := p.itemEvidence(item, now, sourceURI, response, hex.EncodeToString(digest[:]))
		if !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, evidence)
		if len(result) == limit {
			break
		}
	}
	return result
}

func (p *Provider) itemEvidence(item searchItem, now time.Time, sourceURI string,
	response httpclient.Response, responseSHA string,
) (report.Evidence, string, bool) {
	link, key, ok := canonicalHTTPS(item.URL)
	if !ok || !p.isTrusted(link) {
		return report.Evidence{}, "", false
	}
	title := strings.TrimSpace(firstNonEmpty(item.Name, item.Title))
	summary := strings.TrimSpace(firstNonEmpty(item.Summary, item.Snippet, item.Description))
	if title == "" || summary == "" {
		return report.Evidence{}, "", false
	}
	published := parseTimestamp(firstNonEmpty(item.DatePublished, item.PublishedAt))
	crawled := parseTimestamp(firstNonEmpty(item.DateLastCrawled, item.CrawledAt))
	if isOutsideWindow(published, crawled, now, p.maxAge) {
		return report.Evidence{}, "", false
	}
	fetched := response.FetchedAt.UTC()
	return report.Evidence{
		Title: title, URL: link, Summary: summary, SiteName: strings.TrimSpace(item.SiteName),
		CrawledAt: crawled,
		Source: provenance.Provenance{
			Provider: ProviderName, Dataset: DatasetName, DatasetVersion: "v1",
			SourceRevision: responseSHA, SourceURI: sourceURI,
			Citation: "博查开放平台 Web Search API", License: "遵循供应商服务条款",
			DataKind: provenance.DataKindObservation, PublishedAt: published,
			FetchedAt: fetched, ValidFrom: fetched, ValidTo: fetched.Add(p.maxAge),
			TemporalResolution: "按需搜索", ProviderRequestID: response.RequestID,
			QualityFlags: []string{"trusted_domain", "deduplicated", "freshness_filtered"},
		},
	}, key, true
}

func validateEndpoint(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: 博查搜索地址必须是无用户信息的 HTTPS 地址", domain.ErrInvalidInput)
	}
	return parsed.String(), nil
}

func normalizeTrustedDomains(values []string) ([]string, error) {
	if len(values) == 0 {
		values = defaultTrustedDomains
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
		if value == "" || strings.ContainsAny(value, "/:@") {
			return nil, fmt.Errorf("%w: 可信来源域名无效", domain.ErrInvalidInput)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: 可信来源域名不能为空", domain.ErrInvalidInput)
	}
	return result, nil
}

func (p *Provider) isTrusted(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, domainName := range p.trustedDomains {
		if host == domainName || strings.HasSuffix(host, "."+domainName) {
			return true
		}
	}
	return false
}

func freshness(maxAge time.Duration) string {
	switch {
	case maxAge <= 24*time.Hour:
		return "oneDay"
	case maxAge <= 7*24*time.Hour:
		return "oneWeek"
	case maxAge <= 31*24*time.Hour:
		return "oneMonth"
	case maxAge <= 366*24*time.Hour:
		return "oneYear"
	default:
		return "noLimit"
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
