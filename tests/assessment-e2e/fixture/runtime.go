package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
)

type fixtureIdentity struct {
	token   string
	treeSHA string
}

func loadFixtureIdentity() (fixtureIdentity, error) {
	identity := fixtureIdentity{
		token:   envOrDefault("E2E_FIXTURE_TOKEN", "local-fixture"),
		treeSHA: envOrDefault("E2E_TREE_SHA", "local-tree"),
	}
	if !validIdentityPart(identity.token, 128) || !validIdentityPart(identity.treeSHA, 128) {
		return fixtureIdentity{}, fmt.Errorf("评估 fixture 运行身份无效")
	}
	return identity, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func validIdentityPart(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, char := range []byte(value) {
		if isIdentityCharacter(char) {
			continue
		}
		return false
	}
	return true
}

func isIdentityCharacter(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') || char == '-' || char == '_'
}

func listenFixture(address, runtimeFile string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("监听评估 fixture: %w", err)
	}
	if runtimeFile == "" {
		return listener, nil
	}
	if err := os.WriteFile(runtimeFile, []byte(listener.Addr().String()+"\n"), 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("发布评估 fixture 地址: %w", err)
	}
	return listener, nil
}

func health(identity fixtureIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok:"+identity.token+":"+identity.treeSHA+"\n")
	}
}
