package httpserver

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	// CSRFHeaderName 是浏览器写请求必须携带的非简单请求头。
	CSRFHeaderName       = "X-CSRF-Token"
	CSRFHeaderValue      = "ai-gdm-browser-v1"
	maxInboundBodyBytes  = 1 << 20
	maxAuthorizationSize = 512
)

// SecurityOptions 配置入站管理员授权和请求限流。
type SecurityOptions struct {
	AdminToken         string
	RateLimitPerMinute int
	RateLimitBurst     int
}

type requestSecurity struct {
	adminDigest [sha256.Size]byte
	hasAdmin    bool
	limiter     *clientRateLimiter
}

func newRequestSecurity(options SecurityOptions) (*requestSecurity, error) {
	if options.RateLimitPerMinute <= 0 || options.RateLimitPerMinute > 60_000 ||
		options.RateLimitBurst < maxRequestCost || options.RateLimitBurst > 10_000 ||
		options.RateLimitBurst > options.RateLimitPerMinute {
		return nil, fmt.Errorf("HTTP 入站限流配置无效")
	}
	value := &requestSecurity{limiter: newClientRateLimiter(
		options.RateLimitPerMinute, options.RateLimitBurst,
	)}
	if options.AdminToken != "" {
		value.adminDigest, value.hasAdmin = sha256.Sum256([]byte(options.AdminToken)), true
	}
	return value, nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://*.tile.openstreetmap.org; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		if r.TLS != nil {
			header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (security *requestSecurity) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if security.limiter.allow(r, apiRequestCost(r)) {
			next.ServeHTTP(w, r)
			return
		}
		r = earlyResponseRequest(w, r)
		w.Header().Set("Retry-After", "60")
		writeSecurityError(w, r, http.StatusTooManyRequests, "rate_limited", "请求过于频繁")
	})
}

func (security *requestSecurity) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !protectedWriteRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !security.hasAdmin {
			writeSecurityError(w, r, http.StatusServiceUnavailable,
				"admin_security_unconfigured", "管理员授权未配置")
			return
		}
		if !security.validAuthorization(r.Header.Values("Authorization")) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ai-gdm-admin"`)
			writeSecurityError(w, r, http.StatusUnauthorized, "admin_authorization_required", "需要管理员授权")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (security *requestSecurity) validAuthorization(values []string) bool {
	if len(values) != 1 || len(values[0]) > maxAuthorizationSize {
		return false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], security.adminDigest[:]) == 1
}

func csrfProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !protectedWriteRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if err := validateWriteRequest(r); err != nil {
			writeSecurityError(w, r, err.status, err.code, err.message)
			return
		}
		if err := readBoundedWriteBody(r); err != nil {
			writeSecurityError(w, r, err.status, err.code, err.message)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type requestSecurityError struct {
	status  int
	code    string
	message string
}

func validateWriteRequest(r *http.Request) *requestSecurityError {
	if r.ContentLength > maxInboundBodyBytes {
		return &requestSecurityError{http.StatusRequestEntityTooLarge, "request_too_large", "请求体超过安全上限"}
	}
	if values := r.Header.Values("Content-Encoding"); len(values) > 1 || len(values) == 1 && !strings.EqualFold(values[0], "identity") {
		return &requestSecurityError{http.StatusUnsupportedMediaType, "unsupported_content_encoding", "不支持压缩请求体"}
	}
	if !validJSONContentType(r.Header.Values("Content-Type")) {
		return &requestSecurityError{http.StatusUnsupportedMediaType, "json_content_type_required", "写请求必须使用 application/json"}
	}
	if !validCSRFHeader(r.Header.Values(CSRFHeaderName)) || !validRequestOrigin(r) {
		return &requestSecurityError{http.StatusForbidden, "csrf_rejected", "请求来源未获授权"}
	}
	return nil
}

func readBoundedWriteBody(r *http.Request) *requestSecurityError {
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBodyBytes+1))
	if err != nil {
		return &requestSecurityError{http.StatusBadRequest, "request_body_invalid", "请求体读取失败"}
	}
	if len(payload) > maxInboundBodyBytes {
		return &requestSecurityError{http.StatusRequestEntityTooLarge, "request_too_large", "请求体超过安全上限"}
	}
	r.Body = io.NopCloser(bytes.NewReader(payload))
	r.ContentLength = int64(len(payload))
	return nil
}

func validJSONContentType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	rawValue := values[0]
	mediaType, parameters, err := mime.ParseMediaType(rawValue)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	if len(parameters) == 0 {
		return !strings.Contains(rawValue, ";")
	}
	if len(parameters) != 1 || strings.Count(rawValue, ";") != 1 {
		return false
	}
	charset, exists := parameters["charset"]
	return exists && strings.EqualFold(charset, "utf-8")
}

func validCSRFHeader(values []string) bool {
	return len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte(CSRFHeaderValue)) == 1
}

func validRequestOrigin(r *http.Request) bool {
	if values := r.Header.Values("Sec-Fetch-Site"); len(values) > 1 || len(values) == 1 && values[0] != "same-origin" {
		return false
	}
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	origin, err := url.Parse(values[0])
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	return err == nil && strings.EqualFold(origin.Scheme, expectedScheme) && origin.User == nil &&
		origin.Path == "" && origin.RawQuery == "" && origin.Fragment == "" && strings.EqualFold(origin.Host, r.Host)
}

func protectedWriteRequest(r *http.Request) bool {
	return isApplicationAPI(r.URL.Path) && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}

func isApplicationAPI(path string) bool {
	return path == "/api/v1" || strings.HasPrefix(path, "/api/v1/")
}

func writeSecurityError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
		"code": code, "message": message, "requestId": middleware.GetReqID(r.Context()),
	}})
}

func apiRequestCost(r *http.Request) int {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/refresh"):
		return 20
	case path == "/api/v1/ai/report":
		return 10
	case path == "/api/v1/loss/assessments":
		return 5
	case path == "/api/v1/map/routes" || path == "/api/v1/map/places/nearby":
		return 2
	default:
		return 1
	}
}
