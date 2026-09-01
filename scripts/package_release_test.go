package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePackagingContracts(t *testing.T) {
	packageScript := readPackageFile(t, "package-release.sh")
	validateScript := readPackageFile(t, "validate-package.sh")
	offlineCompose := readPackageFile(t, "..", "deploy", "compose.offline.yaml")
	for _, value := range []string{
		"security_prepare_snapshot", "GOOS=linux", "GOOS=windows", "docker save",
		"ai-gdm-images-linux-amd64.tar", "manifest.json", "SHA256SUMS", "security_verify_stability",
		"cleanup_partial_output", "PACKAGE_REQUIRE_SOURCE_COMMIT", "PACKAGE_MIN_FREE_KB",
		"ai.gdm.package.run", "BUILD_CONTAINER", "IMAGE_REVISION", "deploy/deploy.sh", "deploy/deploy.ps1",
		"README.md", "docs/data-sources-v1.md", "docs/model-cards-v1.md",
		"docs/limitations-v1.md", "RELEASE_NOTES_RELATIVE", "docs/release-$VERSION.md",
	} {
		if !strings.Contains(packageScript, value) {
			t.Fatalf("发布打包脚本缺少 %q", value)
		}
	}
	for _, value := range []string{
		"validate-release-archive.py", "ELF 64-bit", "PE32+", "go version -m", "expected-files",
		"go run ./cmd/releasecheck", "PACKAGE_ARCHIVE_INPUT", "PACKAGE_EXPECTED_SOURCE_COMMIT",
		"security_tree_source_sha256", "sourceSha256 与预期提交不一致",
		"ai.gdm.package.validation.run", "./deploy/deploy.sh", "./deploy/deploy.ps1",
		"./README.md", "./docs/data-sources-v1.md", "./docs/model-cards-v1.md",
		"./docs/limitations-v1.md", "./docs/release-$PACKAGE_VERSION.md",
	} {
		if !strings.Contains(validateScript, value) {
			t.Fatalf("发布包门禁缺少 %q", value)
		}
	}
	for _, value := range []string{"AI_GDM_IMAGE", "AI_GDM_POSTGIS_IMAGE", "AI_GDM_REDIS_IMAGE"} {
		if !strings.Contains(offlineCompose, value) {
			t.Fatalf("离线 Compose 缺少 %s", value)
		}
	}
	if strings.Count(offlineCompose, "pull_policy: never") != 3 {
		t.Fatal("离线 Compose 必须禁止三个运行服务拉取镜像")
	}
	if strings.Contains(packageScript, "docker system prune") {
		t.Fatal("打包流程不得清理不属于本阶段的 Docker 资源")
	}
}

func TestPackageReleaseTreatsRepositoryWithoutHeadAsUncommitted(t *testing.T) {
	script := readPackageFile(t, "package-release.sh")
	assertOrderedText(t, script,
		"rev-parse --verify HEAD", "requested=$detected", "requested=unknown", "SOURCE_COMMIT=$requested")
	if strings.Contains(script, "rev-parse HEAD 2>/dev/null ||") {
		t.Fatal("无 HEAD 仓库会把 rev-parse 的字面输出与 unknown 拼接")
	}
}

func TestPackageRejectsInvalidMetadataBeforeDocker(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台缺少 sh，腾讯 Ubuntu 必须执行本测试")
	}
	tests := []struct {
		name  string
		extra []string
	}{
		{name: "版本前导零", extra: []string{"PACKAGE_VERSION=v01.0.0"}},
		{name: "多余版本段", extra: []string{"PACKAGE_VERSION=v1.2.3.4"}},
		{name: "非数字版本", extra: []string{"PACKAGE_VERSION=v1.a.3"}},
		{name: "非 UTC 构建时间", extra: []string{"PACKAGE_CREATED_AT=2026-08-30T08:00:00+08:00"}},
		{name: "非法最低空间", extra: []string{"PACKAGE_MIN_FREE_KB=none"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertPackageFailsBeforeDocker(t, shell, test.extra)
		})
	}
}

func TestReleaseOutputsRemainIgnored(t *testing.T) {
	ignore := readPackageFile(t, "..", ".gitignore")
	for _, value := range []string{"dist/", "*.exe"} {
		if !strings.Contains(ignore, value) {
			t.Fatalf("发布产物忽略规则缺少 %q", value)
		}
	}
}

func TestPackageArchiveRequiresExternalBindingsBeforeDocker(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台缺少 sh，腾讯 Ubuntu 必须执行本测试")
	}
	root := newPackageGateFixture(t)
	marker := filepath.Join(root, "docker-called")
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "package-release.sh")))
	command.Dir = root
	command.Env = append(os.Environ(), packageGateEnvironment(t, root, marker)...)
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

func TestPackageArchiveRejectsOutputInsideSource(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台缺少 sh，腾讯 Ubuntu 必须执行本测试")
	}
	root := newPackageGateFixture(t)
	marker := filepath.Join(root, "docker-called")
	tree, source := packageArchiveBinding(t, shell, root)
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "package-release.sh")))
	command.Dir = root
	command.Env = append(os.Environ(), packageGateEnvironment(t, root, marker)...)
	command.Env = append(command.Env, "SECURITY_E2E_TREE_SHA="+tree, "SECURITY_E2E_SOURCE_SHA256="+source)
	payload, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(payload), "发布目录必须位于源码目录之外") {
		t.Fatalf("归档输出目录边界未生效: error=%v output=%s", runErr, payload)
	}
	assertPackageDockerWasNotCalled(t, marker)
}

func TestPackageRejectsCommitThatDoesNotOwnTree(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台缺少 sh，腾讯 Ubuntu 必须执行本测试")
	}
	root := newPackageGateFixture(t)
	initPackageGitFixture(t, root)
	marker := filepath.Join(root, "docker-called")
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "package-release.sh")))
	command.Dir = root
	command.Env = append(os.Environ(), packageGateEnvironment(t, root, marker)...)
	command.Env = append(command.Env, "PACKAGE_SOURCE_COMMIT="+strings.Repeat("0", 40), "PACKAGE_REQUIRE_SOURCE_COMMIT=1")
	payload, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(payload), "与发布 tree 不一致") {
		t.Fatalf("错误 commit 未被拒绝: error=%v output=%s", runErr, payload)
	}
	assertPackageDockerWasNotCalled(t, marker)
}

func assertPackageFailsBeforeDocker(t *testing.T, shell string, extra []string) {
	t.Helper()
	root := newPackageGateFixture(t)
	marker := filepath.Join(root, "docker-called")
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "package-release.sh")))
	command.Dir = root
	command.Env = append(os.Environ(), packageGateEnvironment(t, root, marker)...)
	command.Env = append(command.Env, extra...)
	if payload, err := command.CombinedOutput(); err == nil {
		t.Fatalf("非法发布元数据未被拒绝: %s", payload)
	}
	assertPackageDockerWasNotCalled(t, marker)
}

func assertPackageDockerWasNotCalled(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("发布边界校验前调用了 Docker")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func packageArchiveBinding(t *testing.T, shell, root string) (string, string) {
	t.Helper()
	command := exec.Command(shell, "-c", `. scripts/security-gate.lib.sh; printf '%s\n%s\n' "$(security_archive_tree .)" "$(security_source_sha256 .)"`)
	command.Dir = root
	command.Env = append(os.Environ(), "AI_GDM_SECURITY_GATE_LIBRARY=source")
	payload, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("计算归档绑定失败: %v: %s", err, payload)
	}
	values := strings.Fields(string(payload))
	if len(values) != 2 {
		t.Fatalf("归档绑定输出无效: %q", payload)
	}
	return values[0], values[1]
}

func initPackageGitFixture(t *testing.T, root string) {
	t.Helper()
	commands := [][]string{{"init", "-q"}, {"add", "-f", "-A", "--", "."}, {"-c", "user.name=AI-GDM", "-c", "user.email=release@example.invalid", "commit", "-qm", "fixture"}}
	for _, arguments := range commands {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if payload, err := command.CombinedOutput(); err != nil {
			t.Fatalf("初始化 Git fixture 失败: %v: %s", err, payload)
		}
	}
}

func newPackageGateFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyPackageFile(t, filepath.Join("..", "scripts", "package-release.sh"), filepath.Join(root, "scripts", "package-release.sh"))
	copyPackageFile(t, filepath.Join("..", "scripts", "security-gate.lib.sh"), filepath.Join(root, "scripts", "security-gate.lib.sh"))
	copyPackageFile(t, filepath.Join("..", "docs", "release-v0.1.1.md"), filepath.Join(root, "docs", "release-v0.1.1.md"))
	return root
}

func copyPackageFile(t *testing.T, source, destination string) {
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

func packageGateEnvironment(t *testing.T, root, marker string) []string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := []byte("#!/usr/bin/env sh\nprintf called > \"$PACKAGE_DOCKER_MARKER\"\nexit 97\n")
	if err := os.WriteFile(filepath.Join(bin, "docker"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PACKAGE_DOCKER_MARKER=" + marker,
	}
}

func readPackageFile(t *testing.T, parts ...string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(payload), "\r\n", "\n")
}
