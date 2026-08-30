package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPrintVersion(t *testing.T) {
	var output bytes.Buffer
	if !printVersion([]string{"--version"}, &output) {
		t.Fatal("--version 未被识别")
	}
	if strings.TrimSpace(output.String()) != version {
		t.Fatalf("版本输出错误: %q", output.String())
	}
	if printVersion([]string{"serve"}, &output) {
		t.Fatal("普通启动参数被误识别为版本请求")
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "invalid")

	started := time.Now()
	if err := run(); err == nil {
		t.Fatal("run() 未拒绝无效配置")
	}
	if time.Since(started) > time.Second {
		t.Fatal("run() 未在启动服务前返回配置错误")
	}
}
