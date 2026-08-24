package logging

import (
	"bytes"
	"testing"
)

func TestNewWritesJSON(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "info")
	if err != nil {
		t.Fatal(err)
	}

	logger.Info("服务启动", "component", "test")
	if !bytes.Contains(output.Bytes(), []byte(`"component":"test"`)) {
		t.Fatalf("日志不是预期 JSON: %s", output.String())
	}
}

func TestNewRejectsUnknownLevel(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "trace"); err == nil {
		t.Fatal("New() 未拒绝未知日志级别")
	}
}
