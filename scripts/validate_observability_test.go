package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObservabilityGateRunsRequiredPackagesAndRace(t *testing.T) {
	payload, err := os.ReadFile("validate-observability.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	required := []string{
		"security_prepare_snapshot",
		"security_verify_stability",
		"OBSERVABILITY_GATE_TREE_SHA",
		"OBSERVABILITY_GATE_SOURCE_SHA256",
		"ai-gdm-observability-go-$RUN_ID",
		"--cidfile",
		"ai.gdm.observability.run=$RUN_ID",
		"golang:1.26.7-bookworm@sha256:",
		`-v "$SNAPSHOT_DIR:/src:ro"`,
		"go test -race",
		"./internal/platform/observability",
		"./internal/platform/httpserver",
		"./internal/application/dashboard",
		"./internal/adapters/http/webui",
		"./cmd/server -count=20",
		"go vet ./...",
		"go build ./...",
	}
	for _, value := range required {
		if !strings.Contains(text, value) {
			t.Fatalf("P9.2 门禁缺少 %q", value)
		}
	}
}

func TestObservabilityGateRejectsUnstagedDriftBeforeDocker(t *testing.T) {
	shell := observabilityShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("缺少 git")
	}
	root := newObservabilityGateRepository(t, gitPath)
	readme := filepath.Join(root, "README.txt")
	if err = os.WriteFile(readme, []byte("drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "docker-called")
	output, runErr := runObservabilityGate(t, shell, root, marker)
	if runErr == nil || !strings.Contains(output, "拒绝未暂存源码漂移") {
		t.Fatalf("未暂存漂移未被拒绝: error=%v output=%s", runErr, output)
	}
	assertObservabilityDockerNotCalled(t, marker)
}

func TestObservabilityArchiveRequiresExternalBindingsBeforeDocker(t *testing.T) {
	shell := observabilityShell(t)
	root := t.TempDir()
	copyObservabilityGateFile(t, "validate-observability.sh", filepath.Join(root, "scripts"))
	copyObservabilityGateFile(t, "security-gate.lib.sh", filepath.Join(root, "scripts"))
	marker := filepath.Join(root, "docker-called")
	output, runErr := runObservabilityGate(t, shell, root, marker)
	if runErr == nil || !strings.Contains(output, "必须传入 OBSERVABILITY_GATE_TREE_SHA") {
		t.Fatalf("归档模式缺少 tree 绑定未被拒绝: error=%v output=%s", runErr, output)
	}
	assertObservabilityDockerNotCalled(t, marker)
}

func newObservabilityGateRepository(t *testing.T, gitPath string) string {
	t.Helper()
	root := t.TempDir()
	copyObservabilityGateFile(t, "validate-observability.sh", filepath.Join(root, "scripts"))
	copyObservabilityGateFile(t, "security-gate.lib.sh", filepath.Join(root, "scripts"))
	if err := os.WriteFile(filepath.Join(root, "README.txt"), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runObservabilityGit(t, gitPath, root, "init", "-q")
	runObservabilityGit(t, gitPath, root, "add", "-f", "-A", "--", ".")
	return root
}

func copyObservabilityGateFile(t *testing.T, name, destination string) {
	t.Helper()
	payload, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(destination, name), payload, 0o755); err != nil {
		t.Fatal(err)
	}
}

func runObservabilityGit(t *testing.T, gitPath, root string, args ...string) string {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = root
	payload, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v 失败: %v: %s", args, err, payload)
	}
	return string(payload)
}

func runObservabilityGate(t *testing.T, shell, root, marker string) (string, error) {
	t.Helper()
	bin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := []byte("#!/usr/bin/env sh\nprintf called > \"$OBSERVABILITY_DOCKER_MARKER\"\nexit 97\n")
	if err := os.WriteFile(filepath.Join(bin, "docker"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-observability.sh")))
	command.Dir = root
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OBSERVABILITY_DOCKER_MARKER="+marker)
	payload, err := command.CombinedOutput()
	return string(payload), err
}

func assertObservabilityDockerNotCalled(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("固定树预检前调用了 Docker: %v", err)
	}
}

func observabilityShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台未安装 sh，需在腾讯 Ubuntu 补跑行为门禁")
	}
	return shell
}
