package scripts_test

import (
	"os"
	"strings"
	"testing"
)

func TestSecurityBrowserImagePreservesAndAuditsPlaywrightReport(t *testing.T) {
	payload, err := os.ReadFile("../tests/security-e2e/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(string(payload), "\r\n", "\n")
	fragments := []string{
		"playwright test --reporter=json > /tmp/security-report.json",
		"status=$?",
		"cat /tmp/security-report.json",
		"[ $status -eq 0 ] || exit $status",
		"node audit-results.mjs expected-tests.json /tmp/security-report.json",
	}
	previous := -1
	for _, fragment := range fragments {
		index := strings.Index(content, fragment)
		if index < 0 || index <= previous {
			t.Fatalf("安全浏览器镜像缺少有序结果审计步骤 %q", fragment)
		}
		previous = index
	}
	if !strings.Contains(content, "COPY playwright.config.js audit-results.mjs expected-tests.json ./") {
		t.Fatal("安全浏览器镜像未复制固定场景清单与审计器")
	}
}
