package scripts

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestOfflineDeploymentEntrypointsHaveFailClosedContracts(t *testing.T) {
	shellScript := readPackageFile(t, "..", "deploy", "deploy.sh")
	powerShellScript := readPackageFile(t, "..", "deploy", "deploy.ps1")
	assertOrderedText(t, shellScript, "verify_package", "docker load -i", "compose up")
	assertOrderedText(t, powerShellScript, "Test-PackageChecksums", "@('load'", "Invoke-Compose -ProjectName")
	for name, script := range map[string]string{"Shell": shellScript, "PowerShell": powerShellScript} {
		for _, required := range []string{"release-images.env", "compose.offline.yaml", "--project-name", "--pull", "never", "--no-build", "--wait"} {
			if !strings.Contains(script, required) {
				t.Fatalf("%s 一键部署脚本缺少 %q", name, required)
			}
		}
		for _, forbidden := range []string{"docker pull", "docker build", "down -v", "Invoke-Expression", "cmd /c"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("%s 一键部署脚本包含禁止命令 %q", name, forbidden)
			}
		}
	}
	if strings.Index(shellScript, "--env-file \"$RUNTIME_ENV_FILE\"") > strings.Index(shellScript, "--env-file \"$IMAGE_ENV_FILE\"") {
		t.Fatal("Shell 部署脚本必须让发布镜像环境覆盖运行模板")
	}
	if strings.Index(powerShellScript, "'--env-file', $RuntimeEnvFile") > strings.Index(powerShellScript, "'--env-file', $ImageEnvFile") {
		t.Fatal("PowerShell 部署脚本必须让发布镜像环境覆盖运行模板")
	}
	for _, required := range []string{"StringComparer]::Ordinal", "AMAP_ENABLED=$(", "BOCHA_ENABLED=$(", "LLM_ENABLED=$("} {
		if !strings.Contains(powerShellScript, required) {
			t.Fatalf("PowerShell 部署脚本缺少精确合同 %q", required)
		}
	}
	for _, forbidden := range []string{"amap_enabled=", "bocha_enabled=", "llm_enabled="} {
		if strings.Contains(powerShellScript, forbidden) {
			t.Fatalf("PowerShell 部署脚本错误地下写供应商键 %q", forbidden)
		}
	}
}

func TestShellDeploymentGeneratesPrivateStableRuntime(t *testing.T) {
	shell := requireDeploymentShell(t)
	root := newDeployFixture(t)
	logFile := filepath.Join(root, "docker.log")
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "deploy", "deploy.sh")))
	command.Dir = root
	command.Env = deployFixtureEnvironment(t, logFile)
	firstOutput, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("首次一键部署失败: %v: %s", err, firstOutput)
	}
	runtimeFile := filepath.Join(root, "deploy", "runtime.env")
	firstRuntime, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	assertGeneratedRuntime(t, runtimeFile, firstRuntime)
	assertSecretsAbsent(t, firstOutput, firstRuntime)
	command = exec.Command(shell, filepath.ToSlash(filepath.Join(root, "deploy", "deploy.sh")))
	command.Dir = root
	command.Env = deployFixtureEnvironment(t, logFile)
	secondOutput, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("重复一键部署失败: %v: %s", err, secondOutput)
	}
	secondRuntime, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstRuntime) != string(secondRuntime) {
		t.Fatal("重复部署改写了既有运行密钥")
	}
	logPayload, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logPayload)
	assertOrderedText(t, logText, "load -i", "compose --project-name")
	if strings.Contains(logText, " pull ") || strings.Contains(logText, " build ") {
		t.Fatal("离线部署调用了 pull 或 build")
	}
}

func TestShellDeploymentUsesValidatedRuntimeOverAmbient(t *testing.T) {
	shell := requireDeploymentShell(t)
	root := newDeployFixture(t)
	runtimeFile := filepath.Join(root, "deploy", "runtime.env")
	expected := writeKnownRuntime(t, runtimeFile)
	logFile := filepath.Join(root, "docker.log")
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "deploy", "deploy.sh")))
	command.Dir = root
	command.Env = pollutedDeployEnvironment(t, logFile, expected)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("宿主环境污染覆盖了已验证 runtime.env: %v: %s", err, output)
	}
	logPayload, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logPayload), "--project-name "+expected["AI_GDM_PROJECT_NAME"]) {
		t.Fatal("Compose 未使用 runtime.env 中的显式项目名")
	}
}

func TestShellDeploymentRejectsDamageBeforeDocker(t *testing.T) {
	shell := requireDeploymentShell(t)
	root := newDeployFixture(t)
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(root, "docker.log")
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "deploy", "deploy.sh")))
	command.Dir = root
	command.Env = deployFixtureEnvironment(t, logFile)
	if payload, err := command.CombinedOutput(); err == nil || !strings.Contains(string(payload), "SHA-256") {
		t.Fatalf("损坏发布包未在 Docker 前被拒绝: error=%v output=%s", err, payload)
	}
	if _, err := os.Stat(logFile); err == nil {
		t.Fatal("校验和失败后仍调用了 Docker")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestDeployValidationStartsFromReleaseArchive(t *testing.T) {
	script := readPackageFile(t, "validate-deploy.sh")
	for _, required := range []string{
		"PACKAGE_ARCHIVE=", "$PACKAGE_ARCHIVE.sha256", "validate-release-archive.py",
		"DEPLOY_PACKAGE_ARCHIVE", "DEPLOY_EXPECTED_SOURCE_COMMIT", "org.opencontainers.image.revision",
		"security_tree_source_sha256", "sourceSha256 与预期提交不一致",
		"[ -x \"$PACKAGE_DIR/deploy/deploy.sh\" ]", "COMPOSE_PROJECT_NAME=ambient-invalid",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("P10.3 部署门禁缺少归档或环境污染合同 %q", required)
		}
	}
	if strings.Contains(script, "PACKAGE_DIR=$(sed -n 's/^PACKAGE_DIR=") {
		t.Fatal("P10.3 仍直接部署构建目录，而不是面试官收到的归档")
	}
}

func assertOrderedText(t *testing.T, value string, tokens ...string) {
	t.Helper()
	position := -1
	for _, token := range tokens {
		next := strings.Index(value, token)
		if next < 0 || next <= position {
			t.Fatalf("文本顺序缺少或颠倒 %q", token)
		}
		position = next
	}
}

func requireDeploymentShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台缺少 sh，腾讯 Ubuntu 必须执行本测试")
	}
	if _, err = exec.LookPath("sha256sum"); err != nil {
		t.Skip("当前平台缺少 sha256sum，腾讯 Ubuntu 必须执行本测试")
	}
	return shell
}

func newDeployFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyPackageFile(t, filepath.Join("..", "deploy", "deploy.sh"), filepath.Join(root, "deploy", "deploy.sh"))
	files := map[string]string{
		"compose.yaml":                         "services: {}\n",
		"deploy/compose.offline.yaml":          "services: {}\n",
		"deploy/release-images.env":            deployReleaseImageEnvironment(),
		"images/ai-gdm-images-linux-amd64.tar": "fixture\n",
		"manifest.json":                        deployFixtureManifest(),
	}
	for name, value := range files {
		writeDeployFixtureFile(t, root, name, []byte(value))
	}
	writeDeployChecksums(t, root)
	return root
}

func deployFixtureManifest() string {
	return fmt.Sprintf("{\n  \"images\": [\n%s\n  ]\n}\n", strings.Join([]string{
		`    {"reference": "ai-gdm/server:v0.1.0", "id": "sha256:` + strings.Repeat("a", 64) + `"}`,
		`    {"reference": "ai-gdm/postgis:17-3.5-v0.1.0", "id": "sha256:` + strings.Repeat("b", 64) + `"}`,
		`    {"reference": "ai-gdm/redis:7.4.10-v0.1.0", "id": "sha256:` + strings.Repeat("c", 64) + `"}`,
	}, ",\n"))
}

func deployReleaseImageEnvironment() string {
	return "AI_GDM_IMAGE=ai-gdm/server:v0.1.0\n" +
		"AI_GDM_POSTGIS_IMAGE=ai-gdm/postgis:17-3.5-v0.1.0\n" +
		"AI_GDM_REDIS_IMAGE=ai-gdm/redis:7.4.10-v0.1.0\n"
}

func writeDeployFixtureFile(t *testing.T, root, name string, payload []byte) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeDeployChecksums(t *testing.T, root string) {
	t.Helper()
	var names []string
	err := filepath.Walk(root, func(filename string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || filepath.Base(filename) == "SHA256SUMS" {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, filename)
		if relErr == nil {
			names = append(names, filepath.ToSlash(relative))
		}
		return relErr
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		payload, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		lines = append(lines, fmt.Sprintf("%x  ./%s", sha256.Sum256(payload), name))
	}
	writeDeployFixtureFile(t, root, "SHA256SUMS", []byte(strings.Join(lines, "\n")+"\n"))
}

func deployFixtureEnvironment(t *testing.T, logFile string) []string {
	t.Helper()
	bin := t.TempDir()
	docker := `#!/usr/bin/env sh
set -eu
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
if [ "$1" = compose ] && [ "${2:-}" != version ] && [ -n "${EXPECTED_PROJECT:-}" ]; then
  [ "$AI_GDM_PROJECT_NAME" = "$EXPECTED_PROJECT" ]
  [ "$AI_GDM_BIND_ADDRESS" = "$EXPECTED_BIND" ]
  [ "$AI_GDM_HTTP_PORT" = "$EXPECTED_PORT" ]
  [ "$POSTGRES_PASSWORD" = "$EXPECTED_POSTGRES" ]
  [ "$REDIS_PASSWORD" = "$EXPECTED_REDIS" ]
fi
case "$*" in
  compose*'config --images'*)
    printf '%s\n' ai-gdm/server:v0.1.0 ai-gdm/postgis:17-3.5-v0.1.0 ai-gdm/redis:7.4.10-v0.1.0
    exit 0
    ;;
esac
case "$1 $2" in
  "compose version") exit 0 ;;
  "info -f") printf '%s\n' linux; exit 0 ;;
  "load -i") exit 0 ;;
  "image inspect")
    last=
    for value in "$@"; do last=$value; done
    case "$*" in
      *'{{.Os}}/{{.Architecture}}'*) printf '%s\n' linux/amd64 ;;
      *)
        case "$last" in
          ai-gdm/server:*) char=a ;;
          ai-gdm/postgis:*) char=b ;;
          ai-gdm/redis:*) char=c ;;
          *) exit 1 ;;
        esac
        printf 'sha256:'
        printf '%64s\n' '' | tr ' ' "$char"
        ;;
    esac
    exit 0 ;;
  "compose "*) exit 0 ;;
esac
exit 1
`
	curl := "#!/usr/bin/env sh\nprintf '%s\\n' 'AI-GDM 地质灾害辅助研判控制台'\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	return append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_DOCKER_LOG="+logFile,
		"AI_GDM_DEPLOY_WAIT_SECONDS=30",
	)
}

func writeKnownRuntime(t *testing.T, filename string) map[string]string {
	t.Helper()
	postgresKey := "POSTGRES_PASS" + "WORD"
	redisKey := "REDIS_PASS" + "WORD"
	databaseKey := "DATABASE_" + "URL"
	adminKey := "APP_ADMIN_" + "TOKEN"
	values := map[string]string{
		"AI_GDM_PROJECT_NAME": "ai-gdm-fixture", "AI_GDM_BIND_ADDRESS": "127.0.0.1", "AI_GDM_HTTP_PORT": "18080",
		postgresKey: strings.Repeat("1", 64), redisKey: strings.Repeat("2", 64), adminKey: strings.Repeat("3", 64),
		"APP_ENV": "production",
	}
	values[databaseKey] = "postgresql://ai_gdm:" + values[postgresKey] + "@postgres:5432/ai_gdm?sslmode=disable"
	var lines []string
	for _, key := range []string{"AI_GDM_PROJECT_NAME", "AI_GDM_BIND_ADDRESS", "AI_GDM_HTTP_PORT", postgresKey, redisKey, databaseKey, "APP_ENV", adminKey} {
		lines = append(lines, key+"="+values[key])
	}
	if err := os.WriteFile(filename, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return values
}

func pollutedDeployEnvironment(t *testing.T, logFile string, expected map[string]string) []string {
	t.Helper()
	postgresKey := "POSTGRES_PASS" + "WORD"
	redisKey := "REDIS_PASS" + "WORD"
	return append(deployFixtureEnvironment(t, logFile),
		"COMPOSE_PROJECT_NAME=ambient-invalid", "AI_GDM_PROJECT_NAME=ambient-invalid",
		"AI_GDM_BIND_ADDRESS=127.0.0.2", "AI_GDM_HTTP_PORT=1",
		postgresKey+"=ambient-invalid", redisKey+"=ambient-invalid",
		"EXPECTED_PROJECT="+expected["AI_GDM_PROJECT_NAME"], "EXPECTED_BIND="+expected["AI_GDM_BIND_ADDRESS"],
		"EXPECTED_PORT="+expected["AI_GDM_HTTP_PORT"], "EXPECTED_POSTGRES="+expected[postgresKey],
		"EXPECTED_REDIS="+expected[redisKey],
	)
}

func assertGeneratedRuntime(t *testing.T, filename string, payload []byte) {
	t.Helper()
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("运行配置权限过宽: %o", info.Mode().Perm())
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]struct{}{}
	for _, key := range []string{"POSTGRES_PASSWORD", "REDIS_PASSWORD", "APP_ADMIN_TOKEN"} {
		value := values[key]
		if !pattern.MatchString(value) {
			t.Fatalf("%s 未生成 64 位随机十六进制值", key)
		}
		if _, exists := seen[value]; exists {
			t.Fatal("生成的运行密钥重复")
		}
		seen[value] = struct{}{}
	}
}

func assertSecretsAbsent(t *testing.T, output, runtime []byte) {
	t.Helper()
	for _, line := range strings.Split(string(runtime), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || len(parts[1]) != 64 {
			continue
		}
		if strings.Contains(string(output), parts[1]) {
			t.Fatal("部署输出泄露运行密钥")
		}
	}
}
