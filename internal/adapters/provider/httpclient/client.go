package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	defaultMaxAttempts = 3
	defaultMaxBody     = 4 << 20
	defaultUserAgent   = "AI-GDM/0.1"
)

// RedirectPolicy 约束单次供应商请求的重定向边界。
type RedirectPolicy string

const (
	// RedirectDefault 沿用调用方 HTTP 客户端的既有策略。
	RedirectDefault RedirectPolicy = ""
	// RedirectDeny 拒绝任何重定向，适用于不可重放的创建请求。
	RedirectDeny RedirectPolicy = "deny"
	// RedirectSameOriginHTTPS 仅允许最多五跳、同源、公开 HTTPS 地址。
	RedirectSameOriginHTTPS RedirectPolicy = "same_origin_https"
)

// Request 描述一次受控的外部 HTTP 请求。
type Request struct {
	Method             string
	URL                string
	Headers            http.Header
	Body               []byte
	CacheKey           string
	CacheTTL           time.Duration
	MaxBodyBytes       int64
	MaxAttempts        int
	RedirectPolicy     RedirectPolicy
	SensitiveQueryKeys []string
}

// Response 保存成功响应和可审计元数据。
type Response struct {
	StatusCode int         `json:"statusCode"`
	Header     http.Header `json:"header"`
	Body       []byte      `json:"body"`
	FetchedAt  time.Time   `json:"fetchedAt"`
	RequestID  string      `json:"requestId,omitempty"`
	FromCache  bool        `json:"fromCache"`
}

// Options 配置共用 HTTP 客户端的安全和可靠性边界。
type Options struct {
	HTTPClient  *http.Client
	Cache       ports.Cache
	Limiter     *rate.Limiter
	Logger      *slog.Logger
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBody     int64
	UserAgent   string
	AllowHTTP   bool
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

// Client 对外部供应商请求统一执行限流、重试、缓存和脱敏日志。
type Client struct {
	httpClient  *http.Client
	cache       ports.Cache
	limiter     *rate.Limiter
	logger      *slog.Logger
	maxAttempts int
	baseBackoff time.Duration
	maxBody     int64
	userAgent   string
	allowHTTP   bool
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
}

// New 创建外部供应商共用 HTTP 客户端。
func New(options Options) *Client {
	applyDefaults(&options)
	return &Client{
		httpClient: options.HTTPClient, cache: options.Cache, limiter: options.Limiter,
		logger: options.Logger, maxAttempts: options.MaxAttempts, baseBackoff: options.BaseBackoff,
		maxBody: options.MaxBody, userAgent: options.UserAgent, allowHTTP: options.AllowHTTP,
		now: options.Now, sleep: options.Sleep,
	}
}

// Do 执行请求并在受控大小内读取响应内容。
func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	if err := c.validate(request); err != nil {
		return Response{}, err
	}
	if cached, ok := c.readCache(ctx, request); ok {
		return cached, nil
	}
	response, err := c.Open(ctx, request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()
	result, err := c.readResponse(response, request.MaxBodyBytes)
	if err != nil {
		return Response{}, err
	}
	c.writeCache(ctx, request, result)
	return result, nil
}

// Open 执行流式请求；调用方必须关闭成功响应的 Body。
func (c *Client) Open(ctx context.Context, request Request) (*http.Response, error) {
	if err := c.validate(request); err != nil {
		return nil, err
	}
	var last error
	maxAttempts := c.requestAttempts(request)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		response, retry, err := c.attempt(ctx, request)
		if err == nil {
			return response, nil
		}
		last = err
		if !retry || attempt == maxAttempts {
			break
		}
		if err = c.waitRetry(ctx, request, attempt, response); err != nil {
			return nil, err
		}
	}
	return nil, last
}

func applyDefaults(options *Options) {
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = defaultMaxAttempts
	}
	if options.BaseBackoff <= 0 {
		options.BaseBackoff = 250 * time.Millisecond
	}
	if options.MaxBody <= 0 {
		options.MaxBody = defaultMaxBody
	}
	if options.UserAgent == "" {
		options.UserAgent = defaultUserAgent
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
}

func (c *Client) validate(request Request) error {
	parsed, err := url.Parse(request.URL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%w: 外部请求地址无效", domain.ErrInvalidInput)
	}
	if parsed.Scheme != "https" && !(c.allowHTTP && parsed.Scheme == "http") {
		return fmt.Errorf("%w: 外部请求必须使用 HTTPS", domain.ErrInvalidInput)
	}
	if request.Method == "" {
		return fmt.Errorf("%w: HTTP 方法为空", domain.ErrInvalidInput)
	}
	if request.MaxAttempts < 0 || request.MaxAttempts > c.maxAttempts ||
		(request.RedirectPolicy != RedirectDefault && request.RedirectPolicy != RedirectDeny &&
			request.RedirectPolicy != RedirectSameOriginHTTPS) {
		return fmt.Errorf("%w: 外部请求重试或重定向策略无效", domain.ErrInvalidInput)
	}
	return nil
}

func (c *Client) attempt(ctx context.Context, request Request) (*http.Response, bool, error) {
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, false, fmt.Errorf("等待供应商限流: %w", err)
		}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bytes.NewReader(request.Body))
	if err != nil {
		return nil, false, fmt.Errorf("%w: 创建外部请求: %v", domain.ErrInvalidInput, err)
	}
	httpRequest.Header = request.Headers.Clone()
	if httpRequest.Header == nil {
		httpRequest.Header = make(http.Header)
	}
	httpRequest.Header.Set("User-Agent", c.userAgent)
	response, err := c.requestHTTPClient(request).Do(httpRequest)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, true, &ProviderError{Cause: redactRequestError(err, request), Retryable: true}
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, false, nil
	}
	_ = response.Body.Close()
	errorValue := &ProviderError{
		StatusCode: response.StatusCode, RequestID: responseRequestID(response),
		Retryable: retryableStatus(response.StatusCode),
	}
	return response, errorValue.Retryable, errorValue
}

func (c *Client) requestAttempts(request Request) int {
	if request.MaxAttempts > 0 {
		return request.MaxAttempts
	}
	return c.maxAttempts
}

func (c *Client) requestHTTPClient(request Request) *http.Client {
	if request.RedirectPolicy == RedirectDefault {
		return c.httpClient
	}
	client := *c.httpClient
	original := c.httpClient.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if request.RedirectPolicy == RedirectDeny {
			return fmt.Errorf("%w: 当前供应商请求禁止重定向", domain.ErrProviderUnavailable)
		}
		if err := validateSameOriginRedirect(next, via); err != nil {
			return err
		}
		if original != nil {
			return original(next, via)
		}
		return nil
	}
	return &client
}

func validateSameOriginRedirect(next *http.Request, via []*http.Request) error {
	if next == nil || next.URL == nil || len(via) == 0 || len(via) > 5 {
		return fmt.Errorf("%w: 供应商重定向跳数或目标无效", domain.ErrProviderUnavailable)
	}
	previous := via[len(via)-1]
	if previous == nil || previous.URL == nil || next.URL.Scheme != "https" || next.URL.User != nil ||
		!sameOrigin(previous.URL, next.URL) || !publicRedirectHost(next.URL.Hostname()) {
		return fmt.Errorf("%w: 供应商重定向必须是同源公开 HTTPS 地址", domain.ErrProviderUnavailable)
	}
	return nil
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) && effectivePort(left) == effectivePort(right)
}

func effectivePort(value *url.URL) string {
	if value.Port() != "" {
		return value.Port()
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	return "80"
}

func publicRedirectHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast())
}

func (c *Client) waitRetry(ctx context.Context, request Request, attempt int, response *http.Response) error {
	delay := c.baseBackoff * time.Duration(1<<(attempt-1))
	if response != nil {
		delay = retryAfter(response.Header.Get("Retry-After"), c.now(), delay)
	}
	c.logger.WarnContext(ctx, "外部请求准备重试",
		"url", RedactURL(request.URL, request.SensitiveQueryKeys...), "attempt", attempt, "delay", delay)
	return c.sleep(ctx, delay)
}

func (c *Client) readResponse(response *http.Response, override int64) (Response, error) {
	limit := c.maxBody
	if override > 0 {
		limit = override
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return Response{}, fmt.Errorf("读取供应商响应: %w", err)
	}
	if int64(len(body)) > limit {
		return Response{}, fmt.Errorf("%w: 供应商响应超过 %d 字节", domain.ErrInvalidInput, limit)
	}
	return Response{
		StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: body,
		FetchedAt: c.now(), RequestID: responseRequestID(response),
	}, nil
}

func (c *Client) readCache(ctx context.Context, request Request) (Response, bool) {
	if c.cache == nil || request.CacheKey == "" || request.CacheTTL <= 0 {
		return Response{}, false
	}
	var cached Response
	found, err := c.cache.Get(ctx, request.CacheKey, &cached)
	if err != nil {
		c.logger.WarnContext(ctx, "读取外部请求缓存失败", "error", err)
		return Response{}, false
	}
	cached.FromCache = found
	return cached, found
}

func (c *Client) writeCache(ctx context.Context, request Request, response Response) {
	if c.cache == nil || request.CacheKey == "" || request.CacheTTL <= 0 {
		return
	}
	if err := c.cache.Set(ctx, request.CacheKey, response, request.CacheTTL); err != nil {
		c.logger.WarnContext(ctx, "写入外部请求缓存失败", "error", err)
	}
}

// ProviderError 保存供应商失败的结构化信息。
type ProviderError struct {
	StatusCode int
	RequestID  string
	Retryable  bool
	Cause      error
}

func (e *ProviderError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("供应商返回 HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("供应商请求失败: %v", e.Cause)
}

// Unwrap 将供应商失败统一映射为领域不可用错误。
func (e *ProviderError) Unwrap() error {
	return errors.Join(domain.ErrProviderUnavailable, e.Cause)
}

func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooEarly ||
		code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

func responseRequestID(response *http.Response) string {
	for _, name := range []string{"X-Request-ID", "X-Log-ID", "X-Amzn-RequestId"} {
		if value := response.Header.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func retryAfter(value string, now time.Time, fallback time.Duration) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if target, err := http.ParseTime(value); err == nil && target.After(now) {
		return target.Sub(now)
	}
	return fallback
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsStrongETag 判断供应商修订标识是否可用于条件请求和响应一致性比对。
func IsStrongETag(value string) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		if value[index] == '"' || value[index] <= 0x20 || value[index] == 0x7f {
			return false
		}
	}
	return true
}

// RedactURL 隐藏 URL 中的密钥、令牌和供应商自定义敏感参数。
func RedactURL(rawURL string, extraKeys ...string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid-url>"
	}
	sensitive := sensitiveKeySet(extraKeys)
	query := parsed.Query()
	for key := range query {
		if _, ok := sensitive[strings.ToLower(key)]; ok {
			query.Set(key, "REDACTED")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type redactedCauseError struct {
	message string
	cause   error
}

func (e *redactedCauseError) Error() string { return e.message }

func (e *redactedCauseError) Is(target error) bool { return errors.Is(e.cause, target) }

func redactRequestError(err error, request Request) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return sanitizedURLError(urlError, request)
	}
	return &redactedCauseError{message: redactErrorMessage(err.Error(), request), cause: err}
}

func sanitizedURLError(value *url.Error, request Request) error {
	message := "外部请求失败"
	if value.Err != nil {
		message = redactErrorMessage(value.Err.Error(), request)
	}
	return &url.Error{
		Op: value.Op, URL: RedactURL(value.URL, request.SensitiveQueryKeys...),
		Err: &redactedCauseError{message: message, cause: value.Err},
	}
}

func redactErrorMessage(message string, request Request) string {
	message = strings.ReplaceAll(message, request.URL,
		RedactURL(request.URL, request.SensitiveQueryKeys...))
	message = redactSensitiveHeaders(message, request.Headers)
	parsed, parseErr := url.Parse(request.URL)
	if parseErr != nil {
		return message
	}
	sensitive := sensitiveKeySet(request.SensitiveQueryKeys)
	for key, values := range parsed.Query() {
		if _, ok := sensitive[strings.ToLower(key)]; !ok {
			continue
		}
		for _, value := range values {
			message = redactSensitiveValue(message, value)
		}
	}
	return message
}

func redactSensitiveHeaders(message string, headers http.Header) string {
	for _, name := range []string{"Authorization", "Proxy-Authorization", "X-API-Key", "API-Key"} {
		for _, value := range headers.Values(name) {
			message = redactSensitiveValue(message, value)
			parts := strings.Fields(value)
			for index := 1; index < len(parts); index++ {
				message = redactSensitiveValue(message, parts[index])
			}
		}
	}
	return message
}

func sensitiveKeySet(extraKeys []string) map[string]struct{} {
	keys := append(defaultSensitiveKeys(), extraKeys...)
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[strings.ToLower(key)] = struct{}{}
	}
	return result
}

func redactSensitiveValue(message, value string) string {
	if value == "" {
		return message
	}
	message = strings.ReplaceAll(message, value, "REDACTED")
	return strings.ReplaceAll(message, url.QueryEscape(value), "REDACTED")
}

func defaultSensitiveKeys() []string {
	return []string{"key", "jscode", "apikey", "api_key", "token", "access_token"}
}
