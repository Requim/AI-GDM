package bocha

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

type searchRequest struct {
	Query     string `json:"query"`
	Freshness string `json:"freshness,omitempty"`
	Summary   bool   `json:"summary"`
	Count     int    `json:"count"`
	Include   string `json:"include,omitempty"`
}

// searchItem 覆盖博查兼容 Bing 结构中用于证据审计的字段。
type searchItem struct {
	Name            string `json:"name"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Summary         string `json:"summary"`
	Description     string `json:"description"`
	SiteName        string `json:"siteName"`
	DatePublished   string `json:"datePublished"`
	DateLastCrawled string `json:"dateLastCrawled"`
	PublishedAt     string `json:"publishedAt"`
	CrawledAt       string `json:"crawledAt"`
}

type searchResponse struct {
	Code     int      `json:"code"`
	LogID    string   `json:"log_id"`
	Message  any      `json:"msg"`
	WebPages webPages `json:"webPages"`
	Data     struct {
		WebPages webPages     `json:"webPages"`
		Value    []searchItem `json:"value"`
	} `json:"data"`
	Value []searchItem `json:"value"`
}

type webPages struct {
	Value []searchItem `json:"value"`
}

const (
	maxItemURLRunes       = 2048
	maxItemTitleRunes     = 512
	maxItemSummaryRunes   = 4096
	maxItemSiteRunes      = 256
	maxItemTimestampRunes = 64
	maxItemMetadataBytes  = 32 << 10
	maxResponseItems      = 200
)

func decodeResponse(body []byte) ([]searchItem, string, error) {
	var response searchResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&response); err != nil {
		return nil, "", fmt.Errorf("%w: 博查搜索响应不是合法 JSON", domain.ErrProviderUnavailable)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, response.LogID, fmt.Errorf("%w: 博查搜索响应包含多个 JSON 值", domain.ErrProviderUnavailable)
	}
	if response.Code != 0 && response.Code != http.StatusOK {
		return nil, response.LogID, fmt.Errorf("%w: 博查搜索业务状态 %d", domain.ErrProviderUnavailable, response.Code)
	}
	items := response.WebPages.Value
	if len(items) == 0 {
		items = response.Data.WebPages.Value
	}
	if len(items) == 0 {
		items = response.Data.Value
	}
	if len(items) == 0 {
		items = response.Value
	}
	if len(items) > maxResponseItems {
		return nil, response.LogID, fmt.Errorf("%w: 博查搜索结果条目超过上限", domain.ErrProviderUnavailable)
	}
	return items, response.LogID, nil
}

func validItemMetadata(value searchItem) bool {
	fields := []struct {
		value string
		max   int
	}{
		{value.URL, maxItemURLRunes}, {value.Name, maxItemTitleRunes}, {value.Title, maxItemTitleRunes},
		{value.Snippet, maxItemSummaryRunes}, {value.Summary, maxItemSummaryRunes},
		{value.Description, maxItemSummaryRunes}, {value.SiteName, maxItemSiteRunes},
		{value.DatePublished, maxItemTimestampRunes}, {value.DateLastCrawled, maxItemTimestampRunes},
		{value.PublishedAt, maxItemTimestampRunes}, {value.CrawledAt, maxItemTimestampRunes},
	}
	for _, field := range fields {
		if field.value != "" && (strings.TrimSpace(field.value) == "" || len([]rune(field.value)) > field.max) {
			return false
		}
	}
	payload, err := json.Marshal(value)
	return err == nil && len(payload) <= maxItemMetadataBytes
}

func canonicalHTTPS(raw string) (string, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "gclid" || lower == "fbclid" || isSensitiveQueryKey(lower) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	canonical := parsed.String()
	return canonical, canonical, true
}

func isSensitiveQueryKey(key string) bool {
	switch strings.ToLower(key) {
	case "key", "jscode", "apikey", "api_key", "token", "access_token", "secret":
		return true
	default:
		return false
	}
}

func parseTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func isOutsideWindow(published, crawled time.Time, now time.Time, maxAge time.Duration) bool {
	reference := published
	if reference.IsZero() {
		reference = crawled
	}
	if reference.IsZero() {
		return false
	}
	if reference.After(now.Add(24 * time.Hour)) {
		return true
	}
	return now.Sub(reference) > maxAge
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
