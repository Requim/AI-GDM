package lhasa

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	DefaultBaseURL = "https://maps.nccs.nasa.gov/download/landslides"
	datasetName    = "LHASA NRT Hazard"
	datasetVersion = "2.1.1"
	filenameLayout = "20060102T1504"
)

var tifPattern = regexp.MustCompile(`^(\d{8}T\d{4})\.tif$`)

// Config 配置 NASA LHASA 近实时目录和过期阈值。
type Config struct {
	BaseURL    string
	StaleAfter time.Duration
}

// Provider 动态发现 LHASA 近实时 GeoTIFF 制品。
type Provider struct {
	client     *httpclient.Client
	baseURL    string
	staleAfter time.Duration
	now        func() time.Time
}

var _ ports.ArtifactDiscovery = (*Provider)(nil)

// New 创建 LHASA 目录发现适配器。
func New(client *httpclient.Client, config Config) *Provider {
	if config.BaseURL == "" {
		config.BaseURL = DefaultBaseURL
	}
	if config.StaleAfter <= 0 {
		config.StaleAfter = 12 * time.Hour
	}
	return &Provider{
		client: client, baseURL: strings.TrimRight(config.BaseURL, "/"), staleAfter: config.StaleAfter,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// DiscoverLatest 解析目录并返回时间戳最新的 GeoTIFF。
func (p *Provider) DiscoverLatest(ctx context.Context) (provenance.Artifact, error) {
	directoryURL := p.baseURL + "/nrt/hazard/tif/"
	response, err := p.client.Do(ctx, httpclient.Request{
		Method: http.MethodGet, URL: directoryURL, CacheKey: "provider:lhasa:directory:tif",
		CacheTTL: 5 * time.Minute, MaxBodyBytes: 2 << 20,
	})
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("读取 LHASA 目录: %w", err)
	}
	candidates, err := parseDirectory(response.Body, directoryURL)
	if err != nil {
		return provenance.Artifact{}, err
	}
	if len(candidates) == 0 {
		return provenance.Artifact{}, fmt.Errorf("%w: LHASA 目录没有时间戳 GeoTIFF", domain.ErrProviderUnavailable)
	}
	latest, found := latestUsable(candidates, p.now())
	if !found {
		return provenance.Artifact{}, fmt.Errorf("%w: LHASA 目录只有未来时间戳文件", domain.ErrProviderUnavailable)
	}
	return p.artifact(latest, response.FetchedAt), nil
}

type candidate struct {
	url        string
	observedAt time.Time
}

func parseDirectory(content []byte, directoryURL string) ([]candidate, error) {
	document, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("解析 LHASA 目录 HTML: %w", err)
	}
	base, err := url.Parse(directoryURL)
	if err != nil {
		return nil, fmt.Errorf("解析 LHASA 目录地址: %w", err)
	}
	values := make([]candidate, 0)
	walkLinks(document, func(href string) {
		if value, ok := parseCandidate(base, href); ok {
			values = append(values, value)
		}
	})
	sort.Slice(values, func(i, j int) bool { return values[i].observedAt.Before(values[j].observedAt) })
	return deduplicate(values), nil
}

func walkLinks(node *html.Node, visit func(string)) {
	if node.Type == html.ElementNode && node.Data == "a" {
		for _, attribute := range node.Attr {
			if attribute.Key == "href" {
				visit(attribute.Val)
				break
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkLinks(child, visit)
	}
}

func parseCandidate(base *url.URL, href string) (candidate, bool) {
	reference, err := url.Parse(href)
	if err != nil {
		return candidate{}, false
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != base.Scheme || !strings.EqualFold(resolved.Host, base.Host) {
		return candidate{}, false
	}
	if resolved.RawQuery != "" || resolved.Fragment != "" || path.Dir(path.Clean(resolved.Path)) != path.Clean(base.Path) {
		return candidate{}, false
	}
	name := path.Base(resolved.Path)
	matches := tifPattern.FindStringSubmatch(name)
	if len(matches) != 2 {
		return candidate{}, false
	}
	observedAt, err := time.ParseInLocation(filenameLayout, matches[1], time.UTC)
	if err != nil {
		return candidate{}, false
	}
	return candidate{url: resolved.String(), observedAt: observedAt}, true
}

func deduplicate(values []candidate) []candidate {
	result := make([]candidate, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value.url]; exists {
			continue
		}
		seen[value.url] = struct{}{}
		result = append(result, value)
	}
	return result
}

func latestUsable(values []candidate, now time.Time) (candidate, bool) {
	latestAllowed := now.Add(time.Hour)
	for index := len(values) - 1; index >= 0; index-- {
		if !values[index].observedAt.After(latestAllowed) {
			return values[index], true
		}
	}
	return candidate{}, false
}

func (p *Provider) artifact(value candidate, fetchedAt time.Time) provenance.Artifact {
	now := p.now()
	qualityFlags := []string{}
	if value.observedAt.After(now.Add(time.Hour)) {
		qualityFlags = append(qualityFlags, "future_timestamp")
	}
	return provenance.Artifact{
		Reference: value.url, MediaType: "image/tiff",
		Provenance: provenance.Provenance{
			Provider: "NASA", Dataset: datasetName, DatasetVersion: datasetVersion,
			SourceURI: value.url, Citation: "NASA LHASA 2.1.1",
			DataKind: provenance.DataKindNowcast, ObservedAt: value.observedAt, FetchedAt: fetchedAt,
			ValidFrom: value.observedAt, ValidTo: value.observedAt.Add(p.staleAfter),
			SpatialResolution: "30 arc-second (~1 km)", TemporalResolution: "通常每日约4次，best-effort",
			CRS: "EPSG:4326", Stale: now.Sub(value.observedAt) > p.staleAfter,
			QualityFlags: qualityFlags, Limitations: []string{
				"全球模型估计，不是中国官方地质灾害预警",
				"主要描述降雨触发滑坡概率，不能覆盖所有泥石流或实际滑坡边界",
			},
		},
	}
}
