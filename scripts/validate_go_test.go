package scripts_test

import (
	"os"
	"strings"
	"testing"
)

func TestValidateGoScriptDeclaresOfflineBoundary(t *testing.T) {
	payload, err := os.ReadFile("validate-go.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(payload), "\r\n", "\n")
	for _, fragment := range []string{
		"运行离线全量 Go 门禁",
		"PostgreSQL 集成与真实 TestLive 由对应专项脚本验证",
		"AI_GDM_LIVE_EXPOSURE=0",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("全量 Go 脚本缺少离线边界 %q", fragment)
		}
	}
	if strings.Contains(script, "TEST_DATABASE_URL=") {
		t.Fatal("离线全量 Go 脚本不应冒充 PostgreSQL 集成门禁")
	}
}
