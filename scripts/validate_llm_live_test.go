package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateLLMLiveScriptClassifiesProviderState(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台未安装 sh，需在腾讯 Ubuntu 补跑行为门禁")
	}
	root, envFile := newLLMLiveFixture(t)
	cases := []struct {
		state      string
		exitCode   int
		contains   string
		notContain string
	}{
		{"recovered", 0, "真实 OpenAI 兼容 LLM 结构化输出契约验证通过", "上游暂不可用"},
		{"degraded", 75, "真实 LLM 上游暂不可用，模型目录与系统降级合同验证通过", "结构化输出契约验证通过"},
		{"ambiguous", 1, "LLM 在线门禁未形成唯一供应商状态", ""},
		{"duplicate-marker", 1, "LLM 在线门禁未形成唯一供应商状态", ""},
		{"missing-marker", 1, "LLM 在线门禁未形成唯一供应商状态", ""},
		{"recovered-prefix", 1, "LLM 在线门禁未形成唯一供应商状态", ""},
		{"recovered-suffix", 1, "LLM 在线门禁未形成唯一供应商状态", ""},
		{"degraded-prefix", 1, "LLM 在线门禁未形成唯一供应商状态", ""},
		{"degraded-suffix", 1, "LLM 在线门禁未形成唯一供应商状态", ""},
		{"missing-agent", 1, "LLM 应用降级合同测试未精确执行并通过", ""},
		{"prefix-agent", 1, "LLM 应用降级合同测试未精确执行并通过", ""},
		{"skipped-agent", 1, "LLM 应用降级合同测试未精确执行并通过", ""},
		{"duplicate-agent", 1, "LLM 应用降级合同测试未精确执行并通过", ""},
		{"failed", 7, "provider failed", ""},
	}
	for _, test := range cases {
		t.Run(test.state, func(t *testing.T) {
			output, code := runLLMLiveFixture(t, shell, root, envFile, test.state)
			if code != test.exitCode || !strings.Contains(output, test.contains) {
				t.Fatalf("状态分类错误: code=%d output=%s", code, output)
			}
			if test.notContain != "" && strings.Contains(output, test.notContain) {
				t.Fatalf("状态文案混淆: %s", output)
			}
		})
	}
}

func newLLMLiveFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	scriptsDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copySystemGateFile(t, "validate-llm-live.sh", scriptsDir)
	fake := "#!/usr/bin/env sh\n" +
		"pass(){ echo \"=== RUN   $1\"; echo \"--- PASS: $1 (0.00s)\"; }\n" +
		"live(){ pass TestLiveProviderState; }\n" +
		"agent(){ pass TestGenerateDegradesProviderTimeoutsWhenRequestContextIsAlive; }\n" +
		"case \"$LLM_GATE_FAKE_STATE\" in\n" +
		"recovered) live; agent; echo 'live_test.go:1: LLM_LIVE_RECOVERED';;\n" +
		"degraded) live; agent; echo 'live_test.go:1: LLM_UPSTREAM_DEGRADED';;\n" +
		"ambiguous) live; agent; echo LLM_LIVE_RECOVERED; echo LLM_UPSTREAM_DEGRADED;;\n" +
		"duplicate-marker) live; agent; echo LLM_LIVE_RECOVERED; echo LLM_LIVE_RECOVERED;;\n" +
		"missing-marker) live; agent;;\n" +
		"recovered-prefix) live; agent; echo PREFIX_LLM_LIVE_RECOVERED;;\n" +
		"recovered-suffix) live; agent; echo LLM_LIVE_RECOVERED_EXTRA;;\n" +
		"degraded-prefix) live; agent; echo PREFIX_LLM_UPSTREAM_DEGRADED;;\n" +
		"degraded-suffix) live; agent; echo LLM_UPSTREAM_DEGRADED_EXTRA;;\n" +
		"missing-agent) live; echo LLM_LIVE_RECOVERED;;\n" +
		"prefix-agent) live; pass TestGenerateDegradesProviderTimeoutsWhenRequestContextIsAliveExtra; echo LLM_LIVE_RECOVERED;;\n" +
		"skipped-agent) live; echo '=== RUN   TestGenerateDegradesProviderTimeoutsWhenRequestContextIsAlive'; echo '--- SKIP: TestGenerateDegradesProviderTimeoutsWhenRequestContextIsAlive (0.00s)'; echo LLM_LIVE_RECOVERED;;\n" +
		"duplicate-agent) live; agent; agent; echo LLM_LIVE_RECOVERED;;\n" +
		"failed) echo provider failed; exit 7;;\nesac\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "run-validation-container.sh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(root, "runtime.env")
	apiKeyName := "LLM_API" + "_KEY"
	env := "LLM_ENABLED=true\nLLM_BASE_URL=https://example.com/v1/chat/completions\n" +
		apiKeyName + "=test-only-value\nLLM_MODEL=test-model\n"
	if err := os.WriteFile(envFile, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, envFile
}

func runLLMLiveFixture(t *testing.T, shell, root, envFile, state string) (string, int) {
	t.Helper()
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-llm-live.sh")))
	command.Dir = root
	command.Env = append(os.Environ(), "LLM_ENV_FILE="+envFile, "LLM_GATE_FAKE_STATE="+state)
	payload, err := command.CombinedOutput()
	if err == nil {
		return string(payload), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	return string(payload), exitErr.ExitCode()
}
