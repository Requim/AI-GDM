package scripts_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSecurityBrowserRejectsOldTreeBeforeDocker(t *testing.T) {
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	oldTree := securityGateGit(t, root, "write-tree")
	securityWriteFile(t, filepath.Join(root, "README.txt"), "changed\n", 0o644)
	securityGateGit(t, root, "add", "README.txt")
	state := newFakeSecurityRuntime(t)
	output, err := runSecurityGate(shell, root, "validate-security-browser.sh", state,
		map[string]string{"SECURITY_E2E_TREE_SHA": oldTree})
	if err == nil || !strings.Contains(output, "SECURITY_E2E_TREE_SHA 与实际 tree 不一致") {
		t.Fatalf("旧 tree 未在 Docker 前被拒绝: error=%v output=%s", err, output)
	}
	if payload, _ := os.ReadFile(state.dockerLog); len(payload) != 0 {
		t.Fatalf("旧 tree 失败后仍调用 Docker: %s", payload)
	}
}

func TestSecurityBrowserRejectsWorktreeDrift(t *testing.T) {
	shell := securityGateShell(t)
	for _, scenario := range []string{"unstaged", "untracked"} {
		t.Run(scenario, func(t *testing.T) {
			root := newSecurityGateRepository(t)
			if scenario == "unstaged" {
				securityWriteFile(t, filepath.Join(root, "README.txt"), "unstaged\n", 0o644)
			} else {
				securityWriteFile(t, filepath.Join(root, "untracked.txt"), "untracked\n", 0o644)
			}
			output, err := runSecurityGate(shell, root, "validate-security-browser.sh",
				newFakeSecurityRuntime(t), nil)
			if err == nil || !strings.Contains(output, "源码漂移") {
				t.Fatalf("%s 漂移未被拒绝: error=%v output=%s", scenario, err, output)
			}
		})
	}
}

func TestSecurityBrowserPreparedSnapshotRejectsDirtyBindings(t *testing.T) {
	shell := securityGateShell(t)
	for _, scenario := range []string{"unstaged", "untracked", "ignored", "hidden-index"} {
		t.Run(scenario, func(t *testing.T) {
			root := newSecurityGateRepository(t)
			if scenario == "ignored" {
				securityWriteFile(t, filepath.Join(root, ".gitignore"), "ignored.txt\n", 0o644)
				securityGateGit(t, root, "add", ".gitignore")
			}
			tree := securityGateGit(t, root, "write-tree")
			prepareDirtySecuritySnapshot(t, root, scenario)
			values := map[string]string{
				"SECURITY_GATE_PREPARED_ROOT": root, "SECURITY_E2E_TREE_SHA": tree,
				"SECURITY_E2E_SOURCE_SHA256": securitySourceDigest(t, shell, root),
			}
			state := newFakeSecurityRuntime(t)
			output, err := runSecurityGate(shell, root, "validate-security-browser.sh", state, values)
			if err == nil || !strings.Contains(output, "安全门禁拒绝") &&
				!strings.Contains(output, "无法由 tree 重建") {
				t.Fatalf("prepared %s 漂移未被拒绝: error=%v output=%s", scenario, err, output)
			}
			if payload, _ := os.ReadFile(state.dockerLog); len(payload) != 0 {
				t.Fatalf("prepared %s 漂移后仍调用 Docker: %s", scenario, payload)
			}
		})
	}
}

func prepareDirtySecuritySnapshot(t *testing.T, root, scenario string) {
	t.Helper()
	switch scenario {
	case "unstaged":
		securityWriteFile(t, filepath.Join(root, "README.txt"), "dirty\n", 0o644)
	case "untracked":
		securityWriteFile(t, filepath.Join(root, "dirty.txt"), "dirty\n", 0o644)
	case "ignored":
		securityWriteFile(t, filepath.Join(root, "ignored.txt"), "dirty\n", 0o644)
	case "hidden-index":
		securityGateGit(t, root, "update-index", "--skip-worktree", "README.txt")
		securityWriteFile(t, filepath.Join(root, "README.txt"), "hidden dirty\n", 0o644)
	}
}

func TestSecurityBrowserArchiveRequiresExactBindings(t *testing.T) {
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	state := newFakeSecurityRuntime(t)
	output, err := runSecurityGate(shell, root, "validate-security-browser.sh", state, nil)
	if err == nil || !strings.Contains(output, "必须传入 SECURITY_E2E_TREE_SHA") {
		t.Fatalf("归档未绑定 tree 仍继续执行: error=%v output=%s", err, output)
	}
	values := map[string]string{
		"SECURITY_E2E_TREE_SHA":      strings.Repeat("0", 40),
		"SECURITY_E2E_SOURCE_SHA256": strings.Repeat("0", 64),
	}
	output, err = runSecurityGate(shell, root, "validate-security-browser.sh", state, values)
	if err == nil || !strings.Contains(output, "与实际 tree 不一致") {
		t.Fatalf("归档错误绑定未被拒绝: error=%v output=%s", err, output)
	}
	tree, digest := securityArchiveBindings(t, shell, root)
	values["SECURITY_E2E_TREE_SHA"] = tree
	output, err = runSecurityGate(shell, root, "validate-security-browser.sh", state, values)
	if err == nil || !strings.Contains(output, "SOURCE_SHA256 与实际受审计源码不一致") {
		t.Fatalf("归档错误 source 未被拒绝: error=%v output=%s", err, output)
	}
	values["SECURITY_E2E_SOURCE_SHA256"] = digest
	output, err = runSecurityGate(shell, root, "validate-security-browser.sh", state, values)
	if err != nil || !strings.Contains(output, "mode=archive tree="+tree) {
		t.Fatalf("归档精确绑定未通过: error=%v output=%s", err, output)
	}
}

func TestSecurityBrowserRejectsRuntimeDrift(t *testing.T) {
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	state := newFakeSecurityRuntime(t)
	state.values["FAKE_MUTATE_FILE"] = filepath.Join(root, "README.txt")
	state.values["FAKE_MUTATE_MARKER"] = filepath.Join(t.TempDir(), "mutated")
	output, err := runSecurityGate(shell, root, "validate-security-browser.sh", state, nil)
	if err == nil || !strings.Contains(output, "未暂存源码漂移") {
		t.Fatalf("运行中源码漂移未被拒绝: error=%v output=%s", err, output)
	}
	if strings.Contains(output, "安全浏览器回归通过") {
		t.Fatalf("运行中漂移后仍发布成功声明: %s", output)
	}
}

func TestSecurityBrowserConcurrentRunsUseDisjointDockerNames(t *testing.T) {
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	state := newFakeSecurityRuntime(t)
	type result struct {
		output string
		err    error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for _, port := range []string{"18081", "18082"} {
		group.Add(1)
		go func(value string) {
			defer group.Done()
			output, err := runSecurityGate(shell, root, "validate-security-browser.sh", state,
				map[string]string{"SECURITY_E2E_PORT": value})
			results <- result{output: output, err: err}
		}(port)
	}
	group.Wait()
	close(results)
	for item := range results {
		if item.err != nil || !strings.Contains(item.output, "安全浏览器回归通过") {
			t.Fatalf("并发安全门禁失败: error=%v output=%s", item.err, item.output)
		}
	}
	assertDisjointSecurityDockerObjects(t, state)
}

func TestSecurityBrowserRejectsUnpinnedValidationImageBeforeDocker(t *testing.T) {
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	state := newFakeSecurityRuntime(t)
	output, err := runSecurityGate(shell, root, "validate-security-browser.sh", state,
		map[string]string{"GO_VALIDATION_IMAGE": "golang:1.26.7-bookworm"})
	if err == nil || !strings.Contains(output, "必须使用 sha256 镜像摘要") {
		t.Fatalf("未固定 Go 镜像仍继续执行: error=%v output=%s", err, output)
	}
	if payload, _ := os.ReadFile(state.dockerLog); len(payload) != 0 {
		t.Fatalf("未固定镜像被拒绝后仍调用 Docker: %s", payload)
	}
}

func TestSecurityBrowserDoesNotDeletePreexistingDockerObjects(t *testing.T) {
	shell := securityGateShell(t)
	for _, kind := range []string{"container", "image"} {
		t.Run(kind, func(t *testing.T) {
			root := newSecurityGateRepository(t)
			state := newFakeSecurityRuntime(t)
			runID := strings.Repeat("a", 32)
			forceSecurityRunID(t, state, runID)
			path := seedSecurityBrowserCollision(t, state, root, runID, kind)
			output, err := runSecurityGate(shell, root, "validate-security-browser.sh", state, nil)
			if err == nil || !strings.Contains(output, "已被占用") {
				t.Fatalf("外部 %s 碰撞未被拒绝: error=%v output=%s", kind, err, output)
			}
			if _, statErr := os.Stat(path); statErr != nil {
				t.Fatalf("安全门禁误删外部 %s: %v", kind, statErr)
			}
		})
	}
}

func TestSecurityBrowserSignalsCleanImageCreatedBeforeBuildReturns(t *testing.T) {
	shell := securityGateShell(t)
	for _, item := range []struct {
		name string
		code int
	}{{"HUP", 129}, {"INT", 130}, {"TERM", 143}} {
		t.Run(strings.ToLower(item.name), func(t *testing.T) {
			runSecurityBrowserBuildSignalScenario(t, shell, item.name, item.code)
		})
	}
}

func runSecurityBrowserBuildSignalScenario(t *testing.T, shell, signal string, wantCode int) {
	t.Helper()
	root := newSecurityGateRepository(t)
	state := newFakeSecurityRuntime(t)
	foreignTag := seedSecurityForeignImage(t, state)
	marker := filepath.Join(t.TempDir(), "build-ready")
	pidfile := filepath.Join(t.TempDir(), "build.pid")
	values := map[string]string{
		"FAKE_BLOCK_BUILD_AFTER_IID_MARKER": marker,
		"FAKE_BLOCK_BUILD_PIDFILE":          pidfile,
	}
	command := newSecurityGateCommand(shell, root, "validate-security-browser.sh", state, values)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitSecurityBrowserPath(t, marker)
	tag := singleSecurityBuildTag(t, state)
	sendSecurityBrowserSignal(t, command.Process.Pid, signal)
	err := waitSecurityBrowserCommand(command, 10*time.Second)
	assertSecuritySignalExit(t, err, wantCode, output.String())
	assertSecurityBuildChildStopped(t, pidfile)
	assertSecurityImageCleanup(t, state, tag, foreignTag)
}

func seedSecurityForeignImage(t *testing.T, state *fakeSecurityRuntime) string {
	t.Helper()
	tag := "foreign-security-image:stable"
	digest := strings.Repeat("e", 64)
	securityWriteFile(t, filepath.Join(state.dockerState, "images", digest),
		"sha256:"+digest+"|foreign|foreign|foreign\n", 0o600)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(tag)))
	securityWriteFile(t, filepath.Join(state.dockerState, "tags", key), "sha256:"+digest+"\n", 0o600)
	return tag
}

func waitSecurityBrowserPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待安全浏览器阻塞点超时: %s", path)
}

func singleSecurityBuildTag(t *testing.T, state *fakeSecurityRuntime) string {
	t.Helper()
	tags := extractSecurityArguments(securityLogLines(t, state.dockerLog), "build", "-t")
	if len(tags) != 1 {
		t.Fatalf("安全浏览器 build tag 数量无效: %v", tags)
	}
	return tags[0]
}

func sendSecurityBrowserSignal(t *testing.T, pid int, signal string) {
	t.Helper()
	if payload, err := exec.Command("kill", "-"+signal, strconv.Itoa(pid)).CombinedOutput(); err != nil {
		t.Fatalf("发送 %s 信号失败: %v: %s", signal, err, payload)
	}
}

func waitSecurityBrowserCommand(command *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = command.Process.Kill()
		<-done
		return fmt.Errorf("安全浏览器门禁退出超时")
	}
}

func assertSecuritySignalExit(t *testing.T, err error, wantCode int, output string) {
	t.Helper()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != wantCode {
		t.Fatalf("信号退出码无效: error=%v want=%d output=%s", err, wantCode, output)
	}
	if strings.Contains(output, "安全浏览器回归通过") {
		t.Fatalf("信号中断后仍发布成功: %s", output)
	}
}

func assertSecurityBuildChildStopped(t *testing.T, pidfile string) {
	t.Helper()
	payload, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(payload))
	if exec.Command("kill", "-0", pid).Run() == nil {
		t.Fatalf("build 子进程仍存活: pid=%s", pid)
	}
}

func assertSecurityImageCleanup(t *testing.T, state *fakeSecurityRuntime, tag, foreignTag string) {
	t.Helper()
	if _, err := os.Stat(securityFakeImageTagPath(state, tag)); !os.IsNotExist(err) {
		t.Fatalf("本轮安全浏览器镜像 tag 未回收: %s", tag)
	}
	if _, err := os.Stat(securityFakeImageTagPath(state, foreignTag)); err != nil {
		t.Fatalf("安全门禁误删外部镜像 tag: %v", err)
	}
	removed := extractSecuritySubcommandTargets(securityLogLines(t, state.dockerLog), "image", "rm")
	if len(removed) != 1 || removed[0] != tag {
		t.Fatalf("安全浏览器镜像回收目标无效: got=%v want=%s", removed, tag)
	}
}

func securityFakeImageTagPath(state *fakeSecurityRuntime, tag string) string {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(tag)))
	return filepath.Join(state.dockerState, "tags", key)
}

func forceSecurityRunID(t *testing.T, state *fakeSecurityRuntime, runID string) {
	t.Helper()
	securityWriteFile(t, filepath.Join(state.bin, "od"), "#!/bin/sh\nprintf '%s\\n' '"+runID+"'\n", 0o755)
}

func seedSecurityBrowserCollision(t *testing.T, state *fakeSecurityRuntime, root, runID, kind string) string {
	t.Helper()
	if kind == "container" {
		path := filepath.Join(state.dockerState, "containers", strings.Repeat("c", 64))
		securityWriteFile(t, path, "ai-gdm-security-fixture-"+runID+"|foreign\n", 0o600)
		return path
	}
	tree := securityGateGit(t, root, "write-tree")
	tag := "ai-gdm-security-browser:" + tree + "-" + runID
	digest := strings.Repeat("d", 64)
	imagePath := filepath.Join(state.dockerState, "images", digest)
	securityWriteFile(t, imagePath, "sha256:"+digest+"|foreign||\n", 0o600)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(tag)))
	tagPath := filepath.Join(state.dockerState, "tags", key)
	securityWriteFile(t, tagPath, "sha256:"+digest+"\n", 0o600)
	return tagPath
}

func TestSecurityMainRunsAllChecksFromOneSnapshot(t *testing.T) {
	shell := securityGateShell(t)
	root := newSecurityGateRepository(t)
	state := newFakeSecurityRuntime(t)
	output, err := runSecurityGate(shell, root, "validate-security.sh", state, nil)
	if err != nil || !strings.Contains(output, "P9.1 安全门禁通过") {
		t.Fatalf("安全主门禁未通过: error=%v output=%s", err, output)
	}
	mounts := securitySourceMounts(t, state.dockerLog)
	if len(mounts) < 3 || len(uniqueSecurityValues(mounts)) != 1 {
		t.Fatalf("Go/Node/Chromium 未使用同一源码快照: %v", mounts)
	}
	assertSecurityManagedLifecycle(t, state, 4)
	assertSecurityCIDFilesUnique(t, state.dockerLog, 4)
}

type fakeSecurityRuntime struct {
	bin, dockerLog, dockerState string
	createdLog, removedLog      string
	values                      map[string]string
}

func newFakeSecurityRuntime(t *testing.T) *fakeSecurityRuntime {
	t.Helper()
	root := t.TempDir()
	state := &fakeSecurityRuntime{
		bin: filepath.Join(root, "bin"), dockerLog: filepath.Join(root, "docker.log"),
		dockerState: filepath.Join(root, "docker-state"), createdLog: filepath.Join(root, "created.log"),
		removedLog: filepath.Join(root, "removed.log"), values: map[string]string{},
	}
	if err := os.MkdirAll(filepath.Join(state.dockerState, "containers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	securityWriteFile(t, filepath.Join(state.bin, "docker"), fakeSecurityDocker, 0o755)
	securityWriteFile(t, filepath.Join(state.bin, "curl"), "#!/bin/sh\nexit 0\n", 0o755)
	return state
}

func runSecurityGate(shell, root, script string, state *fakeSecurityRuntime,
	values map[string]string,
) (string, error) {
	command := newSecurityGateCommand(shell, root, script, state, values)
	payload, err := command.CombinedOutput()
	return string(payload), err
}

func newSecurityGateCommand(shell, root, script string, state *fakeSecurityRuntime,
	values map[string]string,
) *exec.Cmd {
	command := exec.Command(shell, filepath.Join(root, "scripts", script))
	command.Dir = root
	merged := map[string]string{
		"PATH":            state.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_LOG": state.dockerLog, "SECURITY_E2E_PORT": "18083",
		"FAKE_DOCKER_STATE": state.dockerState, "FAKE_CREATED_LOG": state.createdLog,
		"FAKE_REMOVED_LOG": state.removedLog,
	}
	for key, value := range state.values {
		merged[key] = value
	}
	for key, value := range values {
		merged[key] = value
	}
	command.Env = securityGateEnvironment(merged)
	return command
}

func securityGateEnvironment(values map[string]string) []string {
	result := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "SECURITY_") || strings.HasPrefix(entry, "AI_GDM_SECURITY_GATE_") {
			continue
		}
		result = append(result, entry)
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func securityGateShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("安全门禁行为测试在腾讯 Ubuntu 执行")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台未安装 POSIX sh")
	}
	if _, err = exec.LookPath("git"); err != nil {
		t.Skip("当前平台未安装 git")
	}
	return shell
}

func newSecurityGateRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{
		"security-gate.lib.sh", "validate-security-browser.sh", "validate-security.sh",
		"run-validation-container.sh",
	} {
		securityCopyFile(t, name, filepath.Join(root, "scripts", name), 0o755)
	}
	securityWriteFile(t, filepath.Join(root, "tests", "security-e2e", "Dockerfile"), "FROM scratch\n", 0o644)
	securityWriteFile(t, filepath.Join(root, "README.txt"), "security gate fixture\n", 0o644)
	securityGateGit(t, root, "init", "-q")
	securityGateGit(t, root, "config", "user.name", "security-gate-test")
	securityGateGit(t, root, "config", "user.email", "security@example.invalid")
	securityGateGit(t, root, "add", "-f", "-A", "--", ".")
	return root
}

func securityCopyFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	payload, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	securityWriteFile(t, destination, string(payload), mode)
}

func securityWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func securityGateGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	payload, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, payload)
	}
	return strings.TrimSpace(string(payload))
}

func securityArchiveBindings(t *testing.T, shell, root string) (string, string) {
	t.Helper()
	library := filepath.Join(root, "scripts", "security-gate.lib.sh")
	command := exec.Command(shell, "-c", `. "$1"; security_archive_tree "$2"; security_source_sha256 "$2"`,
		"security-bindings", library, root)
	command.Env = securityGateEnvironment(map[string]string{"AI_GDM_SECURITY_GATE_LIBRARY": "source"})
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

func securitySourceDigest(t *testing.T, shell, root string) string {
	t.Helper()
	library := filepath.Join(root, "scripts", "security-gate.lib.sh")
	command := exec.Command(shell, "-c", `. "$1"; security_source_sha256 "$2"`,
		"security-source", library, root)
	command.Env = securityGateEnvironment(map[string]string{"AI_GDM_SECURITY_GATE_LIBRARY": "source"})
	payload, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("计算源码摘要失败: %v: %s", err, payload)
	}
	return strings.TrimSpace(string(payload))
}

func assertDisjointSecurityDockerObjects(t *testing.T, state *fakeSecurityRuntime) {
	t.Helper()
	lines := securityLogLines(t, state.dockerLog)
	images := extractSecurityArguments(lines, "build", "-t")
	created := securityLogLines(t, state.createdLog)
	removed := securityLogLines(t, state.removedLog)
	removedImages := extractSecuritySubcommandTargets(lines, "image", "rm")
	if len(images) != 2 || len(uniqueSecurityValues(images)) != 2 {
		t.Fatalf("并发镜像 tag 未隔离: %v", images)
	}
	if len(created) != 4 || len(uniqueSecurityValues(created)) != 4 {
		t.Fatalf("并发容器名称未隔离: %v", created)
	}
	for _, name := range created {
		if !containsSecurityValue(removed, name) {
			t.Fatalf("容器 %s 未被精确回收: %v", name, removed)
		}
	}
	for _, name := range images {
		if !containsSecurityValue(removedImages, name) {
			t.Fatalf("镜像 %s 未被精确回收: %v", name, removedImages)
		}
	}
}

func extractSecuritySubcommandTargets(lines []string, command, subcommand string) []string {
	result := make([]string, 0)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == command && fields[1] == subcommand {
			result = append(result, fields[2])
		}
	}
	return result
}

func securitySourceMounts(t *testing.T, path string) []string {
	t.Helper()
	result := make([]string, 0)
	for _, line := range securityLogLines(t, path) {
		fields := strings.Fields(line)
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] == "-v" && strings.HasSuffix(fields[index+1], ":/src:ro") {
				result = append(result, strings.TrimSuffix(fields[index+1], ":/src:ro"))
			}
		}
	}
	return result
}

func securityLogLines(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result := make([]string, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		result = append(result, scanner.Text())
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func extractSecurityArguments(lines []string, command, option string) []string {
	result := make([]string, 0)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != command {
			continue
		}
		for index := 1; index+1 < len(fields); index++ {
			if fields[index] == option {
				result = append(result, fields[index+1])
				break
			}
		}
	}
	return result
}

func uniqueSecurityValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsSecurityValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func assertSecurityManagedLifecycle(t *testing.T, state *fakeSecurityRuntime, count int) []string {
	t.Helper()
	created := securityLogLines(t, state.createdLog)
	removed := securityLogLines(t, state.removedLog)
	want := append([]string(nil), created...)
	got := append([]string(nil), removed...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != count || strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Fatalf("主门禁容器 created/removed 不一致: created=%v removed=%v", created, removed)
	}
	if len(uniqueSecurityValues(created)) != count {
		t.Fatalf("主门禁容器名称重复: %v", created)
	}
	groups := make(map[string]map[string]bool)
	for _, name := range created {
		kind, token := securityManagedIdentity(t, name)
		if groups[token] == nil {
			groups[token] = make(map[string]bool)
		}
		groups[token][kind] = true
	}
	for token, kinds := range groups {
		if !validSecurityManagedGroup(kinds) {
			t.Fatalf("run-id %s 未精确绑定验证容器对: %v", token, kinds)
		}
	}
	return created
}

func validSecurityManagedGroup(kinds map[string]bool) bool {
	if len(kinds) != 2 {
		return false
	}
	return kinds["go"] && kinds["node"] || kinds["fixture"] && kinds["playwright"]
}

func securityManagedIdentity(t *testing.T, name string) (string, string) {
	t.Helper()
	prefix, kind := securityManagedPrefix(name)
	if prefix == "" {
		t.Fatalf("主门禁容器名称前缀无效: %s", name)
	}
	token := strings.TrimPrefix(name, prefix)
	if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		t.Fatalf("主门禁容器 run-id 不是 128 位十六进制: %s", name)
	}
	return kind, token
}

func securityManagedPrefix(name string) (string, string) {
	for _, item := range []struct{ prefix, kind string }{
		{"ai-gdm-security-go-", "go"}, {"ai-gdm-security-node-", "node"},
		{"ai-gdm-security-fixture-", "fixture"}, {"ai-gdm-security-playwright-", "playwright"},
	} {
		if strings.HasPrefix(name, item.prefix) {
			return item.prefix, item.kind
		}
	}
	return "", ""
}

func assertSecurityCIDFilesUnique(t *testing.T, path string, count int) {
	t.Helper()
	values := extractSecurityArguments(securityLogLines(t, path), "run", "--cidfile")
	if len(values) != count || len(uniqueSecurityValues(values)) != count {
		t.Fatalf("主门禁 cidfile 未按运行隔离: %v", values)
	}
}

const fakeSecurityDocker = `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
mkdir -p "$FAKE_DOCKER_STATE/containers" "$FAKE_DOCKER_STATE/images" "$FAKE_DOCKER_STATE/tags"

container_by_name() {
  for path in "$FAKE_DOCKER_STATE"/containers/*; do
    [ -f "$path" ] || continue
    IFS='|' read -r stored_name stored_label <"$path"
    [ "$stored_name" = "$1" ] && { basename "$path"; return 0; }
  done
  return 1
}

image_id() {
  case "$1" in
    sha256:*) printf '%s\n' "$1" ;;
    *) key=$(printf '%s' "$1" | sha256sum | awk '{print $1}'); cat "$FAKE_DOCKER_STATE/tags/$key" 2>/dev/null ;;
  esac
}

inspect_container() {
  format=$1
  target=$2
  case "$target" in *[!0-9a-f]*) target=$(container_by_name "$target") || return 1 ;; esac
  [ -f "$FAKE_DOCKER_STATE/containers/$target" ] || return 1
  IFS='|' read -r name label <"$FAKE_DOCKER_STATE/containers/$target"
  case "$format" in *State.Running*) printf '%s\n' true ;; *) printf '/%s|%s\n' "$name" "$label" ;; esac
}

inspect_image() {
  target=$1
  iid=$(image_id "$target") || return 1
  digest=${iid#sha256:}
  [ -f "$FAKE_DOCKER_STATE/images/$digest" ] || return 1
  cat "$FAKE_DOCKER_STATE/images/$digest"
}

command=$1
shift
case "$command" in
  build)
    [ -z "${FAKE_MUTATE_FILE:-}" ] || [ -e "$FAKE_MUTATE_MARKER" ] || {
      printf '%s\n' drift >>"$FAKE_MUTATE_FILE"; : >"$FAKE_MUTATE_MARKER";
    }
    tag= iidfile= run_label= tree_label= source_label=
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -t) shift; tag=${1:-} ;;
        --iidfile) shift; iidfile=${1:-} ;;
        --label)
          shift
          case "${1:-}" in
            ai.gdm.security.run=*) run_label=${1#*=} ;;
            ai.gdm.security.tree=*) tree_label=${1#*=} ;;
            ai.gdm.security.source=*) source_label=${1#*=} ;;
          esac
          ;;
      esac
      shift
    done
    digest=$(printf '%s|%s|%s|%s' "$tag" "$run_label" "$tree_label" "$source_label" | sha256sum | awk '{print $1}')
    iid=sha256:$digest
    printf '%s\n' "$iid|$run_label|$tree_label|$source_label" >"$FAKE_DOCKER_STATE/images/$digest"
    key=$(printf '%s' "$tag" | sha256sum | awk '{print $1}')
    printf '%s\n' "$iid" >"$FAKE_DOCKER_STATE/tags/$key"
    [ -z "$iidfile" ] || printf '%s\n' "$iid" >"$iidfile"
    if [ -n "${FAKE_BLOCK_BUILD_AFTER_IID_MARKER:-}" ]; then
      [ -z "${FAKE_BLOCK_BUILD_PIDFILE:-}" ] || printf '%s\n' "$$" >"$FAKE_BLOCK_BUILD_PIDFILE"
      : >"$FAKE_BLOCK_BUILD_AFTER_IID_MARKER"
      trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM
      while :; do sleep 1; done
    fi
    ;;
  run)
    name= cidfile= run_label=
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --name) shift; name=${1:-} ;;
        --cidfile) shift; cidfile=${1:-} ;;
        --label) shift; case "${1:-}" in ai.gdm.security.run=*) run_label=${1#*=} ;; esac ;;
      esac
      shift
    done
    container_by_name "$name" >/dev/null 2>&1 && exit 1
    cid=$(printf '%s|%s' "$name" "$run_label" | sha256sum | awk '{print $1}')
    [ -z "$cidfile" ] || printf '%s\n' "$cid" >"$cidfile"
    printf '%s|%s\n' "$name" "$run_label" >"$FAKE_DOCKER_STATE/containers/$cid"
    printf '%s\n' "$name" >>"$FAKE_CREATED_LOG"
    if [ -n "${FAKE_BLOCK_CONTAINER_PART:-}" ]; then
      case "$name" in *"$FAKE_BLOCK_CONTAINER_PART"*)
        : >"$FAKE_BLOCK_MARKER"; trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM
        while :; do sleep 1; done ;;
      esac
    fi
    ;;
  inspect)
    format= target=
    while [ "$#" -gt 0 ]; do case "$1" in -f) shift; format=${1:-} ;; *) target=$1 ;; esac; shift; done
    inspect_container "$format" "$target" || exit $?
    ;;
  container)
    [ "${1:-}" = inspect ] || exit 1
    inspect_container "" "${2:-}" || exit $?
    ;;
  image)
    sub=${1:-}; shift || true
    case "$sub" in
      inspect)
        format= target=
        while [ "$#" -gt 0 ]; do case "$1" in -f) shift; format=${1:-} ;; *) target=$1 ;; esac; shift; done
        inspect_image "$target" || exit $?
        ;;
      rm)
        target=${1:-}
        key=$(printf '%s' "$target" | sha256sum | awk '{print $1}')
        [ -f "$FAKE_DOCKER_STATE/tags/$key" ] || exit 0
        rm -f "$FAKE_DOCKER_STATE/tags/$key"
        ;;
    esac
    ;;
  rm)
    target=
    for value in "$@"; do target=$value; done
    [ -f "$FAKE_DOCKER_STATE/containers/$target" ] || exit 0
    IFS='|' read -r name label <"$FAKE_DOCKER_STATE/containers/$target"
    printf '%s\n' "$name" >>"$FAKE_REMOVED_LOG"
    rm -f "$FAKE_DOCKER_STATE/containers/$target"
    ;;
  logs) exit 0 ;;
esac
exit 0
`
