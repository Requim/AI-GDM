package scripts_test

import (
	"os"
	"strings"
	"testing"
)

func TestValidateAssessmentUIScript(t *testing.T) {
	payload, err := os.ReadFile("validate-assessment-ui.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(payload), "\r\n", "\n")
	for _, fragment := range []string{
		"go test ./scripts -run \"^TestValidateAssessmentUIScript$\" -count=1",
		"./internal/adapters/authority",
		"./internal/application/agent",
		"./internal/domain/report",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("assessment 专项脚本缺少 %q", fragment)
		}
	}
}
