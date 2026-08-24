package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestClientRetriesAndCachesSuccess(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Request-ID", "request-1")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	cache := &memoryCache{}
	client := New(Options{
		AllowHTTP: true, Cache: cache, BaseBackoff: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	request := Request{Method: http.MethodGet, URL: server.URL, CacheKey: "test", CacheTTL: time.Minute}
	first, err := client.Do(context.Background(), request)
	if err != nil || first.RequestID != "request-1" || attempts != 2 {
		t.Fatalf("first=%+v attempts=%d err=%v", first, attempts, err)
	}
	second, err := client.Do(context.Background(), request)
	if err != nil || !second.FromCache || attempts != 2 {
		t.Fatalf("second=%+v attempts=%d err=%v", second, attempts, err)
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()
	client := New(Options{AllowHTTP: true, MaxBody: 4})
	_, err := client.Do(context.Background(), Request{Method: http.MethodGet, URL: server.URL})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestClientRequiresHTTPS(t *testing.T) {
	client := New(Options{})
	_, err := client.Do(context.Background(), Request{Method: http.MethodGet, URL: "http://example.test"})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestRedactURL(t *testing.T) {
	got := RedactURL("https://example.test/data?key=secret&query=rain&custom=value", "custom")
	want := "https://example.test/data?custom=REDACTED&key=REDACTED&query=rain"
	if got != want {
		t.Fatalf("RedactURL() = %q", got)
	}
}

type memoryCache struct {
	value Response
	set   bool
}

func (m *memoryCache) Get(_ context.Context, _ string, destination any) (bool, error) {
	if !m.set {
		return false, nil
	}
	*(destination.(*Response)) = m.value
	return true, nil
}

func (m *memoryCache) Set(_ context.Context, _ string, value any, _ time.Duration) error {
	m.value = value.(Response)
	m.set = true
	return nil
}

func (m *memoryCache) Delete(context.Context, string) error {
	m.set = false
	return nil
}
