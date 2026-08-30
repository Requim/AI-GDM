package httpserver

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const securityTestAdminToken = "0123456789abcdef0123456789abcdef"

func TestSecurityHeadersCoverSuccessAndTLS(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := serveRequest(server.Handler(), request)
	for _, name := range []string{"Content-Security-Policy", "Cross-Origin-Opener-Policy",
		"Cross-Origin-Resource-Policy", "Permissions-Policy", "Referrer-Policy",
		"X-Content-Type-Options", "X-Frame-Options"} {
		if response.Header().Get(name) == "" {
			t.Fatalf("响应缺少 %s", name)
		}
	}
	if response.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("明文请求不应下发 HSTS")
	}
	request = httptest.NewRequest(http.MethodGet, "https://example.test/healthz", nil)
	request.TLS = &tls.ConnectionState{}
	if value := serveRequest(server.Handler(), request).Header().Get("Strict-Transport-Security"); value == "" {
		t.Fatal("TLS 请求缺少 HSTS")
	}
}

func TestProtectedWritesRequireConfiguredAdminAuthorization(t *testing.T) {
	tests := []struct {
		name, configured string
		authorization    []string
		want             int
	}{
		{name: "server_unconfigured", want: http.StatusServiceUnavailable},
		{name: "missing", configured: securityTestAdminToken, want: http.StatusUnauthorized},
		{name: "wrong", configured: securityTestAdminToken, authorization: []string{"Bearer wrong"}, want: http.StatusUnauthorized},
		{name: "duplicate", configured: securityTestAdminToken,
			authorization: []string{"Bearer " + securityTestAdminToken, "Bearer " + securityTestAdminToken}, want: http.StatusUnauthorized},
		{name: "valid", configured: securityTestAdminToken,
			authorization: []string{"Bearer " + securityTestAdminToken}, want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := newSecurityTestHandler(t, test.configured, &calls)
			request := secureWriteRequest("/api/v1/command", "{}")
			for _, value := range test.authorization {
				request.Header.Add("Authorization", value)
			}
			response := serveRequest(handler, request)
			if response.Code != test.want || calls != boolInt(test.want == http.StatusNoContent) {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestWriteRequestRejectsUnsafeMetadataBeforeHandler(t *testing.T) {
	for _, test := range writeRequestMetadataCases() {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := newSecurityTestHandler(t, securityTestAdminToken, &calls)
			target := test.target
			if target == "" {
				target = "https://example.test/api/v1/command"
			}
			request := authorizedWriteRequest(target, "{}")
			test.edit(request)
			response := serveRequest(handler, request)
			if response.Code != test.want || calls != boolInt(test.want == http.StatusNoContent) {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
		})
	}
}

type writeRequestMetadataCase struct {
	name   string
	target string
	edit   func(*http.Request)
	want   int
}

func writeRequestMetadataCases() []writeRequestMetadataCase {
	return append(originMetadataCases(), contentMetadataCases()...)
}

func originMetadataCases() []writeRequestMetadataCase {
	return []writeRequestMetadataCase{
		{name: "https_same_origin", target: "https://example.test/api/v1/command", edit: func(r *http.Request) {
			r.Header.Set("Origin", "https://example.test")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}, want: http.StatusNoContent},
		{name: "http_same_origin", target: "http://example.test/api/v1/command", edit: func(r *http.Request) {
			r.Header.Set("Origin", "http://example.test")
		}, want: http.StatusNoContent},
		{name: "https_origin_on_http", target: "http://example.test/api/v1/command",
			edit: func(r *http.Request) { r.Header.Set("Origin", "https://example.test") }, want: http.StatusForbidden},
		{name: "http_origin_on_tls", target: "https://example.test/api/v1/command",
			edit: func(r *http.Request) { r.Header.Set("Origin", "http://example.test") }, want: http.StatusForbidden},
		{name: "forwarded_proto_not_trusted", target: "http://example.test/api/v1/command", edit: func(r *http.Request) {
			r.Header.Set("Origin", "https://example.test")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, want: http.StatusForbidden},
		{name: "cross_origin", edit: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.test") }, want: http.StatusForbidden},
		{name: "null_origin", edit: func(r *http.Request) { r.Header.Set("Origin", "null") }, want: http.StatusForbidden},
		{name: "cross_site", edit: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, want: http.StatusForbidden},
		{name: "missing_csrf", edit: func(r *http.Request) { r.Header.Del(CSRFHeaderName) }, want: http.StatusForbidden},
	}
}

func contentMetadataCases() []writeRequestMetadataCase {
	return []writeRequestMetadataCase{
		{name: "text_plain", edit: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, want: http.StatusUnsupportedMediaType},
		{name: "bad_charset", edit: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=gbk") }, want: http.StatusUnsupportedMediaType},
		{name: "boundary_parameter", edit: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; boundary=x") }, want: http.StatusUnsupportedMediaType},
		{name: "profile_parameter", edit: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; profile=example") }, want: http.StatusUnsupportedMediaType},
		{name: "charset_and_profile", edit: func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json; charset=utf-8; profile=example")
		}, want: http.StatusUnsupportedMediaType},
		{name: "duplicate_charset", edit: func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json; charset=utf-8; charset=utf-8")
		}, want: http.StatusUnsupportedMediaType},
		{name: "duplicate_charset_alias", edit: func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json; charset=utf-8; Charset=UTF-8")
		}, want: http.StatusUnsupportedMediaType},
		{name: "case_insensitive_json_utf8", edit: func(r *http.Request) {
			r.Header.Set("Content-Type", "Application/JSON; Charset=UTF-8")
		}, want: http.StatusNoContent},
		{name: "compressed", edit: func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, want: http.StatusUnsupportedMediaType},
	}
}

func TestWriteRequestRejectsKnownAndChunkedOversize(t *testing.T) {
	for _, knownLength := range []bool{true, false} {
		calls := 0
		handler := newSecurityTestHandler(t, securityTestAdminToken, &calls)
		request := authorizedWriteRequest("/api/v1/command", strings.Repeat("x", maxInboundBodyBytes+1))
		if !knownLength {
			request.ContentLength = -1
		}
		response := serveRequest(handler, request)
		if response.Code != http.StatusRequestEntityTooLarge || calls != 0 {
			t.Fatalf("known=%v status=%d calls=%d", knownLength, response.Code, calls)
		}
	}
}

func TestRateLimitIgnoresForwardedAddressAndBoundsClients(t *testing.T) {
	calls := 0
	handler := newSecurityTestHandlerWithRate(t, securityTestAdminToken, 20, 20, &calls)
	for index := 0; index < 20; index++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/read", nil)
		request.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('a'+index)))
		if response := serveRequest(handler, request); response.Code != http.StatusNoContent {
			t.Fatalf("request %d status=%d", index, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/read", nil)
	request.Header.Set("X-Forwarded-For", "198.51.100.5")
	response := serveRequest(handler, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "60" || calls != 20 {
		t.Fatalf("status=%d retry=%s calls=%d", response.Code, response.Header().Get("Retry-After"), calls)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/read", nil)
	request.RemoteAddr = "198.51.100.9:1234"
	if response = serveRequest(handler, request); response.Code != http.StatusNoContent {
		t.Fatalf("独立客户端 status=%d", response.Code)
	}
	limiter := newClientRateLimiter(60_000, 10_000)
	for index := 0; index < maxRateClients+100; index++ {
		request.RemoteAddr = "2001:db8::" + clientSuffix(index)
		limiter.allow(request, 1)
	}
	if len(limiter.clients) != maxRateClients {
		t.Fatalf("client limiter entries=%d", len(limiter.clients))
	}
}

func TestHTTPServerLocksConnectionTimeouts(t *testing.T) {
	server := newTestServer(t)
	got := server.httpServer
	if got.ReadHeaderTimeout != 5*time.Second || got.ReadTimeout != 15*time.Second ||
		got.WriteTimeout != 60*time.Second || got.IdleTimeout != 60*time.Second || got.MaxHeaderBytes != 32*1024 {
		t.Fatalf("HTTP timeouts=%+v", got)
	}
}

func newSecurityTestHandler(t *testing.T, token string, calls *int) http.Handler {
	return newSecurityTestHandlerWithRate(t, token, 60_000, 10_000, calls)
}

func newSecurityTestHandlerWithRate(t *testing.T, token string, perMinute, burst int, calls *int) http.Handler {
	t.Helper()
	server, err := New(":0", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), SecurityOptions{
		AdminToken: token, RateLimitPerMinute: perMinute, RateLimitBurst: burst,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		w.WriteHeader(http.StatusNoContent)
	})); err != nil {
		t.Fatal(err)
	}
	return server.Handler()
}

func secureWriteRequest(target, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set(CSRFHeaderName, CSRFHeaderValue)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func authorizedWriteRequest(target, body string) *http.Request {
	request := secureWriteRequest(target, body)
	request.Header.Set("Authorization", "Bearer "+securityTestAdminToken)
	return request
}

func serveRequest(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func clientSuffix(value int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[value>>12&15], digits[value>>8&15], digits[value>>4&15], digits[value&15]})
}
