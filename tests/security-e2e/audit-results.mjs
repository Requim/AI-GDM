import { readFileSync } from "node:fs";

const [manifestPath, reportPath] = process.argv.slice(2);
if (!manifestPath || !reportPath) fail("用法: node audit-results.mjs <expected-tests.json> <report.json>");
const expected = readManifest(manifestPath);
const report = readJSON(reportPath, "Playwright 报告");
const specs = [];
collect(Array.isArray(report.suites) ? report.suites : [], specs);
validateTopLevel(report);
validateInventory(specs);
for (const spec of specs) validateSpec(spec);
const stats = report.stats || {};
if (stats.expected !== expected.size || stats.unexpected !== 0 || stats.skipped !== 0 || stats.flaky !== 0) {
  fail("Playwright 汇总不满足全绿门禁", stats);
}
process.stdout.write(`安全浏览器结果审计通过：${expected.size}/${expected.size}\n`);

function readManifest(path) {
  const manifest = readJSON(path, "安全浏览器预期清单");
  if (manifest.version !== 1 || !Array.isArray(manifest.tests) || manifest.tests.length === 0) {
    fail("安全浏览器预期清单 schema 无效", manifest);
  }
  const result = new Set();
  for (const item of manifest.tests) {
    const key = identity(item?.project, item?.file, item?.title);
    if (result.has(key)) fail("安全浏览器预期清单身份重复", key);
    result.add(key);
  }
  return result;
}

function readJSON(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    fail(`${label} 无法读取或不是合法 JSON`, error.message);
  }
}

function validateTopLevel(value) {
  if (!Array.isArray(value.errors) || value.errors.length !== 0) {
    fail("Playwright 报告包含顶层错误", value.errors);
  }
}

function validateInventory(values) {
  const actual = new Set();
  for (const spec of values) {
    const key = specIdentity(spec);
    if (actual.has(key)) {
      fail("浏览器安全场景身份重复或无效", key);
    }
    actual.add(key);
  }
  const missing = [...expected].filter((key) => !actual.has(key));
  const extra = [...actual].filter((key) => !expected.has(key));
  if (missing.length !== 0 || extra.length !== 0) {
    fail("浏览器安全场景清单漂移", { missing, extra });
  }
}

function specIdentity(spec) {
  const tests = Array.isArray(spec?.tests) ? spec.tests : [];
  if (tests.length !== 1) fail("场景没有唯一测试身份", spec?.title);
  const project = tests[0].projectName === undefined ? "" : tests[0].projectName;
  return identity(project, spec.file, spec.title);
}

function identity(project, file, title) {
  const normalizedFile = typeof file === "string" ? file.replaceAll("\\", "/") : "";
  if (typeof project !== "string" || !normalizedFile || typeof title !== "string" || !title ||
      project.includes("\0") || normalizedFile.includes("\0") || title.includes("\0")) {
    fail("浏览器安全场景身份字段无效", { project, file, title });
  }
  return `${project}\0${normalizedFile}\0${title}`;
}

function validateSpec(spec) {
  if (spec.ok !== true || !Array.isArray(spec.tests) || spec.tests.length !== 1) {
    fail("场景没有唯一成功测试", spec.title);
  }
  const test = spec.tests[0];
  validateTestStatus(spec.title, test);
  const results = Array.isArray(test.results) ? test.results : [];
  if (results.length !== 1 || results[0]?.status !== "passed" ||
      !Number.isInteger(results[0]?.retry) || results[0].retry !== 0) {
    fail("场景包含重试或未唯一通过", spec.title);
  }
  validateResultErrors(spec.title, results[0]);
}

function validateTestStatus(title, test) {
  validateAnnotations(title, test.annotations, "test");
  if (test.expectedStatus !== "passed" || test.status !== "expected") {
    fail("场景期望或实际状态漂移", title);
  }
}

function validateResultErrors(title, result) {
  validateAnnotations(title, result.annotations, "result");
  if (result.error !== undefined) {
    fail("场景通过结果包含 error", title);
  }
  if (!Array.isArray(result.errors) || result.errors.length !== 0) {
    fail("场景通过结果包含 errors", title);
  }
}

function validateAnnotations(title, value, scope) {
  if (!Array.isArray(value)) fail(`场景 ${scope} annotations 结构无效`, title);
  if (value.length !== 0) fail(`场景 ${scope} annotations 必须为空`, title);
}

function collect(suites, target) {
  for (const suite of suites) {
    target.push(...(suite.specs || []));
    collect(suite.suites || [], target);
  }
}

function fail(message, detail) {
  process.stderr.write(`${message}: ${JSON.stringify(detail)}\n`);
  process.exit(1);
}
