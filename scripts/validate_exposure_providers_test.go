package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestValidateExposureProvidersScript(t *testing.T) {
	entrypoint := readValidationScript(t)
	for _, fragment := range []string{
		"golang:1.26.7-bookworm sh -c '\n    set -eu",
		"AI_GDM_LIVE_EXPOSURE=0 go test -race",
		`if [ "${AI_GDM_EXPOSURE_GATE_LIBRARY_ONLY:-0}" = "1" ]; then`,
		"生产暴露验证入口禁止启用 library-only 模式",
		`. "$ROOT/scripts/validate-exposure-gates.lib.sh"`,
		". ./scripts/validate-exposure-gates.lib.sh",
		`-run "^Test(ValidateExposureProvidersScript|ExposureGateLibrary.*|OfflineGates.*|LiveGate.*)$"`,
		"\n    run_postgres_gate\n",
		"\n    run_cmd_gate\n",
		"\n      run_live_gate\n",
	} {
		if !strings.Contains(entrypoint, fragment) {
			t.Fatalf("暴露验证入口缺少 %q", fragment)
		}
	}
	if strings.Count(entrypoint, "AI_GDM_EXPOSURE_GATE_LIBRARY_ONLY") != 1 {
		t.Fatal("生产入口不得保留可复用的 library-only 返回路径")
	}

	library := readValidationLibrary(t)
	for _, fragment := range []string{
		"if postgres_output=$(go test ./internal/adapters/storage/postgres",
		"if cmd_output=$(go test ./cmd/server",
		"if live_output=$(go test -p=1",
		"grep -F -x -- \"$count_expected\"",
		"grep -E -x -- \"[[:space:]]*--- $count_kind: $count_name",
		"skip_count=$(count_result_line",
		"fail_count=$(count_result_line",
		"TestExposureOverpassLimitationSurvivesPostgresLossHTTPChain",
		"TestExposureRejectsMissingOverpassCoordinateBeforePostgresProjection",
		"TestExposureRejectsBadOverpassTagsBeforePostgresProjection",
		"TestExposureRejectsUnicodeFoldedOverpassTagsBeforePostgresProjection",
		"TestExposureRejectsUnicodeFoldedGeoBoundarySourceBeforePostgresLoss",
		"TestExposureRejectsMismatchedGeoBoundaryShapeBeforePostgresLoss",
		"TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放/Moved_Permanently",
		"TestWorldPopProductionClientRedirectContracts/创建_POST_禁止重定向重放/Permanent_Redirect",
		"TestWorldPopProductionClientRedirectContracts/轮询_GET_允许同源_HTTPS_重定向",
	} {
		if !strings.Contains(library, fragment) {
			t.Fatalf("暴露门禁私有库缺少 %q", fragment)
		}
	}
	assertAnchoredRunPatterns(t, library)
	assertCaptureOrder(t, library, "run_postgres_gate()", "$postgres_output", "$postgres_status")
	assertCaptureOrder(t, library, "run_cmd_gate()", "$cmd_output", "$cmd_status")
	assertCaptureOrder(t, library, "run_live_gate()", "$live_output", "$live_status")
}

func TestExposureGateLibraryRejectsDirectExecution(t *testing.T) {
	shell := requireShell(t)
	fakeBin, marker := writeDockerProbe(t)
	command := exec.Command(shell, "validate-exposure-providers.sh")
	command.Env = append(libraryEnvironment(fakeBin, marker),
		"AI_GDM_EXPOSURE_GATE_LIBRARY_ONLY=1")
	assertLibraryCommand(t, command, marker, 64, "生产暴露验证入口禁止启用 library-only 模式")
}

func TestExposureGateLibraryFlagRejectsSourcedEntrypoint(t *testing.T) {
	shell := requireShell(t)
	fakeBin, marker := writeDockerProbe(t)
	script := "AI_GDM_EXPOSURE_GATE_LIBRARY_ONLY=1\n" +
		". ./validate-exposure-providers.sh"
	command := exec.Command(shell, "-c", script)
	command.Env = libraryEnvironment(fakeBin, marker)
	assertLibraryCommand(t, command, marker, 64, "生产暴露验证入口禁止启用 library-only 模式")
}

func TestExposureGateLibraryLoadsFunctionsFromPrivateLibrary(t *testing.T) {
	shell := requireShell(t)
	fakeBin, marker := writeDockerProbe(t)
	script := ". ./validate-exposure-gates.lib.sh\n" +
		"command -v run_postgres_gate >/dev/null\n" +
		"command -v run_cmd_gate >/dev/null\n" +
		"command -v run_live_gate >/dev/null"
	command := exec.Command(shell, "-c", script)
	command.Env = libraryEnvironment(fakeBin, marker)
	assertLibraryCommand(t, command, marker, 0, "")
}

func TestOfflineGatesRequireExactRunAndPass(t *testing.T) {
	shell := requireShell(t)
	assertPostgresGateSentinels(t, shell)
	assertCmdGateSentinels(t, shell)
}

func assertCmdGateSentinels(t *testing.T, shell string) {
	t.Helper()
	names := cmdGateNames()
	exact := testPasses(names...)
	assertGate(t, shell, "cmd", exact, 0, 0, "")
	for _, name := range names {
		t.Run(name+"/missing", func(t *testing.T) {
			assertGate(t, shell, "cmd", testPassesExcept(names, name), 0, 1, "RUN=0")
		})
		t.Run(name+"/prefix", func(t *testing.T) {
			assertGate(t, shell, "cmd", replaceTestPass(exact, name, name+"Extra"), 0, 1, "RUN=0")
		})
		t.Run(name+"/skip", func(t *testing.T) {
			output := strings.Replace(exact, testPass(name), testSkip(name), 1)
			assertGate(t, shell, "cmd", output, 0, 1,
				"PASS=0 SKIP=1")
		})
	}
}

func cmdGateNames() []string {
	const parent = "TestWorldPopProductionClientRedirectContracts"
	const create = parent + "/创建_POST_禁止重定向重放"
	return []string{
		parent,
		create,
		create + "/Moved_Permanently",
		create + "/Found",
		create + "/See_Other",
		create + "/Temporary_Redirect",
		create + "/Permanent_Redirect",
		parent + "/轮询_GET_允许同源_HTTPS_重定向",
	}
}

func assertPostgresGateSentinels(t *testing.T, shell string) {
	t.Helper()
	names := []string{"TestExposureOverpassLimitationSurvivesPostgresLossHTTPChain",
		"TestExposureRejectsMissingOverpassCoordinateBeforePostgresProjection",
		"TestExposureRejectsBadOverpassTagsBeforePostgresProjection",
		"TestExposureRejectsUnicodeFoldedOverpassTagsBeforePostgresProjection",
		"TestExposureRejectsUnicodeFoldedGeoBoundarySourceBeforePostgresLoss",
		"TestExposureRejectsMismatchedGeoBoundaryShapeBeforePostgresLoss"}
	exact := testPasses(names...)
	assertGate(t, shell, "postgres", exact, 0, 0, "")
	for _, name := range names {
		t.Run(name+"/missing", func(t *testing.T) {
			assertGate(t, shell, "postgres", testPassesExcept(names, name), 0, 1, "RUN=0")
		})
		t.Run(name+"/prefix", func(t *testing.T) {
			assertGate(t, shell, "postgres", replaceTestPass(exact, name, name+"Extra"), 0, 1, "RUN=0")
		})
		t.Run(name+"/skip", func(t *testing.T) {
			output := strings.Replace(exact, testPass(name), testSkip(name), 1)
			assertGate(t, shell, "postgres", output, 0, 1, "PASS=0 SKIP=1")
		})
		t.Run(name+"/fail", func(t *testing.T) {
			output := strings.Replace(exact, testPass(name), testFail(name), 1)
			assertGate(t, shell, "postgres", output, 0, 1, "PASS=0 SKIP=0 FAIL=1")
		})
		t.Run(name+"/duplicate", func(t *testing.T) {
			assertGate(t, shell, "postgres", exact+testPass(name), 0, 1, "RUN=2 PASS=2")
		})
	}
}

func TestOfflineGatesPrintFailureAndPreserveExitCode(t *testing.T) {
	shell := requireShell(t)
	for _, branch := range []string{"postgres", "cmd"} {
		t.Run(branch, func(t *testing.T) {
			marker := branch + "-provider-failure"
			output := assertGate(t, shell, branch, marker+"\n", 7, 7, marker)
			if strings.Count(output, marker) != 1 {
				t.Fatalf("失败输出应恰好打印一次: %s", output)
			}
		})
	}
}

func TestLiveGateRequiresOnePassPerTarget(t *testing.T) {
	shell := requireShell(t)
	exact := testPass("TestLivePopulation") +
		testPass("TestLiveInfrastructure") + testPass("TestLiveBoundary")
	assertGate(t, shell, "live", exact, 0, 0, "")
}

func TestLiveGateRejectsInvalidResults(t *testing.T) {
	shell := requireShell(t)
	validTail := testPass("TestLiveInfrastructure") + testPass("TestLiveBoundary")
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{name: "skip", output: testSkip("TestLivePopulation") + validTail, want: "PASS=0 SKIP=1"},
		{name: "fail", output: testFail("TestLivePopulation") + validTail, want: "PASS=0 SKIP=0 FAIL=1"},
		{name: "missing", output: testPass("TestLivePopulation") + testPass("TestLiveInfrastructure"), want: "RUN=0"},
		{name: "prefix", output: testPass("TestLivePopulationExtra") + validTail, want: "RUN=0"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			assertGate(t, shell, "live", item.output, 0, 1, item.want)
		})
	}
}

func TestLiveGateRejectsDuplicateRunOrPass(t *testing.T) {
	shell := requireShell(t)
	validTail := testPass("TestLiveInfrastructure") + testPass("TestLiveBoundary")
	duplicateRun := testRun("TestLivePopulation") + testPass("TestLivePopulation") + validTail
	assertGate(t, shell, "live", duplicateRun, 0, 1, "RUN=2 PASS=1")
	duplicatePass := testPass("TestLivePopulation") + testResult("PASS", "TestLivePopulation") + validTail
	assertGate(t, shell, "live", duplicatePass, 0, 1, "RUN=1 PASS=2")
}

func readValidationScript(t *testing.T) string {
	t.Helper()
	return readScriptFile(t, "validate-exposure-providers.sh")
}

func readValidationLibrary(t *testing.T) string {
	t.Helper()
	return readScriptFile(t, "validate-exposure-gates.lib.sh")
}

func readScriptFile(t *testing.T, name string) string {
	t.Helper()
	payload, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(payload), "\r\n", "\n")
}

func assertAnchoredRunPatterns(t *testing.T, script string) {
	t.Helper()
	patterns := []string{
		`-run "^Test(Exposure.*|ReadExposure.*|AdministrativeProjection.*|ProjectAdministration.*|ProjectInfrastructure.*|InfrastructureBinding.*|HasCurrent.*)$"`,
		`-run "^Test(DefaultExposure.*|BuildExposure.*|NewExposure.*|WorldPopProductionClientRedirectContracts|SpatialRefresh.*Exposure.*|SpatialRefreshFreshPath.*)$"`,
		`-run "^TestLive(Population|Infrastructure|Boundary)$"`,
	}
	for _, pattern := range patterns {
		if strings.Count(script, pattern) != 1 {
			t.Fatalf("测试选择表达式必须且只能精确出现一次: %s", pattern)
		}
	}
}

func assertCaptureOrder(t *testing.T, script, start, output, status string) {
	t.Helper()
	startIndex := strings.Index(script, start)
	if startIndex < 0 {
		t.Fatalf("捕获分支缺失: %s", start)
	}
	assertOrdered(t, script[startIndex:], `printf "%s\n" "`+output+`"`, `return "`+status+`"`)
}

func requireShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前平台未安装 sh，需在远端补跑行为门禁")
	}
	return shell
}

func assertGate(
	t *testing.T,
	shell, branch, input string,
	commandCode, wantCode int,
	wantText string,
) string {
	t.Helper()
	functionName := gateFunction(t, branch)
	script := ". ./validate-exposure-gates.lib.sh\n" + functionName
	fakeBin, fixturePath := writeFakeGo(t, input)
	command := exec.Command(shell, "-c", script)
	command.Env = gateEnvironment(commandCode, fakeBin, fixturePath)
	payload, err := command.CombinedOutput()
	code := commandExitCode(t, err)
	output := string(payload)
	if code != wantCode || (wantText != "" && !strings.Contains(output, wantText)) {
		t.Fatalf("%s gate code=%d want=%d output=%s", branch, code, wantCode, output)
	}
	return output
}

func gateFunction(t *testing.T, branch string) string {
	t.Helper()
	switch branch {
	case "postgres":
		return "run_postgres_gate"
	case "cmd":
		return "run_cmd_gate"
	case "live":
		return "run_live_gate"
	default:
		t.Fatalf("未知 gate 分支: %s", branch)
		return ""
	}
}

func writeFakeGo(t *testing.T, output string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	fixturePath := filepath.Join(directory, "go-output.txt")
	if err := os.WriteFile(fixturePath, []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeGo := filepath.Join(directory, "go")
	payload := []byte("#!/usr/bin/env sh\ncat \"$AI_GDM_GATE_FIXTURE_FILE\"\nexit \"$AI_GDM_GATE_EXIT_CODE\"\n")
	if err := os.WriteFile(fakeGo, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	return directory, fixturePath
}

func writeDockerProbe(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	marker := filepath.Join(directory, "docker-called")
	fakeDocker := filepath.Join(directory, "docker")
	payload := []byte("#!/usr/bin/env sh\nprintf called > \"$AI_GDM_DOCKER_PROBE_FILE\"\nexit 97\n")
	if err := os.WriteFile(fakeDocker, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	return directory, marker
}

func assertLibraryCommand(
	t *testing.T,
	command *exec.Cmd,
	marker string,
	wantCode int,
	wantText string,
) {
	t.Helper()
	payload, err := command.CombinedOutput()
	code := commandExitCode(t, err)
	output := string(payload)
	if code != wantCode || (wantText != "" && !strings.Contains(output, wantText)) {
		t.Fatalf("library gate code=%d want=%d output=%s", code, wantCode, output)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("library-only 路径不得调用 docker")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}

func gateEnvironment(commandCode int, fakeBin, fixturePath string) []string {
	result := cleanGateEnvironment(fakeBin)
	return append(result,
		"AI_GDM_GATE_EXIT_CODE="+strconv.Itoa(commandCode),
		"AI_GDM_GATE_FIXTURE_FILE="+fixturePath,
	)
}

func libraryEnvironment(fakeBin, marker string) []string {
	return append(cleanGateEnvironment(fakeBin), "AI_GDM_DOCKER_PROBE_FILE="+marker)
}

func cleanGateEnvironment(fakeBin string) []string {
	result := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !hasGateEnvironmentKey(value) {
			result = append(result, value)
		}
	}
	return append(result, "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func hasGateEnvironmentKey(value string) bool {
	return strings.HasPrefix(value, "PATH=") ||
		strings.HasPrefix(value, "AI_GDM_GATE_EXIT_CODE=") ||
		strings.HasPrefix(value, "AI_GDM_GATE_FIXTURE_FILE=") ||
		strings.HasPrefix(value, "AI_GDM_DOCKER_PROBE_FILE=") ||
		strings.HasPrefix(value, "AI_GDM_EXPOSURE_GATE_LIBRARY_ONLY=")
}

func commandExitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("门禁未返回 shell 退出码: %v", err)
	}
	return exitError.ExitCode()
}

func assertOrdered(t *testing.T, text, first, second string) {
	t.Helper()
	firstIndex := strings.Index(text, first)
	secondIndex := strings.Index(text, second)
	if firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex {
		t.Fatalf("输出顺序无效: first=%d second=%d text=%s", firstIndex, secondIndex, text)
	}
}

func testRun(name string) string {
	return "=== RUN   " + name + "\n"
}

func testResult(kind, name string) string {
	indent := strings.Repeat("    ", strings.Count(name, "/"))
	return indent + "--- " + kind + ": " + name + " (0.01s)\n"
}

func testPass(name string) string {
	return testRun(name) + testResult("PASS", name)
}

func testPasses(names ...string) string {
	var result strings.Builder
	for _, name := range names {
		result.WriteString(testPass(name))
	}
	return result.String()
}

func testPassesExcept(names []string, omitted string) string {
	var result strings.Builder
	for _, name := range names {
		if name != omitted {
			result.WriteString(testPass(name))
		}
	}
	return result.String()
}

func replaceTestPass(output, oldName, newName string) string {
	return strings.Replace(output, testPass(oldName), testPass(newName), 1)
}

func testSkip(name string) string {
	return testRun(name) + testResult("SKIP", name)
}

func testFail(name string) string {
	return testRun(name) + testResult("FAIL", name)
}
