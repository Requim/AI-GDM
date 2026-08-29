import fs from "node:fs";

function fail(message) {
  console.error(message);
  process.exit(1);
}

function readJSON(path, label) {
  try {
    return JSON.parse(fs.readFileSync(path, "utf8"));
  } catch (error) {
    fail(`${label} 无法读取或不是合法 JSON: ${error.message}`);
  }
}

function identity(value) {
  const project = value.project === undefined ? "" : value.project;
  const file = typeof value.file === "string" ? value.file.replaceAll("\\", "/") : "";
  const title = typeof value.title === "string" ? value.title : "";
  if (typeof project !== "string" || !file || !title || project.includes("\0") ||
      file.includes("\0") || title.includes("\0")) {
    fail("评估界面测试清单包含无效身份字段");
  }
  return `${project}\0${file}\0${title}`;
}

function collectTests(suites, result = []) {
  for (const suite of suites || []) {
    for (const spec of suite.specs || []) {
      for (const test of spec.tests || []) {
        result.push({
          key: identity({ project: test.projectName || "", file: spec.file, title: spec.title }),
          spec,
          test
        });
      }
    }
    collectTests(suite.suites, result);
  }
  return result;
}

function uniqueMap(values, label) {
  const result = new Map();
  for (const value of values) {
    const key = typeof value === "string" ? value : value.key;
    if (result.has(key)) {
      fail(`${label} 包含重复测试身份: ${key.replaceAll("\0", " | ")}`);
    }
    result.set(key, value);
  }
  return result;
}

function addSetDifferences(errors, expected, actual) {
  const missing = [...expected.keys()].filter((key) => !actual.has(key));
  const extra = [...actual.keys()].filter((key) => !expected.has(key));
  if (missing.length > 0) {
    errors.push(`缺少受审计场景: ${missing.map((key) => key.replaceAll("\0", " | ")).join(", ")}`);
  }
  if (extra.length > 0) {
    errors.push(`出现未审计场景: ${extra.map((key) => key.replaceAll("\0", " | ")).join(", ")}`);
  }
}

function validateTest(record, errors) {
  const label = record.key.replaceAll("\0", " | ");
  const annotations = Array.isArray(record.test.annotations) ? record.test.annotations : [];
  if (annotations.some((item) => item?.type === "skip" || item?.type === "fixme")) {
    errors.push(`${label} 包含 skip/fixme`);
  }
  if (record.test.expectedStatus !== "passed" || record.test.status !== "expected") {
    errors.push(`${label} 状态不是预期通过`);
  }
  const results = Array.isArray(record.test.results) ? record.test.results : [];
  if (results.length !== 1 || results[0]?.status !== "passed" || record.spec.ok !== true) {
    errors.push(`${label} 没有且仅有一次 passed 结果`);
  }
}

const [manifestPath, reportPath] = process.argv.slice(2);
if (!manifestPath || !reportPath) {
  fail("用法: node validate-results.mjs <expected-tests.json> <playwright-results.json>");
}

const manifest = readJSON(manifestPath, "评估界面预期清单");
const report = readJSON(reportPath, "Playwright 结果");
if (manifest.version !== 1 || !Array.isArray(manifest.tests) || manifest.tests.length === 0) {
  fail("评估界面预期清单 schema 无效");
}

const expected = uniqueMap(manifest.tests.map((item) => identity(item)), "评估界面预期清单");
const records = collectTests(report.suites);
const actual = uniqueMap(records, "Playwright 结果");
const errors = [];
addSetDifferences(errors, expected, actual);

const stats = report.stats || {};
if (!Array.isArray(report.errors) || report.errors.length !== 0) {
  errors.push("Playwright 报告包含顶层错误");
}
if (stats.unexpected !== 0 || stats.flaky !== 0 || stats.skipped !== 0 || stats.expected !== expected.size) {
  errors.push(`统计不匹配: passed=${stats.expected} failed=${stats.unexpected} flaky=${stats.flaky} skipped=${stats.skipped}`);
}
for (const record of records) {
  validateTest(record, errors);
}

if (errors.length > 0) {
  fail(`评估界面 Playwright 结果审计失败:\n- ${errors.join("\n- ")}`);
}
console.log(`评估界面 Playwright 结果审计通过: passed=${expected.size} failed=0 skipped=0`);
