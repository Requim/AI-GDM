package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerDeploymentFilesKeepProductionBoundaries(t *testing.T) {
	dockerfile := readDockerFile(t, "..", "Dockerfile")
	compose := readDockerFile(t, "..", "compose.yaml")
	environment := readDockerFile(t, "..", "deploy", "runtime.env.example")
	for _, value := range []string{"USER 10001:10001", "ai.gdm.gdal.version=\"3.13.3\"", "ai-gdm-healthcheck", "ENTRYPOINT"} {
		if !strings.Contains(dockerfile, value) {
			t.Fatalf("Dockerfile 缺少 %q", value)
		}
	}
	assertComposeBoundaries(t, compose)
	assertEnvironmentHasNoSecret(t, environment)
}

func TestDockerValidationScriptBindsFixedTreeAndRuntimeContracts(t *testing.T) {
	script := readDockerFile(t, "validate-docker.sh")
	required := []string{
		"security_prepare_snapshot", "security_verify_stability", "security_random_token",
		"--iidfile", "ai.gdm.docker.run", "compose up -d --wait", "ReadonlyRootfs",
		"GDAL 3.13.3", "schema_migrations", "docker port", "p10_validation",
		"verify_secret_logs", "verify_no_owned_resources", "compose down -v --remove-orphans",
	}
	for _, value := range required {
		if !strings.Contains(script, value) {
			t.Fatalf("P10.1 容器门禁缺少 %q", value)
		}
	}
}

func TestDockerGateArchiveRequiresTreeAndSourceBeforeDocker(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台缺少 sh，腾讯 Ubuntu 必须执行本测试")
	}
	root := newDockerGateFixture(t)
	marker := filepath.Join(root, "docker-called")
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-docker.sh")))
	command.Dir = root
	command.Env = append(os.Environ(), dockerGateEnvironment(t, root, marker)...)
	payload, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(payload), "必须传入 SECURITY_E2E_TREE_SHA") {
		t.Fatalf("归档缺少 tree 绑定未被拒绝: error=%v output=%s", runErr, payload)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("固定树校验前调用了 Docker")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}

func assertComposeBoundaries(t *testing.T, compose string) {
	t.Helper()
	for _, value := range []string{
		"pull_policy: never", "read_only: true", "no-new-privileges:true", "cap_drop:",
		"internal: true", "postgres-data:", "redis-data:", "lhasa-data:",
		"postgis/postgis:17-3.5@sha256:", "redis:7.4.10-bookworm@sha256:",
	} {
		if !strings.Contains(compose, value) {
			t.Fatalf("Compose 缺少 %q", value)
		}
	}
	if strings.Contains(compose, "container_name:") {
		t.Fatal("Compose 不得使用固定容器名")
	}
}

func assertEnvironmentHasNoSecret(t *testing.T, environment string) {
	t.Helper()
	for _, name := range []string{"POSTGRES_PASSWORD", "REDIS_PASSWORD", "DATABASE_URL", "APP_ADMIN_TOKEN", "AMAP_API_KEY", "BOCHA_API_KEY", "LLM_API_KEY"} {
		if !strings.Contains(environment, name+"=") {
			t.Fatalf("运行配置缺少 %s", name)
		}
	}
	if strings.Contains(environment, "sk-") {
		t.Fatal("运行配置示例包含真实密钥")
	}
}

func newDockerGateFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyDockerGateFile(t, filepath.Join("..", "scripts", "validate-docker.sh"), filepath.Join(root, "scripts", "validate-docker.sh"))
	copyDockerGateFile(t, filepath.Join("..", "scripts", "security-gate.lib.sh"), filepath.Join(root, "scripts", "security-gate.lib.sh"))
	return root
}

func copyDockerGateFile(t *testing.T, source, destination string) {
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

func dockerGateEnvironment(t *testing.T, root, marker string) []string {
	t.Helper()
	bin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := []byte("#!/usr/bin/env sh\nprintf called > \"$DOCKER_GATE_MARKER\"\nexit 97\n")
	if err := os.WriteFile(filepath.Join(bin, "docker"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_GATE_MARKER=" + marker,
	}
}

func readDockerFile(t *testing.T, parts ...string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(payload), "\r\n", "\n")
}
