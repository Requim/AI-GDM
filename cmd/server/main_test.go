package main

import (
	"testing"
	"time"
)

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
