package survivalapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEncodeResponseRejectsOversizedCollectionsAndStrings(t *testing.T) {
	if _, err := encodeResponse(successResponse{Data: make([]string, maxResponseItems+1)}); err == nil {
		t.Fatal("encodeResponse() 未拒绝 1001 项数组")
	}
	if _, err := encodeResponse(successResponse{Data: strings.Repeat("x", 2<<20)}); err == nil {
		t.Fatal("encodeResponse() 未拒绝超过 2 MiB 的单字段")
	}
}

func TestEncodeResponseRejectsFinalWireBudget(t *testing.T) {
	values := make([]string, 100)
	for index := range values {
		values[index] = strings.Repeat("\x01", 4000)
	}
	if _, err := encodeResponse(successResponse{Data: values, RequestID: "request-1"}); err == nil || !strings.Contains(err.Error(), "响应线字节") {
		t.Fatalf("encodeResponse() final budget error=%v", err)
	}
}

func TestEncodeResponseRejectsCustomMarshalersBeforeInvocation(t *testing.T) {
	tests := []struct {
		name  string
		value func(*int) any
	}{
		{"json value method on value", func(calls *int) any { return jsonValueProbe{calls: calls} }},
		{"json value method on pointer", func(calls *int) any { return &jsonValueProbe{calls: calls} }},
		{"json pointer method on value", func(calls *int) any { return jsonPointerProbe{calls: calls} }},
		{"json pointer method on pointer", func(calls *int) any { return &jsonPointerProbe{calls: calls} }},
		{"text value method on value", func(calls *int) any { return textValueProbe{calls: calls} }},
		{"text value method on pointer", func(calls *int) any { return &textValueProbe{calls: calls} }},
		{"text pointer method on value", func(calls *int) any { return textPointerProbe{calls: calls} }},
		{"text pointer method on pointer", func(calls *int) any { return &textPointerProbe{calls: calls} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			if _, err := encodeResponse(successResponse{Data: test.value(&calls)}); err == nil {
				t.Fatal("encodeResponse() 未拒绝自定义编码器")
			}
			if calls != 0 {
				t.Fatalf("自定义编码方法被调用 %d 次", calls)
			}
		})
	}
}

func TestEncodeResponseAppendsSingleNewline(t *testing.T) {
	payload, err := encodeResponse(successResponse{Data: []string{"ok"}, RequestID: "request-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 || payload[len(payload)-1] != '\n' {
		t.Fatalf("payload=%q", payload)
	}
}

func TestWriteFallbackErrorDoesNotMarshalOversizedRequestID(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeFallbackError(recorder, strings.Repeat("x", maxResponseStringBytes+1))
	response := recorder.Result()
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusInternalServerError || string(payload) != fallbackErrorPayload {
		t.Fatalf("fallback status=%d payload=%q", response.StatusCode, payload)
	}
}

type jsonValueProbe struct{ calls *int }

func (p jsonValueProbe) MarshalJSON() ([]byte, error) {
	*p.calls = *p.calls + 1
	return []byte(`{"padding":"json-value"}`), nil
}

type jsonPointerProbe struct{ calls *int }

func (p *jsonPointerProbe) MarshalJSON() ([]byte, error) {
	*p.calls = *p.calls + 1
	return []byte(`{"padding":"json-pointer"}`), nil
}

type textValueProbe struct{ calls *int }

func (p textValueProbe) MarshalText() ([]byte, error) {
	*p.calls = *p.calls + 1
	return []byte("text-value"), nil
}

type textPointerProbe struct{ calls *int }

func (p *textPointerProbe) MarshalText() ([]byte, error) {
	*p.calls = *p.calls + 1
	return []byte("text-pointer"), nil
}
