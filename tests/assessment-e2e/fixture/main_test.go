package main

import (
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthBindsRunIdentity(t *testing.T) {
	identity := fixtureIdentity{token: "run-token-123", treeSHA: "tree-sha-456"}
	recorder := httptest.NewRecorder()
	health(identity).ServeHTTP(recorder, httptest.NewRequest("GET", "/__fixture/health", nil))
	if recorder.Code != 200 || recorder.Body.String() != "ok:run-token-123:tree-sha-456\n" {
		t.Fatalf("health code=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", recorder.Header().Get("Cache-Control"))
	}
}

func TestListenFixturePublishesHeldAutomaticPort(t *testing.T) {
	runtimeFile := filepath.Join(t.TempDir(), "address")
	listener, err := listenFixture("127.0.0.1:0", runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	payload, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	address := strings.TrimSpace(string(payload))
	if address != listener.Addr().String() {
		t.Fatalf("published=%q listener=%q", address, listener.Addr())
	}
	second, err := net.Listen("tcp", address)
	if err == nil {
		_ = second.Close()
		t.Fatal("自动选择的端口未由 fixture 独占")
	}
}

func TestLoadFixtureIdentityRejectsControlCharacters(t *testing.T) {
	t.Setenv("E2E_FIXTURE_TOKEN", "bad\ntoken")
	t.Setenv("E2E_TREE_SHA", "tree-sha")
	if _, err := loadFixtureIdentity(); err == nil {
		t.Fatal("包含控制字符的 fixture token 未被拒绝")
	}
}
