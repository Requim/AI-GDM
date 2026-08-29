package scripts_test

import (
	"archive/tar"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var assessmentContainerPrefixes = []string{
	"ai-gdm-assessment-fixture-",
	"ai-gdm-assessment-playwright-",
	"ai-gdm-assessment-audit-",
}

func TestValidateAssessmentBrowserScriptIsRunBound(t *testing.T) {
	payload, err := os.ReadFile("validate-assessment-browser.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(payload), "\r\n", "\n")
	for _, fragment := range []string{
		"REQUESTED_PORT=${ASSESSMENT_E2E_PORT:-0}",
		"SOURCE_MANIFEST_REL=tests/assessment-e2e/source-files.txt",
		`SOURCE_SHA256=$(calculate_source_sha256 "$SNAPSHOT_DIR")`,
		"SOURCE_MODE=git",
		"SOURCE_MODE=archive",
		"TREE_SHA=$(archive_tree_sha)",
		"validate_archive_inputs",
		"FIXTURE_TOKEN=$(generate_fixture_token)",
		"RUN_ID=$FIXTURE_TOKEN",
		`git -c "safe.directory=$ROOT" write-tree`,
		`--git-dir="$work/repository/.git"`,
		`归档模式必须传入 ASSESSMENT_E2E_SOURCE_SHA256`,
		`ASSESSMENT_E2E_SOURCE_SHA256 与实际受审计源码不一致`,
		`ASSESSMENT_E2E_TREE_SHA 与实际 Git tree 不一致`,
		`verify_git_source "$TREE_SNAPSHOT_DIR"`,
		`copy_audited_source "$TREE_SNAPSHOT_DIR" "$SNAPSHOT_DIR"`,
		`chmod -R a-w "$WORK_DIR"`,
		"verify_source_stability",
		"ai-gdm-assessment-browser:$TREE_SHA",
		`GIT_INDEX_FILE="$work/index"`,
		`BROWSER_NAME="ai-gdm-assessment-playwright-$RUN_ID"`,
		`AUDIT_NAME="ai-gdm-assessment-audit-$RUN_ID"`,
		"E2E_ADDR=127.0.0.1:$REQUESTED_PORT",
		"E2E_FIXTURE_TOKEN=$FIXTURE_TOKEN",
		"E2E_TREE_SHA=$TREE_SHA",
		"EXPECTED_HEALTH=\"ok:$FIXTURE_TOKEN:$TREE_SHA\"",
		"PLAYWRIGHT_JSON_OUTPUT_FILE=/runtime/playwright-results.json",
		"npx playwright test --reporter=line,json",
		"node /audit/validate-results.mjs",
		`-v "$SNAPSHOT_DIR:/src:ro"`,
		`-v "$SNAPSHOT_DIR/tests/assessment-e2e:/audit:ro"`,
		"source_sha256=$SOURCE_SHA256",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("assessment 浏览器脚本缺少 %q", fragment)
		}
	}
	if strings.Contains(script, "ASSESSMENT_E2E_PORT:-18082") {
		t.Fatal("assessment 浏览器脚本仍默认复用固定端口")
	}
	if strings.Contains(script, "scenarios=") {
		t.Fatal("assessment 浏览器脚本不应自行声明场景数")
	}
	if strings.Contains(script, `-v "$ROOT:/src:ro"`) {
		t.Fatal("assessment fixture 不得挂载可变工作树")
	}
	if strings.Contains(script, "RUN_ID=") && strings.Contains(script, "RUN_ID=$(printf") {
		t.Fatal("assessment 容器名称不得使用 PID 或截断随机值兜底")
	}
}

func TestAssessmentBrowserRejectsChangedSourceWithOldTreeSHA(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑 tree 行为门禁")
	}
	root := newAssessmentGitGate(t, gitPath)
	scriptPath := filepath.Join(root, "scripts", "validate-assessment-browser.sh")
	oldTree := strings.TrimSpace(runAssessmentGit(t, gitPath, root, "write-tree"))
	if err = os.WriteFile(filepath.Join(root, "source", "source.go"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAssessmentGit(t, gitPath, root, "add", "--", "source/source.go")
	command := exec.Command(shell, filepath.ToSlash(scriptPath))
	command.Dir = root
	command.Env = assessmentShellEnvironment(t, map[string]string{"ASSESSMENT_E2E_TREE_SHA": oldTree})
	output, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(output), "ASSESSMENT_E2E_TREE_SHA 与实际 Git tree 不一致") {
		t.Fatalf("旧 tree SHA 未被拒绝: error=%v output=%s", runErr, output)
	}
}

func TestAssessmentBrowserGitRejectsChangedSourceWithOldDigest(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑 source digest 门禁")
	}
	root := newAssessmentGitGate(t, gitPath)
	oldDigest := assessmentSourceDigest(t, root)
	writeAssessmentFile(t, filepath.Join(root, "source", "source.go"), "package changed\n", 0o644)
	runAssessmentGit(t, gitPath, root, "add", "--", "source/source.go")
	assertAssessmentGateFailure(t, shell, root,
		map[string]string{"ASSESSMENT_E2E_SOURCE_SHA256": oldDigest}, "实际受审计源码不一致")
}

func TestAssessmentBrowserRejectsIgnoredSourceOutsideTree(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑 ignored source 门禁")
	}
	for _, mode := range []string{"info-exclude", "global-ignore"} {
		t.Run(mode, func(t *testing.T) {
			root := newAssessmentGitGate(t, gitPath)
			hidden, environment := configureAssessmentIgnore(t, gitPath, root, mode)
			writeAssessmentFile(t, hidden, "package source\n", 0o644)
			assertAssessmentGitIgnored(t, gitPath, root, hidden, environment)
			assertAssessmentGateFailure(t, shell, root, environment, "tree 外源码")
		})
	}
}

func TestAssessmentBrowserRejectsGitHiddenIndexFlags(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑 index flag 门禁")
	}
	for name, flag := range map[string]string{
		"assume-unchanged": "--assume-unchanged",
		"skip-worktree":    "--skip-worktree",
	} {
		t.Run(name, func(t *testing.T) {
			root := newAssessmentGitGate(t, gitPath)
			runAssessmentGit(t, gitPath, root, "update-index", flag, "--", "source/source.go")
			assertAssessmentGateFailure(t, shell, root, nil, "Git 隐藏标记")
		})
	}
}

func TestAssessmentBrowserRejectsRuntimeSourceDrift(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑运行期漂移门禁")
	}
	root := newAssessmentGitGate(t, gitPath)
	environment, state := assessmentFakeRuntime(t, root, map[string]string{
		"FAKE_DRIFT_FILE": "source/source.go",
	})
	output, runErr := runAssessmentGate(t, shell, root, environment)
	if runErr == nil || !strings.Contains(output, "与 Git tree 不一致") {
		t.Fatalf("运行中源码漂移未被拒绝: error=%v output=%s", runErr, output)
	}
	if strings.Contains(output, "Chromium 回归通过") {
		t.Fatalf("运行中漂移后仍宣称旧 tree 通过: %s", output)
	}
	assertAssessmentContainersCleaned(t, state)
}

func TestAssessmentBrowserCleansNamedContainersOnFailure(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑容器回收门禁")
	}
	root := newAssessmentGitGate(t, gitPath)
	environment, state := assessmentFakeRuntime(t, root, map[string]string{"FAKE_BROWSER_STATUS": "7"})
	output, runErr := runAssessmentGate(t, shell, root, environment)
	if runErr == nil || !strings.Contains(output, "playwright=7") {
		t.Fatalf("浏览器失败未保留退出码: error=%v output=%s", runErr, output)
	}
	assertAssessmentContainersCleaned(t, state)
}

func TestAssessmentBrowserCleansNamedContainersOnInterrupt(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑中断回收门禁")
	}
	root := newAssessmentGitGate(t, gitPath)
	environment, state := assessmentFakeRuntime(t, root, map[string]string{"FAKE_BROWSER_INTERRUPT": "1"})
	output, runErr := runAssessmentGate(t, shell, root, environment)
	if runErr == nil || strings.Contains(output, "Chromium 回归通过") {
		t.Fatalf("浏览器中断未使门禁失败: error=%v output=%s", runErr, output)
	}
	assertAssessmentCreatedContainersRemoved(t, state)
}

func TestAssessmentContainerLifecycleRejectsWrongCleanupName(t *testing.T) {
	state := t.TempDir()
	created := assessmentLifecycleFixtureNames("a")
	writeAssessmentFile(t, filepath.Join(state, "docker-calls"), assessmentRunRecords(created), 0o644)
	removed := append([]string(nil), created...)
	removed[2] += "x"
	writeAssessmentFile(t, filepath.Join(state, "removed"), strings.Join(removed, "\n")+"\n", 0o644)
	if _, err := assessmentContainerLifecycle(state); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("name+x 错清理未被识别: %v", err)
	}
}

func TestAssessmentBrowserRejectsInvalidRandomTokenBeforeDocker(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑随机源门禁")
	}
	cases := []struct {
		mode, message string
	}{
		{mode: "od-failure", message: "随机源读取失败"},
		{mode: "urandom-failure", message: "随机源读取失败"},
		{mode: "empty", message: "必须是 32 位小写十六进制"},
		{mode: "short", message: "必须是 32 位小写十六进制"},
		{mode: "nonhex", message: "必须是 32 位小写十六进制"},
	}
	for _, item := range cases {
		t.Run(item.mode, func(t *testing.T) {
			root := newAssessmentGitGate(t, gitPath)
			environment, state := assessmentTokenFailureEnvironment(t, item.mode)
			output, runErr := runAssessmentGate(t, shell, root, environment)
			if runErr == nil || !strings.Contains(output, item.message) {
				t.Fatalf("随机令牌 %s 未拒绝: error=%v output=%s", item.mode, runErr, output)
			}
			assertAssessmentDockerNotCalled(t, state)
		})
	}
}

func TestAssessmentBrowserConcurrentRunsUseDistinctRandomNames(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑并发命名门禁")
	}
	root := newAssessmentGitGate(t, gitPath)
	environmentA, stateA := assessmentFakeRuntime(t, root, nil)
	environmentB, stateB := assessmentFakeRuntime(t, root, nil)
	results := make(chan assessmentGateResult, 2)
	go runAssessmentGateAsync(shell, root, environmentA, results)
	go runAssessmentGateAsync(shell, root, environmentB, results)
	for range 2 {
		result := <-results
		if result.err != nil || !strings.Contains(result.output, "Chromium 回归通过") {
			t.Fatalf("并发门禁未通过: error=%v output=%s", result.err, result.output)
		}
	}
	nameA := assessmentNamedContainer(t, stateA, "ai-gdm-assessment-playwright-")
	nameB := assessmentNamedContainer(t, stateB, "ai-gdm-assessment-playwright-")
	assertAssessmentRandomName(t, nameA, "ai-gdm-assessment-playwright-")
	assertAssessmentRandomName(t, nameB, "ai-gdm-assessment-playwright-")
	if nameA == nameB {
		t.Fatalf("两个并发门禁复用了容器名称: %s", nameA)
	}
	createdA := assertAssessmentContainersCleaned(t, stateA)
	createdB := assertAssessmentContainersCleaned(t, stateB)
	assertAssessmentContainerGroupsDisjoint(t, createdA, createdB)
}

func TestAssessmentBrowserArchiveRejectsChangedSourceWithOldDigest(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑归档摘要门禁")
	}
	root := newAssessmentArchive(t)
	oldDigest := assessmentSourceDigest(t, root)
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := assessmentArchiveTree(t, gitPath, root)
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-assessment-browser.sh")))
	command.Dir = root
	command.Env = assessmentShellEnvironment(t, map[string]string{
		"ASSESSMENT_E2E_SOURCE_SHA256": oldDigest,
		"ASSESSMENT_E2E_TREE_SHA":      tree,
	})
	output, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(output),
		"ASSESSMENT_E2E_SOURCE_SHA256 与实际受审计源码不一致") {
		t.Fatalf("归档源码改动未拒绝旧摘要: error=%v output=%s", runErr, output)
	}
}

func TestAssessmentBrowserArchiveRequiresBoundTreeBeforeDocker(t *testing.T) {
	shell := requireShell(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑归档 tree 门禁")
	}
	cases := []struct {
		name, tree, message string
		includeTree         bool
	}{
		{name: "missing", message: "必须传入 ASSESSMENT_E2E_TREE_SHA"},
		{name: "empty", tree: "", message: "必须传入 ASSESSMENT_E2E_TREE_SHA", includeTree: true},
		{name: "short", tree: "abc", message: "40 位小写十六进制", includeTree: true},
		{name: "uppercase", tree: strings.Repeat("A", 40), message: "40 位小写十六进制", includeTree: true},
		{name: "mismatch", tree: strings.Repeat("0", 40), message: "与实际 Git tree 不一致", includeTree: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			root := newAssessmentArchive(t)
			values := map[string]string{"ASSESSMENT_E2E_SOURCE_SHA256": assessmentSourceDigest(t, root)}
			if item.includeTree {
				values["ASSESSMENT_E2E_TREE_SHA"] = item.tree
			}
			environment, state := assessmentArchivePreflightEnvironment(t, values)
			output, runErr := runAssessmentGate(t, shell, root, environment)
			if runErr == nil || !strings.Contains(output, item.message) {
				t.Fatalf("归档 tree %s 未拒绝: error=%v output=%s", item.name, runErr, output)
			}
			assertAssessmentDockerNotCalled(t, state)
		})
	}
}

func TestAssessmentBrowserArchiveStillRequiresSourceDigest(t *testing.T) {
	shell := requireShell(t)
	root := newAssessmentArchive(t)
	environment, state := assessmentArchivePreflightEnvironment(t, map[string]string{
		"ASSESSMENT_E2E_TREE_SHA": strings.Repeat("0", 40),
	})
	output, runErr := runAssessmentGate(t, shell, root, environment)
	if runErr == nil || !strings.Contains(output, "必须传入 ASSESSMENT_E2E_SOURCE_SHA256") {
		t.Fatalf("归档 source SHA 未保持强制: error=%v output=%s", runErr, output)
	}
	assertAssessmentDockerNotCalled(t, state)
}

func TestAssessmentBrowserArchiveVerifiesProvidedTreeSHA(t *testing.T) {
	shell := requireShell(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑归档 tree 行为门禁")
	}
	root := newAssessmentArchive(t)
	digest := assessmentSourceDigest(t, root)
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-assessment-browser.sh")))
	command.Dir = root
	command.Env = assessmentShellEnvironment(t, map[string]string{
		"ASSESSMENT_E2E_SOURCE_SHA256": digest,
		"ASSESSMENT_E2E_TREE_SHA":      strings.Repeat("0", 40),
	})
	output, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(output), "ASSESSMENT_E2E_TREE_SHA 与实际 Git tree 不一致") {
		t.Fatalf("归档模式把 tree SHA 当成未验证标签: error=%v output=%s", runErr, output)
	}
}

func TestAssessmentBrowserGitArchiveAcceptsRecomputedIntegrity(t *testing.T) {
	shell := requireShell(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("当前平台未安装 git，需在远端补跑 git archive 行为门禁")
	}
	source := newAssessmentArchive(t)
	runAssessmentGit(t, gitPath, source, "init", "-q")
	runAssessmentGit(t, gitPath, source, "config", "core.autocrlf", "false")
	runAssessmentGit(t, gitPath, source, "add", "-f", "-A", "--", ".")
	tree := strings.TrimSpace(runAssessmentGit(t, gitPath, source, "write-tree"))
	archiveRoot := extractAssessmentGitArchive(t, gitPath, source, tree)
	if _, statErr := os.Stat(filepath.Join(archiveRoot, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("git archive 解压目录不应包含 .git: %v", statErr)
	}
	digest := assessmentSourceDigest(t, archiveRoot)
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(archiveRoot, "scripts", "validate-assessment-browser.sh")))
	command.Dir = archiveRoot
	command.Env = assessmentShellEnvironment(t, map[string]string{
		"ASSESSMENT_E2E_SOURCE_SHA256": digest,
		"ASSESSMENT_E2E_TREE_SHA":      tree,
		"ASSESSMENT_E2E_PORT":          "invalid",
	})
	output, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(output), "ASSESSMENT_E2E_PORT 必须是") {
		t.Fatalf("git archive 未通过完整性预检: error=%v output=%s", runErr, output)
	}
}

func TestAssessmentBrowserSourceManifestCoversAuditedRuntime(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "tests", "assessment-e2e", "source-files.txt"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.ReplaceAll(string(payload), "\r\n", "\n")
	for _, entry := range []string{
		"go.mod", "go.sum", "internal/", "scripts/", "tests/assessment-e2e/Dockerfile",
		"tests/assessment-e2e/expected-tests.json", "tests/assessment-e2e/fixture/",
		"tests/assessment-e2e/package-lock.json", "tests/assessment-e2e/package.json",
		"tests/assessment-e2e/playwright.config.js", "tests/assessment-e2e/source-files.txt",
		"tests/assessment-e2e/specs/", "tests/assessment-e2e/validate-results.mjs",
	} {
		if !strings.Contains("\n"+manifest, "\n"+entry+"\n") {
			t.Fatalf("assessment 源码清单缺少 %q", entry)
		}
	}
	for _, excluded := range []string{"node_modules", "test-results", "playwright-report"} {
		if strings.Contains(manifest, excluded) {
			t.Fatalf("assessment 源码清单包含运行产物 %q", excluded)
		}
	}
}

func TestAssessmentResultManifestIsAudited(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "tests", "assessment-e2e", "expected-tests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version int `json:"version"`
		Tests   []struct {
			File    string `json:"file"`
			Title   string `json:"title"`
			Project string `json:"project"`
		} `json:"tests"`
	}
	if err = json.Unmarshal(payload, &manifest); err != nil || manifest.Version != 1 || len(manifest.Tests) == 0 {
		t.Fatalf("assessment manifest 无效: version=%d tests=%d error=%v", manifest.Version, len(manifest.Tests), err)
	}
	seen := make(map[string]struct{}, len(manifest.Tests))
	for _, item := range manifest.Tests {
		key := item.Project + "\x00" + item.File + "\x00" + item.Title
		if item.File == "" || item.Title == "" {
			t.Fatal("assessment manifest 包含空身份")
		}
		if _, exists := seen[key]; exists {
			t.Fatalf("assessment manifest 包含重复身份: %q", key)
		}
		seen[key] = struct{}{}
	}
}

func TestAssessmentResultAuditAcceptsExactPassedManifest(t *testing.T) {
	node := assessmentNode(t)
	manifest := assessmentManifest("one", "two")
	report := assessmentReport([]any{assessmentSpec("one", "passed"), assessmentSpec("two", "passed")},
		map[string]any{"expected": 2, "unexpected": 0, "flaky": 0, "skipped": 0})
	output, err := runAssessmentAudit(t, node, manifest, report)
	if err != nil || !strings.Contains(output, "passed=2 failed=0 skipped=0") {
		t.Fatalf("精确通过报告未通过审计: error=%v output=%s", err, output)
	}
}

func TestAssessmentResultAuditRejectsSkipFixmeAndMissing(t *testing.T) {
	node := assessmentNode(t)
	cases := []struct {
		name     string
		manifest any
		report   any
		message  string
	}{
		{name: "skip", manifest: assessmentManifest("one"),
			report: assessmentReport([]any{assessmentSpec("one", "skip")},
				map[string]any{"expected": 0, "unexpected": 0, "flaky": 0, "skipped": 1}), message: "skip/fixme"},
		{name: "fixme", manifest: assessmentManifest("one"),
			report: assessmentReport([]any{assessmentSpec("one", "fixme")},
				map[string]any{"expected": 0, "unexpected": 0, "flaky": 0, "skipped": 1}), message: "skip/fixme"},
		{name: "missing", manifest: assessmentManifest("one", "two"),
			report: assessmentReport([]any{assessmentSpec("one", "passed")},
				map[string]any{"expected": 1, "unexpected": 0, "flaky": 0, "skipped": 0}), message: "缺少受审计场景"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			output, err := runAssessmentAudit(t, node, item.manifest, item.report)
			if err == nil || !strings.Contains(output, item.message) {
				t.Fatalf("%s 未被拒绝: error=%v output=%s", item.name, err, output)
			}
		})
	}
}

func TestAssessmentResultAuditRejectsExtraDuplicateRetryAndInvalidSummary(t *testing.T) {
	node := assessmentNode(t)
	cases := assessmentInvalidReportCases()
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			output, err := runAssessmentAudit(t, node, item.manifest, item.report)
			if err == nil || !strings.Contains(output, item.message) {
				t.Fatalf("%s 未被拒绝: error=%v output=%s", name, err, output)
			}
		})
	}
}

type assessmentInvalidReportCase struct {
	manifest any
	report   any
	message  string
}

func assessmentInvalidReportCases() map[string]assessmentInvalidReportCase {
	passedStats := map[string]any{"expected": 1, "unexpected": 0, "flaky": 0, "skipped": 0}
	topLevelError := assessmentReport([]any{assessmentSpec("one", "passed")}, passedStats).(map[string]any)
	topLevelError["errors"] = []any{map[string]any{"message": "boom"}}
	return map[string]assessmentInvalidReportCase{
		"extra": {manifest: assessmentManifest("one"),
			report: assessmentReport([]any{assessmentSpec("one", "passed"), assessmentSpec("two", "passed")},
				map[string]any{"expected": 2, "unexpected": 0, "flaky": 0, "skipped": 0}),
			message: "出现未审计场景"},
		"duplicate": {manifest: assessmentManifest("one"),
			report:  assessmentReport([]any{assessmentSpec("one", "passed"), assessmentSpec("one", "passed")}, passedStats),
			message: "包含重复测试身份"},
		"retry": {manifest: assessmentManifest("one"),
			report:  assessmentReport([]any{assessmentRetriedSpec("one")}, passedStats),
			message: "没有且仅有一次 passed 结果"},
		"top-level-error": {manifest: assessmentManifest("one"), report: topLevelError,
			message: "Playwright 报告包含顶层错误"},
		"stats-drift": {manifest: assessmentManifest("one"),
			report: assessmentReport([]any{assessmentSpec("one", "passed")},
				map[string]any{"expected": 1, "unexpected": 0, "flaky": 1, "skipped": 0}),
			message: "统计不匹配"},
	}
}

func TestAssessmentBrowserDependenciesAreLocked(t *testing.T) {
	root := filepath.Join("..", "tests", "assessment-e2e")
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(dockerfile)
	for _, fragment := range []string{
		"ARG CHROMIUM_VERSION=151.0.7922.173-1~deb12u1",
		"chromium=${CHROMIUM_VERSION}",
		"COPY package.json package-lock.json ./",
		"RUN npm ci --ignore-scripts",
	} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("assessment Dockerfile 缺少 %q", fragment)
		}
	}
	assertAssessmentLock(t, filepath.Join(root, "package-lock.json"))
}

func assertAssessmentLock(t *testing.T, path string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(payload, &lock); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node_modules/@playwright/test", "node_modules/playwright", "node_modules/playwright-core"} {
		if lock.Packages[name].Version != "1.55.0" {
			t.Fatalf("%s version=%q", name, lock.Packages[name].Version)
		}
	}
}

func assessmentNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("当前平台未安装 node，需在浏览器镜像补跑结果审计行为门禁")
	}
	return node
}

func runAssessmentGit(t *testing.T, gitPath, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command(gitPath, arguments...)
	command.Dir = directory
	payload, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v output=%s", strings.Join(arguments, " "), err, payload)
	}
	return string(payload)
}

func assessmentArchiveTree(t *testing.T, gitPath, root string) string {
	t.Helper()
	repository := t.TempDir()
	runAssessmentGit(t, gitPath, repository, "init", "-q")
	gitDirectory := filepath.Join(repository, ".git")
	runAssessmentGit(t, gitPath, repository,
		"--git-dir="+gitDirectory, "--work-tree="+root, "add", "-f", "-A", "--", ".")
	return strings.TrimSpace(runAssessmentGit(t, gitPath, repository,
		"--git-dir="+gitDirectory, "write-tree"))
}

func newAssessmentGitGate(t *testing.T, gitPath string) string {
	t.Helper()
	root := t.TempDir()
	payload, err := os.ReadFile("validate-assessment-browser.sh")
	if err != nil {
		t.Fatal(err)
	}
	writeAssessmentBytes(t, filepath.Join(root, "scripts", "validate-assessment-browser.sh"), payload, 0o755)
	writeAssessmentFile(t, filepath.Join(root, "scripts", "run-validation-container.sh"), fakeAssessmentRunner, 0o755)
	writeAssessmentFile(t, filepath.Join(root, "source", "source.go"), "package source\n", 0o644)
	writeAssessmentFile(t, filepath.Join(root, "tests", "assessment-e2e", "Dockerfile"), "FROM scratch\n", 0o644)
	writeAssessmentFile(t, filepath.Join(root, "tests", "assessment-e2e", "expected-tests.json"), "{}\n", 0o644)
	writeAssessmentFile(t, filepath.Join(root, "tests", "assessment-e2e", "validate-results.mjs"), "// audited\n", 0o644)
	writeAssessmentFile(t, filepath.Join(root, "tests", "assessment-e2e", "source-files.txt"), assessmentGateManifest, 0o644)
	runAssessmentGit(t, gitPath, root, "init", "-q")
	runAssessmentGit(t, gitPath, root, "config", "core.autocrlf", "false")
	runAssessmentGit(t, gitPath, root, "add", "-f", "-A", "--", ".")
	return root
}

func configureAssessmentIgnore(t *testing.T, gitPath, root, mode string) (string, map[string]string) {
	t.Helper()
	if mode == "info-exclude" {
		path := filepath.Join(root, ".git", "info", "exclude")
		writeAssessmentFile(t, path, "source/hidden.generated.go\n", 0o644)
		return filepath.Join(root, "source", "hidden.generated.go"), nil
	}
	configRoot := t.TempDir()
	ignorePath := filepath.Join(configRoot, "global.ignore")
	configPath := filepath.Join(configRoot, "global.gitconfig")
	writeAssessmentFile(t, ignorePath, "*.global.go\n", 0o644)
	runAssessmentGit(t, gitPath, root, "config", "--file", configPath,
		"core.excludesFile", filepath.ToSlash(ignorePath))
	return filepath.Join(root, "source", "hidden.global.go"), map[string]string{"GIT_CONFIG_GLOBAL": configPath}
}

func assessmentFakeRuntime(t *testing.T, root string, overrides map[string]string) ([]string, string) {
	t.Helper()
	state := t.TempDir()
	bin := t.TempDir()
	writeAssessmentFile(t, filepath.Join(bin, "docker"), fakeAssessmentDocker, 0o755)
	writeAssessmentFile(t, filepath.Join(bin, "curl"), fakeAssessmentCurl, 0o755)
	writeAssessmentFile(t, filepath.Join(bin, "chmod"), fakeAssessmentChmod, 0o755)
	writeAssessmentFile(t, filepath.Join(bin, "od"), fakeAssessmentSuccessOD(t), 0o755)
	values := map[string]string{
		"FAKE_STATE_DIR": state,
		"PATH":           bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	for key, value := range overrides {
		values[key] = value
	}
	return assessmentEnvironment(values), state
}

func assessmentArchivePreflightEnvironment(
	t *testing.T, overrides map[string]string,
) ([]string, string) {
	t.Helper()
	state := t.TempDir()
	bin := t.TempDir()
	writeAssessmentFile(t, filepath.Join(bin, "docker"), fakeAssessmentDocker, 0o755)
	writeAssessmentFile(t, filepath.Join(bin, "chmod"), fakeAssessmentChmod, 0o755)
	values := map[string]string{
		"FAKE_STATE_DIR": state,
		"PATH":           bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	for key, value := range overrides {
		values[key] = value
	}
	return assessmentEnvironment(values), state
}

func assessmentTokenFailureEnvironment(t *testing.T, mode string) ([]string, string) {
	t.Helper()
	state := t.TempDir()
	bin := t.TempDir()
	writeAssessmentFile(t, filepath.Join(bin, "docker"), fakeAssessmentDocker, 0o755)
	writeAssessmentFile(t, filepath.Join(bin, "chmod"), fakeAssessmentChmod, 0o755)
	writeAssessmentFile(t, filepath.Join(bin, "od"), fakeAssessmentOD, 0o755)
	return assessmentEnvironment(map[string]string{
		"FAKE_OD_MODE":   mode,
		"FAKE_STATE_DIR": state,
		"PATH":           bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}), state
}

func runAssessmentGate(t *testing.T, shell, root string, environment []string) (string, error) {
	t.Helper()
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-assessment-browser.sh")))
	command.Dir = root
	command.Env = environment
	payload, err := command.CombinedOutput()
	return string(payload), err
}

type assessmentGateResult struct {
	output string
	err    error
}

func runAssessmentGateAsync(
	shell, root string, environment []string, results chan<- assessmentGateResult,
) {
	command := exec.Command(shell, filepath.ToSlash(filepath.Join(root, "scripts", "validate-assessment-browser.sh")))
	command.Dir = root
	command.Env = environment
	payload, err := command.CombinedOutput()
	results <- assessmentGateResult{output: string(payload), err: err}
}

func assertAssessmentGateFailure(t *testing.T, shell, root string, overrides map[string]string, message string) {
	t.Helper()
	output, err := runAssessmentGate(t, shell, root, assessmentShellEnvironment(t, overrides))
	if err == nil || !strings.Contains(output, message) {
		t.Fatalf("门禁未拒绝 %s: error=%v output=%s", message, err, output)
	}
}

func assessmentShellEnvironment(t *testing.T, overrides map[string]string) []string {
	t.Helper()
	bin := t.TempDir()
	writeAssessmentFile(t, filepath.Join(bin, "chmod"), fakeAssessmentChmod, 0o755)
	values := map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	for key, value := range overrides {
		values[key] = value
	}
	return assessmentEnvironment(values)
}

func assertAssessmentGitIgnored(
	t *testing.T, gitPath, root, path string, overrides map[string]string,
) {
	t.Helper()
	command := exec.Command(gitPath, "check-ignore", "--", filepath.ToSlash(path))
	command.Dir = root
	command.Env = assessmentEnvironment(overrides)
	if payload, err := command.CombinedOutput(); err != nil {
		t.Fatalf("测试文件未被 Git ignore 隐藏: error=%v output=%s", err, payload)
	}
}

func assertAssessmentContainersCleaned(t *testing.T, state string) []string {
	t.Helper()
	created, err := assessmentContainerLifecycle(state)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func assertAssessmentCreatedContainersRemoved(t *testing.T, state string) {
	t.Helper()
	created, err := assessmentDockerRunNames(state)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := assessmentRemovedNames(state)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), created...)
	got := append([]string(nil), removed...)
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, "\x00") != strings.Join(got, "\x00") {
		t.Fatalf("中断容器 created/removed 集合不一致: created=%v removed=%v", created, removed)
	}
}

func assessmentContainerLifecycle(state string) ([]string, error) {
	created, err := assessmentDockerRunNames(state)
	if err != nil {
		return nil, err
	}
	removed, err := assessmentRemovedNames(state)
	if err != nil {
		return nil, err
	}
	if err = validateAssessmentContainerNames("created", created); err != nil {
		return nil, err
	}
	if err = validateAssessmentContainerNames("removed", removed); err != nil {
		return nil, err
	}
	want := append([]string(nil), created...)
	got := append([]string(nil), removed...)
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, "\x00") != strings.Join(got, "\x00") {
		return nil, fmt.Errorf("容器 created/removed 集合不一致: created=%v removed=%v", created, removed)
	}
	return created, nil
}

func assessmentDockerRunNames(state string) ([]string, error) {
	payload, err := os.ReadFile(filepath.Join(state, "docker-calls"))
	if err != nil {
		return nil, fmt.Errorf("读取 fake docker 调用: %w", err)
	}
	names := make([]string, 0, 3)
	for _, line := range strings.Split(string(payload), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "run" {
			continue
		}
		name, found := assessmentDockerName(fields[1:])
		if !found {
			return nil, fmt.Errorf("fake docker run 缺少 --name: %s", line)
		}
		names = append(names, name)
	}
	return names, nil
}

func assessmentDockerName(arguments []string) (string, bool) {
	for index, argument := range arguments {
		if argument == "--name" && index+1 < len(arguments) {
			return arguments[index+1], true
		}
		if strings.HasPrefix(argument, "--name=") && len(argument) > len("--name=") {
			return strings.TrimPrefix(argument, "--name="), true
		}
	}
	return "", false
}

func assessmentRemovedNames(state string) ([]string, error) {
	payload, err := os.ReadFile(filepath.Join(state, "removed"))
	if err != nil {
		return nil, fmt.Errorf("读取 fake docker 回收记录: %w", err)
	}
	return strings.Fields(string(payload)), nil
}

func validateAssessmentContainerNames(label string, names []string) error {
	if len(names) != len(assessmentContainerPrefixes) {
		return fmt.Errorf("容器 %s 数量不是 %d: %v", label, len(assessmentContainerPrefixes), names)
	}
	seen := make(map[string]struct{}, len(names))
	prefixCounts := make(map[string]int, len(assessmentContainerPrefixes))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			return fmt.Errorf("容器 %s 名称重复: %s", label, name)
		}
		seen[name] = struct{}{}
		for _, prefix := range assessmentContainerPrefixes {
			if strings.HasPrefix(name, prefix) {
				prefixCounts[prefix]++
			}
		}
	}
	for _, prefix := range assessmentContainerPrefixes {
		if prefixCounts[prefix] != 1 {
			return fmt.Errorf("容器 %s 前缀 %s 数量不是 1: %v", label, prefix, names)
		}
	}
	return nil
}

func assertAssessmentDockerNotCalled(t *testing.T, state string) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(state, "docker-calls"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("预检失败后仍调用 Docker: %s", payload)
}

func assessmentNamedContainer(t *testing.T, state, prefix string) string {
	t.Helper()
	names, err := assessmentDockerRunNames(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			return name
		}
	}
	t.Fatalf("未找到容器名称前缀 %s: %v", prefix, names)
	return ""
}

func assessmentLifecycleFixtureNames(character string) []string {
	token := strings.Repeat(character, 32)
	result := make([]string, 0, len(assessmentContainerPrefixes))
	for _, prefix := range assessmentContainerPrefixes {
		result = append(result, prefix+token)
	}
	return result
}

func assessmentRunRecords(names []string) string {
	var records strings.Builder
	for _, name := range names {
		fmt.Fprintf(&records, "run --rm --name %s image\n", name)
	}
	return records.String()
}

func assertAssessmentContainerGroupsDisjoint(t *testing.T, first, second []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(first))
	for _, name := range first {
		seen[name] = struct{}{}
	}
	for _, name := range second {
		if _, exists := seen[name]; exists {
			t.Fatalf("并发门禁容器集合相交: %s", name)
		}
	}
}

func assertAssessmentRandomName(t *testing.T, name, prefix string) {
	t.Helper()
	value := strings.TrimPrefix(name, prefix)
	if len(value) != 32 {
		t.Fatalf("容器名称未绑定完整 128-bit 随机值: %s", name)
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			t.Fatalf("容器名称随机值不是小写十六进制: %s", name)
		}
	}
}

func writeAssessmentFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	writeAssessmentBytes(t, path, []byte(content), mode)
}

func writeAssessmentBytes(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func fakeAssessmentSuccessOD(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return "#!/usr/bin/env sh\nprintf '%s\\n' '" + hex.EncodeToString(value) + "'\n"
}

func assessmentEnvironment(overrides map[string]string) []string {
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key := strings.SplitN(item, "=", 2)[0]
		_, overridden := overrides[key]
		if !overridden && key != "ASSESSMENT_E2E_TREE_SHA" && key != "ASSESSMENT_E2E_SOURCE_SHA256" {
			result = append(result, item)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

const assessmentGateManifest = `# assessment-e2e-source-v1
scripts/
source/
tests/assessment-e2e/Dockerfile
tests/assessment-e2e/expected-tests.json
tests/assessment-e2e/source-files.txt
tests/assessment-e2e/validate-results.mjs
`

const fakeAssessmentRunner = `#!/usr/bin/env sh
set -eu
exec docker run --rm "$@"
`

const fakeAssessmentDocker = `#!/usr/bin/env sh
set -eu
command=${1:-}
shift || true
printf '%s %s\n' "$command" "$*" >>"$FAKE_STATE_DIR/docker-calls"
case "$command" in
  build|logs) exit 0 ;;
  inspect) printf '%s\n' true ;;
  run)
    arguments=" $* "
    runtime=
    token=
    tree=
    for argument in "$@"; do
      case "$argument" in
        *:/runtime) runtime=${argument%:/runtime} ;;
        E2E_FIXTURE_TOKEN=*) token=${argument#*=} ;;
        E2E_TREE_SHA=*) tree=${argument#*=} ;;
      esac
    done
    case "$arguments" in
      *" -d "*)
        printf '%s\n' '127.0.0.1:31234' >"$runtime/address"
        printf 'ok:%s:%s\n' "$token" "$tree" >"$FAKE_STATE_DIR/health"
        ;;
      *" npx playwright test "*)
        if [ "${FAKE_BROWSER_INTERRUPT:-0}" = 1 ]; then
          kill -TERM "$PPID"
          sleep 1
          exit 143
        fi
        [ -z "${FAKE_DRIFT_FILE:-}" ] || printf '%s\n' drift >>"$FAKE_DRIFT_FILE"
        printf '%s\n' '{}' >"$runtime/playwright-results.json"
        exit "${FAKE_BROWSER_STATUS:-0}"
        ;;
    esac
    ;;
  rm)
    [ "${1:-}" = -f ] && shift
    name=${1:-missing}
    found=false
    while IFS= read -r call; do
      case " $call " in
        *" --name $name "*) found=true; break ;;
      esac
    done <"$FAKE_STATE_DIR/docker-calls"
    [ "$found" = true ] || exit 1
    printf '%s\n' "$name" >>"$FAKE_STATE_DIR/removed"
    ;;
  *) printf 'unexpected docker command: %s\n' "$command" >&2; exit 9 ;;
esac
`

const fakeAssessmentCurl = `#!/usr/bin/env sh
set -eu
cat "$FAKE_STATE_DIR/health"
`

const fakeAssessmentChmod = `#!/usr/bin/env sh
exit 0
`

const fakeAssessmentOD = `#!/usr/bin/env sh
set -eu
case "$FAKE_OD_MODE" in
  od-failure) exit 7 ;;
  urandom-failure) printf '%s\n' 'random device read failed' >&2; exit 8 ;;
  empty) exit 0 ;;
  short) printf '%s\n' '00' ;;
  nonhex) printf '%s\n' 'gggggggggggggggggggggggggggggggg' ;;
  *) printf '%s\n' 'unexpected fake od mode' >&2; exit 9 ;;
esac
`

func extractAssessmentGitArchive(t *testing.T, gitPath, source, tree string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "source.tar")
	runAssessmentGit(t, gitPath, source, "archive", "--format=tar", "--output", archivePath, tree)
	payload, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	root := t.TempDir()
	reader := tar.NewReader(payload)
	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		extractAssessmentArchiveEntry(t, root, header, reader)
	}
	return root
}

func extractAssessmentArchiveEntry(t *testing.T, root string, header *tar.Header, reader io.Reader) {
	t.Helper()
	clean := filepath.Clean(filepath.FromSlash(header.Name))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		t.Fatalf("git archive 包含越界路径: %q", header.Name)
	}
	target := filepath.Join(root, clean)
	switch header.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
			t.Fatal(err)
		}
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("解压 %s: copy=%v close=%v", header.Name, copyErr, closeErr)
		}
	default:
		t.Fatalf("git archive 包含不支持的条目类型: %q type=%d", header.Name, header.Typeflag)
	}
}

func newAssessmentArchive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	scriptDirectory := filepath.Join(root, "scripts")
	e2eDirectory := filepath.Join(root, "tests", "assessment-e2e")
	if err := os.MkdirAll(scriptDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e2eDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("validate-assessment-browser.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(scriptDirectory, "validate-assessment-browser.sh"), payload, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "# assessment-e2e-source-v1\nscripts/validate-assessment-browser.sh\n" +
		"source.txt\ntests/assessment-e2e/source-files.txt\n"
	if err = os.WriteFile(filepath.Join(e2eDirectory, "source-files.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "source.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func assessmentSourceDigest(t *testing.T, root string) string {
	t.Helper()
	manifestPath := filepath.Join(root, "tests", "assessment-e2e", "source-files.txt")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	paths := assessmentSourcePaths(t, root, string(payload))
	var records strings.Builder
	records.WriteString("assessment-e2e-source-v1\n")
	for _, path := range paths {
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		contentHash := sha256.Sum256(content)
		fmt.Fprintf(&records, "%x  %s\n", contentHash, path)
	}
	digest := sha256.Sum256([]byte(records.String()))
	return fmt.Sprintf("%x", digest)
}

func assessmentSourcePaths(t *testing.T, root, manifest string) []string {
	t.Helper()
	paths := make([]string, 0)
	for _, entry := range strings.Split(strings.ReplaceAll(manifest, "\r\n", "\n"), "\n") {
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if !strings.HasSuffix(entry, "/") {
			paths = append(paths, entry)
			continue
		}
		directory := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(entry, "/")))
		err := filepath.Walk(directory, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() {
				return walkErr
			}
			relative, relErr := filepath.Rel(root, path)
			paths = append(paths, filepath.ToSlash(relative))
			return relErr
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(paths)
	return paths
}

func assessmentManifest(titles ...string) any {
	tests := make([]any, 0, len(titles))
	for _, title := range titles {
		tests = append(tests, map[string]any{"file": "assessment.spec.js", "title": title})
	}
	return map[string]any{"version": 1, "tests": tests}
}

func assessmentReport(specs []any, stats map[string]any) any {
	return map[string]any{"suites": []any{map[string]any{"specs": specs}}, "errors": []any{}, "stats": stats}
}

func assessmentSpec(title, state string) any {
	expectedStatus, status, resultStatus := "passed", "expected", "passed"
	annotations := []any{}
	if state == "skip" || state == "fixme" {
		expectedStatus, status, resultStatus = "skipped", "skipped", "skipped"
		annotations = []any{map[string]any{"type": state}}
	}
	return map[string]any{"file": "assessment.spec.js", "title": title, "ok": true,
		"tests": []any{map[string]any{"projectName": "", "expectedStatus": expectedStatus,
			"status": status, "annotations": annotations, "results": []any{map[string]any{"status": resultStatus}}}}}
}

func assessmentRetriedSpec(title string) any {
	return map[string]any{"file": "assessment.spec.js", "title": title, "ok": true,
		"tests": []any{map[string]any{"projectName": "", "expectedStatus": "passed",
			"status": "expected", "annotations": []any{}, "results": []any{
				map[string]any{"status": "failed"}, map[string]any{"status": "passed"},
			}}}}
}

func runAssessmentAudit(t *testing.T, node string, manifest, report any) (string, error) {
	t.Helper()
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "manifest.json")
	reportPath := filepath.Join(directory, "report.json")
	writeAssessmentJSON(t, manifestPath, manifest)
	writeAssessmentJSON(t, reportPath, report)
	validator := filepath.Join("..", "tests", "assessment-e2e", "validate-results.mjs")
	command := exec.Command(node, validator, manifestPath, reportPath)
	payload, err := command.CombinedOutput()
	return string(payload), err
}

func writeAssessmentJSON(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
