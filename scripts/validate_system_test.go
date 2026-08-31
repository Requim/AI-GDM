package scripts

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type systemManifest struct {
	Version     int                  `json:"version"`
	Gates       []systemManifestGate `json:"gates"`
	Limitations []systemLimitation   `json:"limitations"`
}

type systemManifestGate struct {
	ID               string           `json:"id"`
	Script           string           `json:"script"`
	Mode             string           `json:"mode"`
	LiveEnv          string           `json:"liveEnv"`
	PassCount        int              `json:"passCount"`
	AuditMarker      string           `json:"auditMarker"`
	DegradedExitCode int              `json:"degradedExitCode"`
	DegradedMarker   string           `json:"degradedMarker"`
	DegradedLimit    systemLimitation `json:"degradedLimitation"`
}

type systemLimitation struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	ReasonCode string `json:"reasonCode"`
}

func TestValidateSystemScriptMatchesManifest(t *testing.T) {
	script := readSystemGateFile(t, "validate-system.sh")
	manifest := readSystemManifest(t)
	if manifest.Version != 1 || len(manifest.Gates) != 15 {
		t.Fatalf("系统门禁 manifest 数量无效: %+v", manifest)
	}
	declarations := declaredSystemGates(script)
	if len(declarations) != len(manifest.Gates) {
		t.Fatalf("系统门禁声明数量=%d want=%d", len(declarations), len(manifest.Gates))
	}
	for index, gate := range manifest.Gates {
		if declarations[index][1] != gate.ID || declarations[index][2] != gate.Script {
			t.Fatalf("系统门禁声明漂移: index=%d declaration=%v gate=%+v", index, declarations[index], gate)
		}
		if gate.LiveEnv != "" && !strings.Contains(script, gate.LiveEnv) {
			t.Fatalf("系统门禁缺少 live 开关 %s", gate.LiveEnv)
		}
	}
	assertSystemGateFragments(t, script)
	assertBrowserSentinels(t, manifest)
	assertSystemGateLimitations(t, manifest)
	assertLLMDegradedContract(t, manifest)
}

func declaredSystemGates(script string) [][]string {
	pattern := regexp.MustCompile(`(?m)^run_(?:live_)?gate ([a-z0-9-]+) (scripts/validate-[a-z0-9-]+\.sh)`)
	return pattern.FindAllStringSubmatch(script, -1)
}

func TestSystemGateArchiveRequiresExternalBindingsBeforeDocker(t *testing.T) {
	shell := systemGateShell(t)
	root := newSystemGateFixture(t)
	marker := filepath.Join(root, "docker-called")
	output, runErr := runSystemGate(t, shell, root, marker, nil)
	if runErr == nil || !strings.Contains(output, "必须传入 SYSTEM_GATE_TREE_SHA") {
		t.Fatalf("归档模式缺少 tree 绑定未被拒绝: error=%v output=%s", runErr, output)
	}
	assertSystemDockerNotCalled(t, marker)
}

func TestSystemGateRejectsInvalidLiveFlagBeforeDocker(t *testing.T) {
	shell := systemGateShell(t)
	root := newSystemGateFixture(t)
	marker := filepath.Join(root, "docker-called")
	extra := []string{"AI_GDM_SYSTEM_LIVE_EXPOSURE=yes"}
	output, runErr := runSystemGate(t, shell, root, marker, extra)
	if runErr == nil || !strings.Contains(output, "AI_GDM_SYSTEM_LIVE_EXPOSURE 必须是 0 或 1") {
		t.Fatalf("非法 live 开关未被拒绝: error=%v output=%s", runErr, output)
	}
	assertSystemDockerNotCalled(t, marker)
}

func TestSystemGateRejectsInvalidAmapLiveFlagBeforeDocker(t *testing.T) {
	shell := systemGateShell(t)
	root := newSystemGateFixture(t)
	marker := filepath.Join(root, "docker-called")
	extra := []string{"AI_GDM_SYSTEM_LIVE_AMAP=yes"}
	output, runErr := runSystemGate(t, shell, root, marker, extra)
	if runErr == nil || !strings.Contains(output, "AI_GDM_SYSTEM_LIVE_AMAP 必须是 0 或 1") {
		t.Fatalf("非法高德 live 开关未被拒绝: error=%v output=%s", runErr, output)
	}
	assertSystemDockerNotCalled(t, marker)
}

func TestSystemGateRejectsGitDriftBeforeDocker(t *testing.T) {
	shell := systemGateShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("缺少 git")
	}
	root := newSystemGateFixture(t)
	runSystemGit(t, gitPath, root, "init", "-q")
	runSystemGit(t, gitPath, root, "add", "-f", "-A", "--", ".")
	if err = os.WriteFile(filepath.Join(root, "drift.txt"), []byte("drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "docker-called")
	output, runErr := runSystemGate(t, shell, root, marker, nil)
	if runErr == nil || !strings.Contains(output, "拒绝未跟踪源码漂移") {
		t.Fatalf("Git 漂移未被拒绝: error=%v output=%s", runErr, output)
	}
	assertSystemDockerNotCalled(t, marker)
}

func assertSystemGateFragments(t *testing.T, script string) {
	t.Helper()
	required := []string{
		"security_prepare_snapshot", "security_verify_stability",
		"SYSTEM_GATE_TREE_SHA", "SYSTEM_GATE_SOURCE_SHA256",
		"audit-results.test.mjs", "audit-results.mjs", "manifest.json",
		"docker ps -a --format '{{.Names}}'", "docker network ls --format '{{.Name}}'",
		"AI_GDM_LIVE_EXPOSURE=$LIVE_EXPOSURE", "disabled null",
		"security_hash_file \"$log_file\"", "duration_ms", "verify_gate_sentinel",
		"scripts/validate-loss-api.sh", "assessment_source_sha256",
		"scripts/validate-amap-live.sh", "AI_GDM_SYSTEM_LIVE_AMAP",
		"真实 LLM 上游暂不可用，模型目录与系统降级合同验证通过",
		"ASSESSMENT_E2E_TREE_SHA=$SECURITY_TREE_SHA",
		"ASSESSMENT_E2E_SOURCE_SHA256=$(assessment_source_sha256 \"$SNAPSHOT_DIR\")",
	}
	for _, value := range required {
		if !strings.Contains(script, value) {
			t.Fatalf("系统门禁缺少 %q", value)
		}
	}
	if strings.Contains(script, "validate-security.sh") || strings.Contains(script, "validate-security-browser.sh") {
		t.Fatal("P9.3 不得重复执行 P9.1 security 专项")
	}
}

func assertBrowserSentinels(t *testing.T, manifest systemManifest) {
	t.Helper()
	expected := map[string]int{"risk-map-chromium": 19, "evacuation-chromium": 42, "assessment-chromium": 120}
	for _, gate := range manifest.Gates {
		count, exists := expected[gate.ID]
		if !exists {
			continue
		}
		if gate.PassCount != count || gate.AuditMarker == "" {
			t.Fatalf("浏览器 sentinel 无效: %+v", gate)
		}
		delete(expected, gate.ID)
	}
	if len(expected) != 0 {
		t.Fatalf("缺少浏览器 sentinel: %v", expected)
	}
}

func assertSystemGateLimitations(t *testing.T, manifest systemManifest) {
	t.Helper()
	if len(manifest.Limitations) != 1 {
		t.Fatalf("系统门禁限制数量无效: %+v", manifest.Limitations)
	}
	expected := map[string]string{
		"amap-route": "candidate_routes_do_not_confirm_road_open",
	}
	for _, value := range manifest.Limitations {
		if value.State != "degraded" || expected[value.ID] != value.ReasonCode {
			t.Fatalf("系统门禁供应商边界无效: %+v", value)
		}
		delete(expected, value.ID)
	}
	if len(expected) != 0 {
		t.Fatalf("系统门禁缺少供应商限制: %v", expected)
	}
}

func assertLLMDegradedContract(t *testing.T, manifest systemManifest) {
	t.Helper()
	for _, gate := range manifest.Gates {
		if gate.ID != "live-llm" {
			continue
		}
		if gate.DegradedExitCode != 75 || gate.DegradedMarker !=
			"真实 LLM 上游暂不可用，模型目录与系统降级合同验证通过" ||
			gate.DegradedLimit.ID != "llm-provider" || gate.DegradedLimit.State != "degraded" ||
			gate.DegradedLimit.ReasonCode != "provider_upstream_error" {
			t.Fatalf("LLM 在线降级合同无效: %+v", gate)
		}
		return
	}
	t.Fatal("系统门禁缺少 LLM 在线降级合同")
}

func readSystemManifest(t *testing.T) systemManifest {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "tests", "system-gate", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result systemManifest
	if err = json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func readSystemGateFile(t *testing.T, name string) string {
	t.Helper()
	payload, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(payload), "\r\n", "\n")
}

func newSystemGateFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copySystemGateFile(t, "validate-system.sh", filepath.Join(root, "scripts"))
	copySystemGateFile(t, "security-gate.lib.sh", filepath.Join(root, "scripts"))
	copySystemExternalFile(t, filepath.Join("..", "tests", "system-gate", "manifest.json"),
		filepath.Join(root, "tests", "system-gate", "manifest.json"))
	copySystemExternalFile(t, filepath.Join("..", "tests", "system-gate", "audit-results.mjs"),
		filepath.Join(root, "tests", "system-gate", "audit-results.mjs"))
	return root
}

func copySystemGateFile(t *testing.T, name, destination string) {
	t.Helper()
	copySystemExternalFile(t, name, filepath.Join(destination, name))
}

func copySystemExternalFile(t *testing.T, source, destination string) {
	t.Helper()
	payload, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, payload, 0o755); err != nil {
		t.Fatal(err)
	}
}

func runSystemGate(t *testing.T, shell, root, marker string, extra []string) (string, error) {
	t.Helper()
	bin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := []byte("#!/usr/bin/env sh\nprintf called > \"$SYSTEM_DOCKER_MARKER\"\nexit 97\n")
	if err := os.WriteFile(filepath.Join(bin, "docker"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-system.sh")))
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SYSTEM_DOCKER_MARKER="+marker)
	command.Env = append(command.Env, extra...)
	payload, err := command.CombinedOutput()
	return string(payload), err
}

func runSystemGit(t *testing.T, gitPath, root string, args ...string) {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = root
	if payload, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v 失败: %v: %s", args, err, payload)
	}
}

func assertSystemDockerNotCalled(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("固定树或 live 预检前调用了 Docker")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func systemGateShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台未安装 sh，需在腾讯 Ubuntu 补跑行为门禁")
	}
	return shell
}
