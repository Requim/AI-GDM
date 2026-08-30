package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	defaultEndpoint = "http://127.0.0.1:8080/readyz"
	maxPayloadBytes = 1024
)

func main() {
	endpoint, err := endpointArgument(os.Args)
	if err == nil {
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		err = check(ctx, client, endpoint)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func endpointArgument(arguments []string) (string, error) {
	if len(arguments) == 1 {
		return defaultEndpoint, nil
	}
	if len(arguments) != 2 {
		return "", fmt.Errorf("健康检查只接受一个 URL 参数")
	}
	return arguments[1], nil
}

func check(ctx context.Context, client *http.Client, endpoint string) error {
	if client == nil || !loopbackHTTP(endpoint) {
		return fmt.Errorf("健康检查客户端或地址无效")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("创建健康检查请求: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("请求就绪探针: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("就绪探针返回状态 %d", response.StatusCode)
	}
	return decodeReady(response.Body)
}

func loopbackHTTP(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	return (host == "127.0.0.1" || host == "localhost" || host == "::1") && parsed.Path == "/readyz"
}

func decodeReady(reader io.Reader) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxPayloadBytes+1))
	if err != nil || len(payload) > maxPayloadBytes {
		return fmt.Errorf("读取就绪探针响应失败或超量")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value struct {
		Status string `json:"status"`
	}
	if err = decoder.Decode(&value); err != nil || value.Status != "ready" {
		return fmt.Errorf("就绪探针响应无效")
	}
	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("就绪探针包含尾随内容")
	}
	return nil
}
