import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const titles = [
  "管理员令牌只驻留当前页面内存并通过服务端写请求边界",
  "非法令牌和跨站接口在客户端 fail-closed",
  "脚本阻断或禁用时授权控件不会把令牌写入 URL",
  "401 响应体超量或中断时立即清除内存令牌",
  "管理员授权控件在手机和桌面视口不产生横向溢出"
];
const directory = mkdtempSync(join(tmpdir(), "ai-gdm-security-audit-"));
const validator = fileURLToPath(new URL("./audit-results.mjs", import.meta.url));
const manifest = fileURLToPath(new URL("./expected-tests.json", import.meta.url));

try {
  assertAudit(baseReport(), true, "精确通过报告");
  for (const scenario of ["top-error", "spec-not-ok", "expected-status", "test-status",
    "skip", "fixme", "slow", "custom", "annotations-shape", "retry", "retry-marker", "retry-missing",
    "result-status", "result-error", "result-errors", "result-errors-missing", "result-skip",
    "result-slow", "result-custom", "result-annotations-shape", "unreviewed-file",
    "unreviewed-project", "duplicate", "extra", "missing", "stats"]) {
    const report = baseReport();
    mutate(report, scenario);
    assertAudit(report, false, scenario);
  }
  process.stdout.write("安全浏览器结果审计行为测试通过\n");
} finally {
  rmSync(directory, { recursive: true, force: true });
}

function baseReport() {
  return {
    suites: [{ specs: titles.map((title) => spec(title)) }], errors: [],
    stats: { expected: titles.length, unexpected: 0, flaky: 0, skipped: 0 }
  };
}

function spec(title) {
  return { file: "security.spec.js", title, ok: true,
    tests: [{ projectName: "", expectedStatus: "passed", status: "expected",
      annotations: [], results: [{ status: "passed", retry: 0, errors: [], annotations: [] }] }] };
}

function mutate(report, scenario) {
  const specs = report.suites[0].specs;
  const target = specs[0];
  const test = target.tests[0];
  const result = test.results[0];
  if (scenario === "top-error") report.errors = [{ message: "boom" }];
  if (scenario === "spec-not-ok") target.ok = false;
  if (scenario === "expected-status") test.expectedStatus = "skipped";
  if (scenario === "test-status") test.status = "unexpected";
  if (scenario === "skip" || scenario === "fixme") test.annotations = [{ type: scenario }];
  if (scenario === "slow") test.annotations = [{ type: "slow" }];
  if (scenario === "custom") test.annotations = [{ type: "security-review", description: "qa" }];
  if (scenario === "annotations-shape") test.annotations = { type: "skip" };
  if (scenario === "retry") test.results.push({ status: "passed", retry: 1 });
  if (scenario === "retry-marker") result.retry = 1;
  if (scenario === "retry-missing") delete result.retry;
  if (scenario === "result-status") result.status = "failed";
  if (scenario === "result-error") result.error = { message: "boom" };
  if (scenario === "result-errors") result.errors = [{ message: "boom" }];
  if (scenario === "result-errors-missing") delete result.errors;
  if (scenario === "result-skip") result.annotations = [{ type: "skip" }];
  if (scenario === "result-slow") result.annotations = [{ type: "slow" }];
  if (scenario === "result-custom") result.annotations = [{ type: "security-review" }];
  if (scenario === "result-annotations-shape") result.annotations = { type: "fixme" };
  if (scenario === "unreviewed-file") target.file = "unreviewed.spec.js";
  if (scenario === "unreviewed-project") test.projectName = "unreviewed";
  if (scenario === "duplicate") specs.push(spec(titles[0]));
  if (scenario === "extra") specs.push(spec("未审计场景"));
  if (scenario === "missing") specs.pop();
  if (scenario === "stats") report.stats.flaky = 1;
}

function assertAudit(report, success, label) {
  const path = join(directory, `${label}.json`);
  writeFileSync(path, JSON.stringify(report));
  const result = spawnSync(process.execPath, [validator, manifest, path], { encoding: "utf8" });
  if ((result.status === 0) !== success) {
    throw new Error(`${label} 审计结果错误: status=${result.status} stdout=${result.stdout} stderr=${result.stderr}`);
  }
}
