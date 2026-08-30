package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckAcceptsReadyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = response.Write([]byte(`{"status":"ready"}`))
	}))
	defer server.Close()
	if err := check(context.Background(), server.Client(), server.URL+"/readyz"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsNonReadyResponses(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
	}{
		{name: "http_status", status: http.StatusServiceUnavailable, body: `{"status":"not_ready"}`},
		{name: "wrong_value", status: http.StatusOK, body: `{"status":"ok"}`},
		{name: "unknown_field", status: http.StatusOK, body: `{"status":"ready","extra":true}`},
		{name: "trailing_json", status: http.StatusOK, body: `{"status":"ready"}{}`},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat(" ", maxPayloadBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			if err := check(context.Background(), server.Client(), server.URL+"/readyz"); err == nil {
				t.Fatal("损坏的就绪响应未被拒绝")
			}
		})
	}
}

func TestLoopbackHTTPRejectsUnsafeEndpoints(t *testing.T) {
	for _, value := range []string{
		"https://127.0.0.1:8080/readyz", "http://example.com/readyz",
		"http://127.0.0.1:8080/healthz", "http://user@127.0.0.1:8080/readyz",
		"http://127.0.0.1:8080/readyz?token=value",
	} {
		if loopbackHTTP(value) {
			t.Fatalf("不安全健康检查地址被接受: %s", value)
		}
	}
}
