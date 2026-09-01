package scripts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const maxCandidateBytes = 2 << 20

const maxCandidateDecodeBytes = 8 * maxCandidateBytes

const (
	maxYAMLCandidateBytes = 512 << 10
	maxYAMLDocuments      = 8
	maxYAMLDepth          = 32
	maxYAMLNodes          = 4096
	maxYAMLScalarBytes    = 128 << 10
)

var errJSONKeyCollision = errors.New("JSON 字段名重复或大小写冲突")

func TestSecurityValidationScriptLocksRequiredPackages(t *testing.T) {
	payload, err := os.ReadFile("validate-security.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(payload), "\r\n", "\n")
	for _, fragment := range []string{
		"./internal/platform/config", "./internal/platform/httpserver",
		"./internal/adapters/http/mapapi", "./internal/adapters/http/webui",
		"./cmd/server", "./scripts", "-skip '^TestReleaseArchiveValidator' -count=20",
		"-run '^TestReleaseArchiveValidator' -count=1", "node --check internal/adapters/http/webui/static/api.js",
		"node tests/security-e2e/audit-results.test.mjs", "--cidfile", "ai-gdm-security-go-",
		"ai-gdm-security-node-", "trap 'terminate 129' HUP",
		"SECURITY_E2E_TREE_SHA", "SECURITY_E2E_SOURCE_SHA256",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("安全门禁脚本缺少 %q", fragment)
		}
	}
}

func TestSecurityBrowserDependenciesAreContentPinned(t *testing.T) {
	dockerfile, err := os.ReadFile("../tests/security-e2e/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	for _, required := range []string{
		"FROM mcr.microsoft.com/playwright:v1.55.0-noble@sha256:",
		"COPY package.json package-lock.json ./", "npm ci --ignore-scripts",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("安全浏览器 Dockerfile 缺少固定依赖 %q", required)
		}
	}
	for _, forbidden := range []string{"npm install", "--no-package-lock", "apt-get install"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("安全浏览器 Dockerfile 包含可变依赖命令 %q", forbidden)
		}
	}
	validateSecurityPackageLock(t)
}

func validateSecurityPackageLock(t *testing.T) {
	t.Helper()
	payload, err := os.ReadFile("../tests/security-e2e/package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version, Resolved, Integrity string
		} `json:"packages"`
	}
	if err = json.Unmarshal(payload, &lock); err != nil || lock.LockfileVersion != 3 {
		t.Fatalf("安全浏览器 lockfile 无效: %v", err)
	}
	for path, item := range lock.Packages {
		if path == "" {
			continue
		}
		if item.Version == "" || !strings.HasPrefix(item.Resolved, "https://registry.npmjs.org/") ||
			!strings.HasPrefix(item.Integrity, "sha512-") {
			t.Fatalf("安全浏览器依赖未固定: %s", path)
		}
	}
	if item := lock.Packages["node_modules/@playwright/test"]; item.Version != "1.55.0" {
		t.Fatalf("Playwright lock 版本漂移: %q", item.Version)
	}
}

func TestCandidateFilesDoNotContainSecretsOrPrivateKeyFiles(t *testing.T) {
	if err := scanCandidateFiles(filepath.Clean("..")); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateScannerReadsStagedBlobInsteadOfWorktree(t *testing.T) {
	root := newGitRepository(t)
	path := filepath.Join(root, "config.txt")
	secret := "sk" + "-" + strings.Repeat("a", 32)
	writeFile(t, path, secret)
	runGit(t, root, "add", "config.txt")
	writeFile(t, path, "safe worktree text")
	if err := scanCandidateFiles(root); err == nil || !strings.Contains(err.Error(), "config.txt") {
		t.Fatalf("未拒绝仅存在于暂存 blob 的密钥: %v", err)
	}
}

func TestCandidateScannerReadsStagedSensitiveSettingInsteadOfWorktree(t *testing.T) {
	root := newGitRepository(t)
	path := filepath.Join(root, "evaluation.env")
	key := "APP_ADMIN_" + "TOKEN"
	writeFile(t, path, key+"=candidate-secret-value")
	runGit(t, root, "add", "evaluation.env")
	writeFile(t, path, key+"=")
	if err := scanCandidateFiles(root); err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("未拒绝仅存在于暂存 blob 的敏感配置: %v", err)
	}
}

func TestCandidateScannerReadsStagedPrivateKeysInsteadOfWorktree(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		payload func() string
	}{
		{
			name: "service_account_json", path: "service-account.json",
			payload: func() string {
				header := "-----" + "BEGIN PRIVATE KEY-----"
				return `{"private_key":"` + header + `\nsynthetic\n-----END PRIVATE KEY-----"}`
			},
		},
		{
			name: "openssh_embedded", path: "deployment.txt",
			payload: func() string {
				return "prefix:" + "-----" + "BEGIN OPENSSH PRIVATE KEY-----\nsynthetic"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newGitRepository(t)
			path := filepath.Join(root, test.path)
			writeFile(t, path, test.payload())
			runGit(t, root, "add", test.path)
			writeFile(t, path, "safe worktree text")
			if err := scanCandidateFiles(root); err == nil || !strings.Contains(err.Error(), test.path) {
				t.Fatalf("未拒绝仅存在于暂存 blob 的私钥 %s: %v", test.path, err)
			}
		})
	}
}

func TestCandidateScannerRejectsPrivateKeyKinds(t *testing.T) {
	for _, kind := range []string{"", "OPENSSH ", "RSA ", "EC ", "DSA ", "ENCRYPTED "} {
		payload := []byte("-----" + "BEGIN " + kind + "PRIVATE KEY-----")
		if !containsSecretSignature(payload) {
			t.Fatalf("未识别 %q 私钥头", kind)
		}
	}
	if !containsSecretSignature([]byte("-----" + "BEGIN PGP " + "PRIVATE KEY BLOCK-----")) {
		t.Fatal("未识别 PGP 私钥头")
	}
}

func TestCandidateScannerRejectsEmbeddedEncodedOrBOMPrivateKeys(t *testing.T) {
	header := "-----" + "BEGIN " + "OPENSSH PRIVATE KEY-----"
	escaped := strings.Repeat(`\u002d`, 5) + "BEGIN OPENSSH PRIVATE KEY" + strings.Repeat(`\u002d`, 5)
	for _, payload := range [][]byte{
		[]byte("prefix:" + header),
		append([]byte{0xef, 0xbb, 0xbf}, []byte(header)...),
		[]byte(`{"privateKey":"prefix:` + escaped + `\nvalue"}`),
	} {
		if !containsSecretSignature(payload) {
			t.Fatalf("未识别内嵌、编码或 BOM 私钥头: %q", payload)
		}
	}
}

func TestCandidateScannerRejectsNonEmptySensitiveSettings(t *testing.T) {
	keys := []string{"APP_ADMIN_" + "TOKEN", "LLM_API_" + "KEY", "BOCHA_API_" + "KEY",
		"AMAP_API_" + "KEY", "AMAP_JS" + "CODE", "OPEN_METEO_API_" + "KEY",
		"REDIS_" + "PASSWORD", "TEST_REDIS_" + "PASSWORD"}
	for _, key := range keys {
		payload := []byte(key + `="candidate-secret-value"`)
		if err := validateCandidate("evaluation.env", payload); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("未拒绝 %s 非空值: %v", key, err)
		}
	}
}

func TestCandidateScannerRejectsSensitiveAssignmentsAcrossFormats(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	secret := "candidate-secret-value"
	payloads := []string{
		`{"safe":"value","` + key + `":"` + secret + `"}`,
		"{\"" + key + "\":\n\"" + secret + "\"}",
		`{"env":["` + key + `=` + secret + `"]}`,
		`{"script":"env ` + key + `=` + secret + ` command"}`,
		`docker run -e "` + key + `=` + secret + `" image`,
		`ENV ` + key + ` ` + secret,
		`setx ` + key + ` ` + secret,
		`- ` + key + `=` + secret,
		`env SAFE=value ` + key + `=` + secret + ` command`,
		`$env:` + key + `='` + secret + `'`,
		key + `=redacted,` + secret,
		key + `=unavailable"` + secret + `"`,
		key + `=$ADMIN_TOKEN"-` + secret + `"`,
	}
	for index, payload := range payloads {
		if err := validateCandidate("assignment.txt", []byte(payload)); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("格式 %d 未拒绝敏感赋值: %v", index, err)
		}
	}
}

func TestCandidateScannerRejectsStructuredEnvironmentValues(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	secret := "candidate-secret-value"
	payloads := []string{
		`{"env":[{"name":"` + key + `","value":"` + secret + `"}]}`,
		"env:\n  - name: " + key + "\n    value: " + secret + "\n",
		"env:\n  - value: " + secret + "\n    name: " + key + "\n",
	}
	for index, payload := range payloads {
		if err := validateCandidate("deployment.yaml", []byte(payload)); err == nil {
			t.Fatalf("结构化格式 %d 未拒绝敏感值", index)
		}
	}
}

func TestCandidateScannerRejectsJSONKeyCollisions(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	secret := "candidate-secret-value"
	payloads := []string{
		`{"name":"` + key + `","value":"` + secret + `","value":""}`,
		`{"name":"` + key + `","value":"","Value":"` + secret + `"}`,
		`{"name":"safe","name":"` + key + `","value":"` + secret + `"}`,
		`{"Name":"` + key + `","name":"safe","value":""}`,
	}
	for index, payload := range payloads {
		if err := validateCandidate("deployment.json", []byte(payload)); err == nil {
			t.Fatalf("JSON 冲突格式 %d 未 fail-closed", index)
		}
	}
}

func TestCandidateScannerRejectsUnquotedSensitiveSuffixes(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	for index, payload := range []string{
		key + "=redacted#candidate-secret",
		key + ": redacted#candidate-secret",
		key + ": redacted,candidate-secret",
	} {
		if err := validateCandidate("deployment.yaml", []byte(payload)); err == nil {
			t.Fatalf("未拒绝未加引号的敏感后缀 %d", index)
		}
	}
	if err := validateCandidate("deployment.yaml", []byte(key+": redacted # 仅为注释")); err != nil {
		t.Fatalf("明确占位值后的 YAML 注释被误判: %v", err)
	}
}

func TestCandidateScannerRejectsDockerAndPowerShellAssignments(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	secret := "candidate-secret-value"
	payloads := []string{
		"ENV " + key + " \\\n    " + secret,
		"Set-Item Env:" + key + " " + secret,
		"Set-Item -Path Env:" + key + " -Value " + secret,
		`[Environment]::SetEnvironmentVariable("` + key + `", "` + secret + `")`,
	}
	for index, payload := range payloads {
		if err := validateCandidate("runtime.txt", []byte(payload)); err == nil {
			t.Fatalf("命令格式 %d 未拒绝敏感值", index)
		}
	}
}

func TestCandidateScannerKeepsYAMLListItemsBounded(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	unsafe := "env:\n  - name: " + key + " # 管理令牌\n    value: candidate-secret-value\n"
	if err := validateCandidate("deployment.yaml", []byte(unsafe)); err == nil {
		t.Fatal("带注释的 Kubernetes 敏感变量名称未被拒绝")
	}
	safe := "env:\n  - name: SAFE_VALUE\n    value: candidate-secret-value\n" +
		"  - name: " + key + "\n    valueFrom:\n      secretKeyRef:\n        name: runtime-secret\n"
	if err := validateCandidate("deployment.yaml", []byte(safe)); err != nil {
		t.Fatalf("跨 YAML 列表项错误绑定 value: %v", err)
	}
}

func TestCandidateScannerRejectsYAMLSemanticEscapesAndStructures(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	header := "BEGIN OPENSSH PRIVATE KEY"
	tests := []struct {
		name    string
		payload string
	}{
		{name: "hex_name", payload: "env:\n  - name: \"\\x41PP_ADMIN_TOKEN\"\n    value: candidate-secret-value\n"},
		{name: "unicode_name", payload: "env:\n  - name: \"\\U00000041PP_ADMIN_TOKEN\"\n    value: candidate-secret-value\n"},
		{name: "anchored_name", payload: "env:\n  - name: &admin " + key + "\n    value: candidate-secret-value\n"},
		{name: "folded_name", payload: "env:\n  - name: >-\n      " + key + "\n    value: candidate-secret-value\n"},
		{name: "encoded_mapping_key", payload: "\"\\x41PP_ADMIN_TOKEN\": candidate-secret-value\n"},
		{name: "encoded_private_key", payload: "private_key: \"" + strings.Repeat(`\x2d`, 5) + header + strings.Repeat(`\x2d`, 5) + "\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCandidate("deployment.yaml", []byte(test.payload)); err == nil {
				t.Fatal("YAML 解码后的敏感内容未 fail-closed")
			}
		})
	}
}

func TestCandidateScannerReadsStagedYAMLSemanticsInsteadOfWorktree(t *testing.T) {
	root := newGitRepository(t)
	path := filepath.Join(root, "deployment.yaml")
	payload := "\"\\x41PP_ADMIN_TOKEN\": candidate-secret-value\n"
	writeFile(t, path, payload)
	runGit(t, root, "add", "deployment.yaml")
	writeFile(t, path, "safe: value\n")
	if err := scanCandidateFiles(root); err == nil || !strings.Contains(err.Error(), "deployment.yaml") {
		t.Fatalf("未拒绝仅存在于暂存 YAML blob 的密钥: %v", err)
	}
}

func TestCandidateScannerAllowsYAMLSecretReferences(t *testing.T) {
	payload := "env:\n  - name: \"\\x41PP_ADMIN_TOKEN\"\n" +
		"    valueFrom:\n      secretKeyRef:\n        name: runtime-admin\n        key: token\n"
	if err := validateCandidate("deployment.yml", []byte(payload)); err != nil {
		t.Fatalf("仅引用 secretKeyRef 的 YAML 被误判: %v", err)
	}
}

func TestCandidateScannerBoundsYAMLStructures(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "alias", payload: "value: &safe placeholder\ncopy: *safe\n"},
		{name: "depth", payload: nestedYAML(maxYAMLDepth + 2)},
		{name: "nodes", payload: repeatedYAMLNodes(maxYAMLNodes + 1)},
		{name: "bytes", payload: "value: " + strings.Repeat("a", maxYAMLCandidateBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCandidate("bounded.yaml", []byte(test.payload)); err == nil {
				t.Fatal("超出 YAML 审计预算的结构未 fail-closed")
			}
		})
	}
}

func nestedYAML(depth int) string {
	var result strings.Builder
	for level := 0; level < depth; level++ {
		result.WriteString(strings.Repeat("  ", level))
		result.WriteString("level:\n")
	}
	result.WriteString(strings.Repeat("  ", depth))
	result.WriteString("value\n")
	return result.String()
}

func repeatedYAMLNodes(count int) string {
	var result strings.Builder
	result.WriteString("items:\n")
	for index := 0; index < count; index++ {
		fmt.Fprintf(&result, "  - item-%d\n", index)
	}
	return result.String()
}

func TestCandidateScannerDecodesEscapesUntilStable(t *testing.T) {
	encoded := `\u0041PP_ADMIN_` + `TOKEN=candidate-secret-value`
	for range 20 {
		encoded = strings.ReplaceAll(encoded, `\`, `\\`)
	}
	if len(encoded) > maxCandidateBytes {
		t.Fatalf("测试输入超过候选文件预算: %d", len(encoded))
	}
	if err := validateCandidate("encoded.txt", []byte(encoded)); err == nil {
		t.Fatal("超过旧固定轮数的转义敏感配置未被拒绝")
	}
}

func TestCandidateScannerAvoidsCommandAndPuttyProseFalsePositives(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	payload := "Use env " + key + " variable at runtime.\nPrivate-" + "Lines: is a documentation label."
	if err := validateCandidate("guide.txt", []byte(payload)); err != nil {
		t.Fatalf("普通说明文本被误判: %v", err)
	}
	if containsPuttyPrivateKey("PuTTY-" + "User-Key-File-3: documentation") {
		t.Fatal("单个 PuTTY 标记不应被判定为完整私钥")
	}
}

func TestCandidateScannerAllowsVariableAssignmentsAcrossFormats(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	payloads := []string{
		`{"env":["` + key + `=$ADMIN_TOKEN"]}`,
		`{"script":"env ` + key + `=${ADMIN_TOKEN} command"}`,
		`docker run -e "` + key + `=$ADMIN_TOKEN" image`,
		`- ` + key + `=$ADMIN_TOKEN`,
		`$env:` + key + `='$env:ADMIN_TOKEN'`,
	}
	for index, payload := range payloads {
		if err := validateCandidate("assignment.txt", []byte(payload)); err != nil {
			t.Fatalf("格式 %d 的变量引用被误判为密钥: %v", index, err)
		}
	}
}

func TestCandidateScannerChecksDatabaseURLPasswords(t *testing.T) {
	databaseKey := "DATABASE_" + "URL"
	testDatabaseKey := "TEST_DATABASE_" + "URL"
	for _, key := range []string{databaseKey, testDatabaseKey} {
		unsafeValues := []string{
			"postgres://user:candidate-password@db.example.invalid/app",
			"postgres://user:%63andidate-password@db.example.invalid/app",
			"postgres://db.example.invalid/app?password=candidate-password",
			"postgres://db.example.invalid/app?sslpassword=candidate-password",
			"candidate-password",
			"mysql://user@db.example.invalid/app",
		}
		for _, value := range unsafeValues {
			if err := validateCandidate("database.env", []byte(key+"="+value)); err == nil {
				t.Fatalf("%s 未拒绝数据库 userinfo 密码", key)
			}
		}
		for _, value := range []string{
			"postgres://db.example.invalid/app",
			"postgres://user@db.example.invalid/app",
			"postgres://user:$POSTGRES_PASSWORD@db.example.invalid/app",
			"postgres://db.example.invalid/app?password=$POSTGRES_PASSWORD",
			"$DATABASE_URL",
		} {
			if err := validateCandidate("database.env", []byte(key+"="+value)); err != nil {
				t.Fatalf("%s 无真实密码的 DSN 被拒绝: %v", key, err)
			}
		}
	}
}

func TestCandidateScannerChecksPostgresPasswordSettings(t *testing.T) {
	for _, key := range []string{"POSTGRES_" + "PASSWORD", "PG" + "PASSWORD"} {
		if err := validateCandidate("database.env", []byte(key+"=candidate-password")); err == nil {
			t.Fatalf("%s 未拒绝字面数据库口令", key)
		}
		for _, value := range []string{"$POSTGRES_PASSWORD", "validation-$VALIDATION_ID"} {
			if err := validateCandidate("database.env", []byte(key+"="+value)); err != nil {
				t.Fatalf("%s 运行期变量模板被误拒绝: %v", key, err)
			}
		}
	}
}

func TestCandidateScannerRejectsEncodedSensitiveSettingFormats(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	encodedKey := `\u0041PP_ADMIN_` + `TOKEN`
	for _, payload := range [][]byte{
		append([]byte{0xef, 0xbb, 0xbf}, []byte(key+"=candidate-secret-value")...),
		[]byte(`{"` + key + `":"candidate-secret-value"}`),
		[]byte(`{"` + encodedKey + `":"candidate-secret-value"}`),
	} {
		if err := validateCandidate("evaluation.json", payload); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("未拒绝编码敏感配置: %v", err)
		}
	}
}

func TestCandidateScannerAllowsUnavailableSensitiveSettings(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	for _, value := range []string{"", `""`, "unavailable", "<not-configured>", "[redacted]", "null",
		"$ADMIN_TOKEN", "${ADMIN_TOKEN}", "$env:ADMIN_TOKEN", "%ADMIN_TOKEN%"} {
		if err := validateCandidate("evaluation.env", []byte(key+"="+value)); err != nil {
			t.Fatalf("明确不可用占位值 %q 被拒绝: %v", value, err)
		}
	}
}

func TestCandidateScannerAcceptsCleanIndexWithoutUntrackedFiles(t *testing.T) {
	root := newGitRepository(t)
	writeFile(t, filepath.Join(root, "safe.txt"), "safe")
	runGit(t, root, "add", "safe.txt")
	if err := scanCandidateFiles(root); err != nil {
		t.Fatalf("干净暂存树被误拒绝: %v", err)
	}
}

func TestCandidateScannerFailsClosedForLargeOrBinaryFiles(t *testing.T) {
	largeRoot := newGitRepository(t)
	writeBytes(t, filepath.Join(largeRoot, "large.txt"), bytes.Repeat([]byte("a"), maxCandidateBytes+1))
	runGit(t, largeRoot, "add", "large.txt")
	if err := scanCandidateFiles(largeRoot); err == nil {
		t.Fatal("超量暂存 blob 未 fail-closed")
	}
	binaryRoot := newGitRepository(t)
	writeBytes(t, filepath.Join(binaryRoot, "binary.bin"), []byte{0xff, 0xfe})
	if err := scanCandidateFiles(binaryRoot); err == nil {
		t.Fatal("非 UTF-8 未跟踪文件未 fail-closed")
	}
}

func TestCandidateScannerRejectsNULAndUTF16Text(t *testing.T) {
	key := "APP_ADMIN_" + "TOKEN"
	utf16 := make([]byte, 0, len(key)*2+32)
	for _, value := range []byte(key + "=candidate-secret-value") {
		utf16 = append(utf16, value, 0)
	}
	for _, payload := range [][]byte{[]byte("safe\x00text"), utf16} {
		if err := validateCandidate("encoded.env", payload); err == nil {
			t.Fatal("含 NUL 的伪文本未 fail-closed")
		}
	}
}

func TestCandidateScannerRejectsPuttyPrivateKeys(t *testing.T) {
	marker := "PuTTY-" + "User-Key-File-3: ssh-ed25519\n" + "Private-" + "Lines: 1"
	if err := validateCandidate("deployment.txt", []byte(marker)); err == nil {
		t.Fatal("PuTTY 私钥内容未被拒绝")
	}
	if !forbiddenTrackedSecretPath("keys/deployment." + "ppk") {
		t.Fatal("PuTTY 私钥扩展名未被拒绝")
	}
}

func scanCandidateFiles(root string) error {
	tracked, err := stagedCandidates(root)
	if err != nil {
		return err
	}
	for _, item := range tracked {
		payload, readErr := readBlob(root, item.object)
		if readErr != nil {
			return fmt.Errorf("读取暂存文件 %q: %w", item.path, readErr)
		}
		if validateErr := validateCandidate(item.path, payload); validateErr != nil {
			return validateErr
		}
	}
	return scanUntrackedCandidates(root)
}

type stagedCandidate struct {
	path   string
	object string
}

func stagedCandidates(root string) ([]stagedCandidate, error) {
	payload, err := gitOutput(root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	result := make([]stagedCandidate, 0)
	for _, record := range splitNUL(payload) {
		meta, path, found := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !found || len(fields) != 3 || fields[2] != "0" {
			return nil, fmt.Errorf("暂存索引记录无效")
		}
		result = append(result, stagedCandidate{path: filepath.ToSlash(path), object: fields[1]})
	}
	return result, nil
}

func scanUntrackedCandidates(root string) error {
	payload, err := gitOutput(root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	for _, value := range splitNUL(payload) {
		path := filepath.ToSlash(string(value))
		content, readErr := readWorktreeCandidate(root, path)
		if readErr != nil {
			return readErr
		}
		if validateErr := validateCandidate(path, content); validateErr != nil {
			return validateErr
		}
	}
	return nil
}

func readBlob(root, object string) ([]byte, error) {
	sizePayload, err := gitOutput(root, "cat-file", "-s", object)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizePayload)), 10, 64)
	if err != nil || size > maxCandidateBytes {
		return nil, fmt.Errorf("候选 blob 超过 %d 字节或大小无效", maxCandidateBytes)
	}
	payload, err := gitOutput(root, "cat-file", "blob", object)
	if err != nil || int64(len(payload)) != size {
		return nil, fmt.Errorf("候选 blob 读取不完整")
	}
	return payload, nil
}

func readWorktreeCandidate(root, path string) ([]byte, error) {
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(fullPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCandidateBytes {
		return nil, fmt.Errorf("未跟踪候选文件 %q 无效或超量", path)
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxCandidateBytes+1))
}

func validateCandidate(path string, payload []byte) error {
	if forbiddenTrackedSecretPath(path) {
		return fmt.Errorf("Git 候选树包含敏感路径 %q", path)
	}
	if err := validateCandidatePayload(path, payload); err != nil {
		return err
	}
	if err := validateYAMLCandidate(path, payload); err != nil {
		return err
	}
	if containsSecretSignature(payload) {
		return fmt.Errorf("Git 候选文件 %q 命中高置信密钥特征", path)
	}
	if key := exposedSensitiveSetting(payload); key != "" {
		return fmt.Errorf("Git 候选文件 %q 包含非空敏感配置 %s", path, key)
	}
	return nil
}

type yamlScanBudget struct {
	nodes int
}

func validateYAMLCandidate(path string, payload []byte) error {
	if !yamlCandidatePath(path) {
		return nil
	}
	if len(payload) > maxYAMLCandidateBytes {
		return fmt.Errorf("Git 候选 YAML %q 超过 %d 字节", path, maxYAMLCandidateBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	for document := 1; ; document++ {
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil || document > maxYAMLDocuments {
			return fmt.Errorf("Git 候选 YAML %q 结构无效或文档过多", path)
		}
		budget := yamlScanBudget{}
		if err := scanYAMLNode(&node, 0, &budget); err != nil {
			return fmt.Errorf("Git 候选 YAML %q 不可安全审计: %w", path, err)
		}
	}
}

func yamlCandidatePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func scanYAMLNode(node *yaml.Node, depth int, budget *yamlScanBudget) error {
	budget.nodes++
	if budget.nodes > maxYAMLNodes || depth > maxYAMLDepth {
		return fmt.Errorf("YAML 节点或深度超过预算")
	}
	switch node.Kind {
	case 0:
		return nil
	case yaml.AliasNode:
		return fmt.Errorf("YAML alias 不允许进入候选树")
	case yaml.MappingNode:
		return scanYAMLMapping(node, depth, budget)
	case yaml.DocumentNode, yaml.SequenceNode:
		return scanYAMLChildren(node, depth, budget)
	case yaml.ScalarNode:
		return scanYAMLScalar(node)
	default:
		return fmt.Errorf("YAML 节点类型无效")
	}
}

func scanYAMLChildren(node *yaml.Node, depth int, budget *yamlScanBudget) error {
	for _, child := range node.Content {
		if err := scanYAMLNode(child, depth+1, budget); err != nil {
			return err
		}
	}
	return nil
}

func scanYAMLMapping(node *yaml.Node, depth int, budget *yamlScanBudget) error {
	if len(node.Content)%2 != 0 {
		return fmt.Errorf("YAML 映射字段不完整")
	}
	keys := make([]string, 0, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Kind != yaml.ScalarNode || equalFoldMember(keys, key.Value) {
			return fmt.Errorf("YAML 映射键无效、重复或大小写冲突")
		}
		keys = append(keys, key.Value)
		if err := scanYAMLNode(key, depth+1, budget); err != nil {
			return err
		}
		if setting, found := sensitiveSettingByName(key.Value); found && yamlSensitiveValueExposed(setting, value) {
			return fmt.Errorf("YAML 包含非空敏感配置 %s", setting.name)
		}
		if err := scanYAMLNode(value, depth+1, budget); err != nil {
			return err
		}
	}
	return scanYAMLNamedSetting(node)
}

func scanYAMLNamedSetting(node *yaml.Node) error {
	name, hasName := yamlMappingValue(node, "name")
	if !hasName || name.Kind != yaml.ScalarNode {
		return nil
	}
	setting, sensitive := sensitiveSettingByName(name.Value)
	value, hasValue := yamlMappingValue(node, "value")
	if sensitive && hasValue && yamlSensitiveValueExposed(setting, value) {
		return fmt.Errorf("YAML 包含非空敏感配置 %s", setting.name)
	}
	return nil
}

func yamlMappingValue(node *yaml.Node, name string) (*yaml.Node, bool) {
	for index := 0; index < len(node.Content); index += 2 {
		if strings.EqualFold(strings.TrimSpace(node.Content[index].Value), name) {
			return node.Content[index+1], true
		}
	}
	return nil, false
}

func yamlSensitiveValueExposed(setting sensitiveSetting, node *yaml.Node) bool {
	if node.Kind != yaml.ScalarNode {
		return true
	}
	if node.Tag == "!!null" {
		return false
	}
	return exposedSettingValue(setting, node.Value)
}

func scanYAMLScalar(node *yaml.Node) error {
	if len(node.Value) > maxYAMLScalarBytes || node.Tag == "!!binary" {
		return fmt.Errorf("YAML 标量超量或使用二进制标签")
	}
	if containsSecretSignature([]byte(node.Value)) {
		return fmt.Errorf("YAML 解码标量命中高置信密钥特征")
	}
	if key := exposedTextSetting(node.Value); key != "" {
		return fmt.Errorf("YAML 解码标量包含非空敏感配置 %s", key)
	}
	return nil
}

func validateCandidatePayload(path string, payload []byte) error {
	if len(payload) > maxCandidateBytes {
		return fmt.Errorf("Git 候选文件 %q 超过 %d 字节", path, maxCandidateBytes)
	}
	if approvedBinaryAsset(path, payload) {
		return nil
	}
	if !utf8.Valid(payload) || containsUnsafeTextControl(payload) {
		return fmt.Errorf("Git 候选文件 %q 不是安全 UTF-8 文本", path)
	}
	return nil
}

func containsUnsafeTextControl(payload []byte) bool {
	for _, value := range payload {
		if value == 0x7f || value < 0x20 && value != '\t' && value != '\n' && value != '\r' {
			return true
		}
	}
	return false
}

func approvedBinaryAsset(path string, payload []byte) bool {
	expected, exists := approvedBinaryDigests[filepath.ToSlash(path)]
	if !exists {
		return false
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest) == expected
}

var approvedBinaryDigests = map[string]string{
	"internal/adapters/http/webui/static/vendor/leaflet/images/layers-2x.png":      "066daca850d8ffbef007af00b06eac0015728dee279c51f3cb6c716df7c42edf",
	"internal/adapters/http/webui/static/vendor/leaflet/images/layers.png":         "1dbbe9d028e292f36fcba8f8b3a28d5e8932754fc2215b9ac69e4cdecf5107c6",
	"internal/adapters/http/webui/static/vendor/leaflet/images/marker-icon-2x.png": "00179c4c1ee830d3a108412ae0d294f55776cfeb085c60129a39aa6fc4ae2528",
	"internal/adapters/http/webui/static/vendor/leaflet/images/marker-icon.png":    "574c3a5cca85f4114085b6841596d62f00d7c892c7b03f28cbfa301deb1dc437",
	"internal/adapters/http/webui/static/vendor/leaflet/images/marker-shadow.png":  "264f5c640339f042dd729062cfc04c17f8ea0f29882b538e3848ed8f10edb4da",
}

func gitOutput(root string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", root}, args...)
	payload, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

func splitNUL(payload []byte) [][]byte {
	payload = bytes.TrimSuffix(payload, []byte{0})
	if len(payload) == 0 {
		return nil
	}
	return bytes.Split(payload, []byte{0})
}

func forbiddenTrackedSecretPath(path string) bool {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	if base == ".env.example" || base == ".env.evaluation.example" {
		return false
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	extension := filepath.Ext(base)
	return extension == ".pem" || extension == ".key" || extension == ".p12" || extension == ".ppk"
}

func containsSecretSignature(payload []byte) bool {
	text, ok := normalizedCandidateText(payload)
	if !ok {
		return true
	}
	if containsPrivateKeyHeader(text) || containsPuttyPrivateKey(text) {
		return true
	}
	return containsPrefixedToken(text, "github_"+"pat_", 20) ||
		containsPrefixedToken(text, "gh"+"p_", 20) ||
		containsPrefixedToken(text, "xo"+"xb-", 20) ||
		containsPrefixedToken(text, "AK"+"IA", 16) || containsOpenAIStyleToken(text)
}

func containsPuttyPrivateKey(text string) bool {
	upper := strings.ToUpper(text)
	return strings.Contains(upper, "PUTTY-"+"USER-KEY-FILE-") &&
		strings.Contains(upper, "PRIVATE-"+"LINES:")
}

func containsPrivateKeyHeader(text string) bool {
	upper := strings.ToUpper(text)
	marker := "-----" + "BEGIN "
	for offset := 0; ; {
		index := strings.Index(upper[offset:], marker)
		if index < 0 {
			return false
		}
		start := offset + index + len(marker)
		end := strings.Index(upper[start:], "---"+"--")
		if end >= 0 && strings.Contains(strings.TrimSpace(upper[start:start+end]), "PRIVATE KEY") {
			return true
		}
		offset = start
	}
}

func normalizedCandidateText(payload []byte) (string, bool) {
	return normalizedText(strings.TrimPrefix(string(payload), "\ufeff"))
}

func normalizedText(text string) (string, bool) {
	decoded := text
	total := len(text)
	for {
		next := decodeJSONEscapePass(decoded)
		if next == decoded {
			return text + "\n" + decoded, true
		}
		total += len(next)
		if total > maxCandidateDecodeBytes {
			return "", false
		}
		decoded = next
	}
}

func decodeJSONEscapePass(text string) string {
	var result strings.Builder
	result.Grow(len(text))
	for index := 0; index < len(text); index++ {
		if text[index] != '\\' || index+1 >= len(text) {
			result.WriteByte(text[index])
			continue
		}
		consumed, decoded, ok := decodeJSONEscape(text[index:])
		if !ok {
			result.WriteByte(text[index])
			continue
		}
		result.WriteRune(decoded)
		index += consumed - 1
	}
	return result.String()
}

func decodeJSONEscape(value string) (int, rune, bool) {
	if len(value) >= 6 && value[1] == 'u' {
		parsed, err := strconv.ParseUint(value[2:6], 16, 16)
		return 6, rune(parsed), err == nil
	}
	if len(value) < 2 {
		return 0, 0, false
	}
	switch value[1] {
	case '\\', '"', '/':
		return 2, rune(value[1]), true
	case 'n':
		return 2, '\n', true
	case 'r':
		return 2, '\r', true
	case 't':
		return 2, '\t', true
	default:
		return 0, 0, false
	}
}

func exposedSensitiveSetting(payload []byte) string {
	if key, parsed := exposedJSONSetting(payload); parsed {
		return key
	}
	text, ok := normalizedCandidateText(payload)
	if !ok {
		return "候选内容解码预算"
	}
	return exposedTextSetting(text)
}

func exposedTextSetting(text string) string {
	for _, setting := range sensitiveSettings {
		if containsExposedSetting(text, setting) || containsCommandSetting(text, setting) ||
			containsYAMLNamedSetting(text, setting) {
			return setting.name
		}
	}
	return ""
}

type sensitiveSetting struct {
	name                    string
	databaseURL             bool
	allowValidationTemplate bool
}

var sensitiveSettings = []sensitiveSetting{
	{name: "APP_ADMIN_" + "TOKEN"}, {name: "LLM_API_" + "KEY"}, {name: "BOCHA_API_" + "KEY"},
	{name: "AMAP_API_" + "KEY"}, {name: "AMAP_JS" + "CODE"}, {name: "OPEN_METEO_API_" + "KEY"},
	{name: "REDIS_" + "PASSWORD"}, {name: "TEST_REDIS_" + "PASSWORD"},
	{name: "DATABASE_" + "URL", databaseURL: true}, {name: "TEST_DATABASE_" + "URL", databaseURL: true},
	{name: "POSTGRES_" + "PASSWORD", allowValidationTemplate: true},
	{name: "PG" + "PASSWORD", allowValidationTemplate: true},
}

func exposedJSONSetting(payload []byte) (string, bool) {
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
	if err := rejectJSONKeyCollisions(payload); err != nil {
		if errors.Is(err, errJSONKeyCollision) {
			return "JSON 字段冲突", true
		}
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", false
	}
	return exposedJSONValue(value), true
}

func rejectJSONKeyCollisions(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("JSON 尾随内容无效")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return scanJSONObject(decoder)
	case '[':
		return scanJSONArray(decoder)
	default:
		return fmt.Errorf("JSON 结构无效")
	}
}

func scanJSONObject(decoder *json.Decoder) error {
	keys := make([]string, 0)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return fmt.Errorf("JSON 对象字段无效")
		}
		if equalFoldMember(keys, key) {
			return fmt.Errorf("%w: %s", errJSONKeyCollision, key)
		}
		keys = append(keys, key)
		if err := scanJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func scanJSONArray(decoder *json.Decoder) error {
	for decoder.More() {
		if err := scanJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func equalFoldMember(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}

func exposedJSONValue(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if key := exposedJSONMap(typed); key != "" {
			return key
		}
	case []any:
		for _, item := range typed {
			if key := exposedJSONValue(item); key != "" {
				return key
			}
		}
	case string:
		text, ok := normalizedText(typed)
		if !ok {
			return "候选内容解码预算"
		}
		return exposedTextSetting(text)
	}
	return ""
}

func exposedJSONMap(value map[string]any) string {
	if name, ok := jsonStringField(value, "name"); ok {
		if setting, found := sensitiveSettingByName(name); found {
			if item, exists := jsonField(value, "value"); exists && exposedJSONScalar(setting, item) {
				return setting.name
			}
		}
	}
	for key, item := range value {
		if setting, found := sensitiveSettingByName(key); found && exposedJSONScalar(setting, item) {
			return setting.name
		}
		if nested := exposedJSONValue(item); nested != "" {
			return nested
		}
	}
	return ""
}

func jsonStringField(value map[string]any, name string) (string, bool) {
	item, exists := jsonField(value, name)
	text, ok := item.(string)
	return text, exists && ok
}

func jsonField(value map[string]any, name string) (any, bool) {
	for key, item := range value {
		if strings.EqualFold(key, name) {
			return item, true
		}
	}
	return nil, false
}

func exposedJSONScalar(setting sensitiveSetting, value any) bool {
	if value == nil {
		return false
	}
	text, ok := value.(string)
	return !ok || exposedSettingValue(setting, text)
}

func sensitiveSettingByName(name string) (sensitiveSetting, bool) {
	for _, setting := range sensitiveSettings {
		if strings.EqualFold(strings.TrimSpace(name), setting.name) {
			return setting, true
		}
	}
	return sensitiveSetting{}, false
}

func containsExposedSetting(text string, setting sensitiveSetting) bool {
	for offset := 0; offset+len(setting.name) <= len(text); offset++ {
		if !asciiEqualFoldAt(text, setting.name, offset) || !settingNameBoundary(text, offset, len(setting.name)) {
			continue
		}
		value, ok := assignedScalar(text, offset, offset+len(setting.name))
		if ok && exposedSettingValue(setting, value) {
			return true
		}
	}
	return false
}

func containsCommandSetting(text string, setting sensitiveSetting) bool {
	for _, line := range logicalCommandLines(text) {
		if value, ok := powershellEnvironmentValue(line, setting.name); ok {
			return exposedSettingValue(setting, value)
		}
		fields := commandFields(line)
		if value, ok := commandSettingValue(fields, setting.name); ok && exposedSettingValue(setting, value) {
			return true
		}
	}
	return false
}

func logicalCommandLines(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		continued := strings.HasSuffix(trimmed, `\`)
		if continued {
			trimmed = strings.TrimSuffix(trimmed, `\`)
		}
		current += " " + strings.TrimSpace(trimmed)
		if !continued {
			result = append(result, strings.TrimSpace(current))
			current = ""
		}
	}
	if strings.TrimSpace(current) != "" {
		result = append(result, strings.TrimSpace(current))
	}
	return result
}

func commandFields(line string) []string {
	fields := strings.Fields(line)
	if len(fields) > 0 && strings.EqualFold(strings.Trim(fields[0], `"'`), "run") {
		fields = fields[1:]
	}
	if len(fields) > 0 && strings.EqualFold(strings.Trim(fields[0], `"'`), "sudo") {
		fields = skipCommandOptions(fields[1:])
	}
	if len(fields) > 2 && powershellCommand(fields[0]) && strings.EqualFold(fields[1], "-command") {
		fields = fields[2:]
	}
	return fields
}

func skipCommandOptions(fields []string) []string {
	for len(fields) > 0 && strings.HasPrefix(fields[0], "-") {
		fields = fields[1:]
	}
	return fields
}

func powershellCommand(value string) bool {
	value = strings.ToLower(strings.Trim(value, `"'`))
	return value == "powershell" || value == "powershell.exe" || value == "pwsh" || value == "pwsh.exe"
}

func commandSettingValue(fields []string, name string) (string, bool) {
	if len(fields) < 3 {
		return "", false
	}
	command := strings.ToLower(strings.Trim(fields[0], `"'`))
	if command == "env" || command == "setx" {
		return namedCommandValue(fields[1], fields[2], name)
	}
	if command == "set-item" {
		return powershellSetItemValue(fields[1:], name)
	}
	return "", false
}

func namedCommandValue(candidate, value, expected string) (string, bool) {
	candidate = strings.Trim(candidate, `"'`)
	return strings.Trim(value, `"'`), strings.EqualFold(candidate, expected)
}

func powershellSetItemValue(fields []string, name string) (string, bool) {
	pathIndex, valueIndex := 0, 1
	if len(fields) >= 4 && strings.EqualFold(fields[0], "-path") && strings.EqualFold(fields[2], "-value") {
		pathIndex, valueIndex = 1, 3
	}
	if pathIndex >= len(fields) || valueIndex >= len(fields) {
		return "", false
	}
	path := strings.Trim(fields[pathIndex], `"'`)
	prefix := "env:"
	if len(path) <= len(prefix) || !strings.EqualFold(path[:len(prefix)], prefix) {
		return "", false
	}
	return namedCommandValue(path[len(prefix):], fields[valueIndex], name)
}

func powershellEnvironmentValue(line, name string) (string, bool) {
	lower := strings.ToLower(line)
	marker := "[environment]::setenvironmentvariable"
	index := strings.Index(lower, marker)
	if index < 0 {
		return "", false
	}
	arguments, ok := parenthesizedArguments(line[index+len(marker):])
	if !ok || len(arguments) < 2 {
		return "", false
	}
	return namedCommandValue(arguments[0], arguments[1], name)
}

func parenthesizedArguments(value string) ([]string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '(' {
		return nil, false
	}
	end := strings.LastIndex(value, ")")
	if end < 1 {
		return nil, false
	}
	return splitQuotedArguments(value[1:end]), true
}

func splitQuotedArguments(value string) []string {
	result, start, quote := make([]string, 0, 2), 0, byte(0)
	for index := 0; index < len(value); index++ {
		if (value[index] == '"' || value[index] == '\'') && quote == 0 {
			quote = value[index]
		} else if value[index] == quote {
			quote = 0
		} else if value[index] == ',' && quote == 0 {
			result = append(result, strings.TrimSpace(value[start:index]))
			start = index + 1
		}
	}
	return append(result, strings.TrimSpace(value[start:]))
}

func containsYAMLNamedSetting(text string, setting sensitiveSetting) bool {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		name, ok := yamlFieldValue(line, "name")
		if !ok || !strings.EqualFold(name, setting.name) {
			continue
		}
		if value, found := nearbyYAMLValue(lines, index); found && exposedSettingValue(setting, value) {
			return true
		}
	}
	return false
}

func nearbyYAMLValue(lines []string, index int) (string, bool) {
	start, end := yamlItemBounds(lines, index)
	value, found := "", false
	for target := start; target < end; target++ {
		candidate, ok := yamlFieldValue(lines[target], "value")
		if !ok {
			continue
		}
		if found {
			return "重复 YAML value 字段", true
		}
		value, found = candidate, true
	}
	return value, found
}

func yamlItemBounds(lines []string, index int) (int, int) {
	start, base, found := index, yamlIndent(lines[index]), false
	for target := index; target >= 0; target-- {
		indent, listItem := yamlListItem(lines[target])
		if listItem && indent <= base {
			start, base, found = target, indent, true
			break
		}
	}
	end := len(lines)
	for target := index + 1; target < len(lines); target++ {
		indent, listItem := yamlListItem(lines[target])
		if found && listItem && indent <= base || !found && strings.TrimSpace(lines[target]) != "" && indent < base {
			end = target
			break
		}
	}
	return start, end
}

func yamlListItem(line string) (int, bool) {
	indent := yamlIndent(line)
	trimmed := strings.TrimSpace(line)
	return indent, trimmed == "-" || strings.HasPrefix(trimmed, "- ")
}

func yamlIndent(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

func yamlFieldValue(line, field string) (string, bool) {
	line = strings.TrimSpace(stripYAMLComment(line))
	if line == "-" {
		return "", false
	}
	if strings.HasPrefix(line, "- ") {
		line = strings.TrimSpace(line[2:])
	}
	name, value, found := strings.Cut(line, ":")
	if !found || !strings.EqualFold(strings.TrimSpace(name), field) {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(value), `"'`), true
}

func stripYAMLComment(value string) string {
	quote := byte(0)
	for index := 0; index < len(value); index++ {
		if (value[index] == '"' || value[index] == '\'') && quote == 0 {
			quote = value[index]
			continue
		}
		if value[index] == quote {
			quote = 0
			continue
		}
		if value[index] == '#' && quote == 0 && (index == 0 || value[index-1] == ' ' || value[index-1] == '\t') {
			return value[:index]
		}
	}
	return value
}

func asciiEqualFoldAt(text, key string, offset int) bool {
	for index := 0; index < len(key); index++ {
		left, right := text[offset+index], key[index]
		if left >= 'a' && left <= 'z' {
			left -= 'a' - 'A'
		}
		if left != right {
			return false
		}
	}
	return true
}

func settingNameBoundary(text string, offset, length int) bool {
	if offset > 0 && environmentNameCharacter(text[offset-1]) {
		return false
	}
	end := offset + length
	return end >= len(text) || !environmentNameCharacter(text[end])
}

func environmentNameCharacter(value byte) bool {
	return value == '_' || alphaNumeric(value)
}

func assignedScalar(text string, start, offset int) (string, bool) {
	outerQuote := byte(0)
	if start > 0 && (text[start-1] == '"' || text[start-1] == '\'') {
		outerQuote = text[start-1]
	}
	if offset < len(text) && (text[offset] == '"' || text[offset] == '\'') {
		offset++
	}
	offset = skipASCIISpace(text, offset)
	if offset >= len(text) || text[offset] != '=' && text[offset] != ':' {
		return "", false
	}
	operator := text[offset]
	if shellRequiredExpansion(text, start, operator, offset+1) {
		return "", false
	}
	if operator == ':' {
		outerQuote = 0
		offset = skipASCIISpace(text, offset+1)
	} else {
		offset = skipHorizontalSpace(text, offset+1)
	}
	return readAssignmentScalar(text, offset, operator, outerQuote), true
}

func shellRequiredExpansion(text string, start int, operator byte, valueOffset int) bool {
	return operator == ':' && start >= 2 && text[start-2:start] == "${" &&
		valueOffset < len(text) && text[valueOffset] == '?'
}

func skipASCIISpace(text string, offset int) int {
	for offset < len(text) && strings.ContainsRune(" \t\r\n", rune(text[offset])) {
		offset++
	}
	return offset
}

func skipHorizontalSpace(text string, offset int) int {
	for offset < len(text) && (text[offset] == ' ' || text[offset] == '\t') {
		offset++
	}
	return offset
}

func readAssignmentScalar(text string, offset int, operator, outerQuote byte) string {
	if offset >= len(text) || operator == '=' && strings.ContainsRune("\r\n", rune(text[offset])) {
		return ""
	}
	if text[offset] == '"' || text[offset] == '\'' {
		return readQuotedScalar(text, offset+1, text[offset])
	}
	if outerQuote != 0 {
		return readQuotedScalar(text, offset, outerQuote)
	}
	return readUnquotedScalar(text, offset, operator)
}

func readUnquotedScalar(text string, offset int, operator byte) string {
	if operator == ':' {
		return readYAMLUnquotedScalar(text, offset)
	}
	delimiters := " \t\r\n;"
	end := offset
	for end < len(text) && !strings.ContainsRune(delimiters, rune(text[end])) {
		end++
	}
	return strings.TrimSpace(text[offset:end])
}

func readYAMLUnquotedScalar(text string, offset int) string {
	end := strings.IndexAny(text[offset:], "\r\n")
	if end < 0 {
		end = len(text)
	} else {
		end += offset
	}
	return strings.TrimSpace(stripYAMLComment(text[offset:end]))
}

func readQuotedScalar(text string, offset int, quote byte) string {
	start, escaped := offset, false
	for offset < len(text) {
		if text[offset] == quote && !escaped {
			return text[start:offset]
		}
		if text[offset] == '\\' && !escaped {
			escaped, offset = true, offset+1
			continue
		}
		escaped, offset = false, offset+1
	}
	return text[start:]
}

func exposedSettingValue(setting sensitiveSetting, value string) bool {
	if unavailableSecretValue(value) || pureVariableReference(value) {
		return false
	}
	if setting.allowValidationTemplate && validationPasswordTemplate(value) {
		return false
	}
	if setting.databaseURL {
		return databaseURLPasswordExposed(value)
	}
	return true
}

func validationPasswordTemplate(value string) bool {
	const prefix = "validation-"
	return strings.HasPrefix(value, prefix) && pureVariableReference(value[len(prefix):])
}

func databaseURLPasswordExposed(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || !validPostgresURL(parsed) {
		return true
	}
	if parsed.User != nil {
		password, exists := parsed.User.Password()
		if exists && password != "" && !unavailableSecretValue(password) && !pureVariableReference(password) {
			return true
		}
	}
	return databaseQueryCredentialExposed(parsed.Query())
}

func validPostgresURL(parsed *url.URL) bool {
	scheme := strings.ToLower(parsed.Scheme)
	return (scheme == "postgres" || scheme == "postgresql") && parsed.Host != ""
}

func databaseQueryCredentialExposed(values url.Values) bool {
	for key, candidates := range values {
		if !databaseCredentialParameter(key) {
			continue
		}
		for _, value := range candidates {
			if !unavailableSecretValue(value) && !pureVariableReference(value) {
				return true
			}
		}
	}
	return false
}

func databaseCredentialParameter(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "password", "pass", "passwd", "sslpassword", "token", "access_token", "secret":
		return true
	default:
		return false
	}
}

func pureVariableReference(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) > 5 && strings.EqualFold(value[:5], "$env:") {
		return validVariableName(value[5:])
	}
	if len(value) > 3 && strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		return validVariableName(value[2 : len(value)-1])
	}
	if len(value) > 2 && value[0] == '$' {
		return validVariableName(value[1:])
	}
	if len(value) > 2 && value[0] == '%' && value[len(value)-1] == '%' {
		return validVariableName(value[1 : len(value)-1])
	}
	return false
}

func validVariableName(value string) bool {
	if value == "" || value[0] != '_' && !asciiLetter(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !environmentNameCharacter(value[index]) {
			return false
		}
	}
	return true
}

func asciiLetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func unavailableSecretValue(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "null", "~", "unavailable", "not-configured", "<unavailable>", "<not-configured>",
		"disabled", "redacted", "[redacted]", "<redacted>":
		return true
	default:
		return false
	}
}

func containsOpenAIStyleToken(text string) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], "sk"+"-")
		if index < 0 {
			return false
		}
		index += offset + 3
		count := countTokenCharacters(text[index:])
		if count >= 20 {
			return true
		}
		offset = index + count
	}
}

func containsPrefixedToken(text, prefix string, minimum int) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], prefix)
		if index < 0 {
			return false
		}
		index += offset + len(prefix)
		count := countTokenCharacters(text[index:])
		if count >= minimum {
			return true
		}
		offset = index + count
	}
}

func countTokenCharacters(text string) int {
	count := 0
	for count < len(text) && tokenCharacter(text[count]) {
		count++
	}
	return count
}

func alphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func tokenCharacter(value byte) bool {
	return alphaNumeric(value) || value == '_' || value == '-'
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "security-test")
	runGit(t, root, "config", "user.email", "security@example.invalid")
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	if payload, err := gitOutput(root, args...); err != nil {
		t.Fatalf("%v: %s", err, payload)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	writeBytes(t, path, []byte(content))
}

func writeBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
