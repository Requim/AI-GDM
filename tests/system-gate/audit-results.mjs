import { readFileSync } from "node:fs";

const stableLimitations = [
  { id: "amap-route", state: "degraded", reasonCode: "candidate_routes_do_not_confirm_road_open" }
];

const [manifestPath, reportPath, expectedTree, expectedSource] = process.argv.slice(2);
if (!manifestPath || !reportPath || !expectedTree || !expectedSource) {
  fail("用法: node audit-results.mjs <manifest.json> <report.json> <tree> <source-sha256>");
}

validateSHA(expectedTree, 40, "外部 tree SHA");
validateSHA(expectedSource, 64, "外部 source SHA-256");
const manifest = readJSON(manifestPath, "系统门禁 manifest");
const report = readJSON(reportPath, "系统门禁报告");
const expectedGates = validateManifest(manifest);
validateReport(report, expectedGates, manifest.limitations);
process.stdout.write(`P9.3 系统门禁结果审计通过：${expectedGates.length}/${expectedGates.length}\n`);

function readJSON(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    fail(`${label} 无法读取或不是合法 JSON`, error.message);
  }
}

function validateManifest(value) {
  exactKeys(value, ["version", "gates", "limitations"], "manifest");
  if (value.version !== 1 || !Array.isArray(value.gates) || value.gates.length === 0) {
    fail("系统门禁 manifest schema 无效", value);
  }
  const ids = new Set();
  for (const gate of value.gates) {
    validateManifestGate(gate);
    if (ids.has(gate.id)) fail("系统门禁 manifest 包含重复 gate", gate.id);
    ids.add(gate.id);
  }
  validateLimitations(value.limitations, "manifest");
  if (JSON.stringify(value.limitations) !== JSON.stringify(stableLimitations)) {
    fail("系统门禁稳定限制漂移", value.limitations);
  }
  return value.gates;
}

function validateManifestGate(gate) {
  const hasLiveEnv = Object.hasOwn(gate, "liveEnv");
  const hasSentinel = Object.hasOwn(gate, "passCount") || Object.hasOwn(gate, "auditMarker");
  const hasDegradedCode = Object.hasOwn(gate, "degradedExitCode");
  const hasDegradedMarker = Object.hasOwn(gate, "degradedMarker");
  const hasDegradedLimitation = Object.hasOwn(gate, "degradedLimitation");
  const keys = ["id", "script", "mode"];
  if (hasLiveEnv) keys.push("liveEnv");
  if (hasSentinel) keys.push("passCount", "auditMarker");
  if (hasDegradedCode || hasDegradedMarker || hasDegradedLimitation) {
    keys.push("degradedExitCode", "degradedMarker", "degradedLimitation");
  }
  exactKeys(gate, keys, "manifest gate");
  if (!boundedName(gate.id) || typeof gate.script !== "string" ||
      !/^scripts\/validate-[a-z0-9-]+\.sh$/.test(gate.script)) {
    fail("系统门禁 manifest gate 身份无效", gate);
  }
  if (gate.mode !== "required" && gate.mode !== "live") fail("系统门禁 mode 无效", gate);
  if (gate.mode === "live" && !hasLiveEnv) fail("live gate 缺少启用变量", gate);
  if (hasLiveEnv && !/^AI_GDM_SYSTEM_LIVE_[A-Z_]+$/.test(gate.liveEnv)) {
    fail("系统门禁 liveEnv 无效", gate);
  }
  if (hasSentinel && (!Number.isInteger(gate.passCount) || gate.passCount <= 0 ||
      typeof gate.auditMarker !== "string" || !gate.auditMarker || gate.auditMarker.length > 256)) {
    fail("系统门禁浏览器 sentinel 无效", gate);
  }
  if (hasDegradedCode !== hasDegradedMarker || hasDegradedCode !== hasDegradedLimitation ||
      (hasDegradedCode &&
      (gate.mode !== "live" || !Number.isInteger(gate.degradedExitCode) ||
       gate.degradedExitCode <= 0 || gate.degradedExitCode > 125 ||
       typeof gate.degradedMarker !== "string" || !gate.degradedMarker ||
       gate.degradedMarker.length > 256))) {
    fail("系统门禁降级合同无效", gate);
  }
  if (hasDegradedLimitation) validateLimitations([gate.degradedLimitation], `gate ${gate.id}`);
}

function validateReport(report, expectedGates, expectedLimitations) {
  exactKeys(report, ["version", "treeSha", "sourceSha256", "gates", "limitations", "cleanup"], "报告");
  if (report.version !== 1 || report.treeSha !== expectedTree || report.sourceSha256 !== expectedSource) {
    fail("系统门禁报告 tree/source 漂移", report);
  }
  if (!Array.isArray(report.gates)) fail("系统门禁报告 gates 无效", report.gates);
  validateGateInventory(expectedGates, report.gates);
  validateLimitations(report.limitations, "报告");
  const requiredLimitations = expectedReportLimitations(expectedGates, expectedLimitations, report.gates);
  if (JSON.stringify(report.limitations) !== JSON.stringify(requiredLimitations)) {
    fail("系统门禁降级限制漂移", report.limitations);
  }
  validateCleanup(report.cleanup);
}

function expectedReportLimitations(expectedGates, baseLimitations, actualGates) {
  const result = structuredClone(baseLimitations);
  for (const expected of expectedGates) {
    if (!expected.degradedLimitation) continue;
    const actual = actualGates.find((gate) => gate.id === expected.id);
    if (actual?.outcome === "degraded") result.push(structuredClone(expected.degradedLimitation));
  }
  return result;
}

function validateGateInventory(expected, actual) {
  const actualByID = uniqueGates(actual);
  const expectedIDs = expected.map((gate) => gate.id);
  const actualIDs = actual.map((gate) => gate?.id);
  const missing = expectedIDs.filter((id) => !actualByID.has(id));
  const extra = actualIDs.filter((id) => !expectedIDs.includes(id));
  if (missing.length || extra.length || JSON.stringify(expectedIDs) !== JSON.stringify(actualIDs)) {
    fail("系统门禁 gate 清单漂移", { missing, extra, expectedIDs, actualIDs });
  }
  for (const gate of expected) validateGateResult(gate, actualByID.get(gate.id));
}

function uniqueGates(values) {
  const result = new Map();
  for (const gate of values) {
    if (!gate || typeof gate.id !== "string") fail("系统门禁报告 gate 身份无效", gate);
    if (result.has(gate.id)) fail("系统门禁报告包含重复 gate", gate.id);
    result.set(gate.id, gate);
  }
  return result;
}

function validateGateResult(expected, actual) {
  const keys = ["id", "script", "mode", "liveEnv", "liveEnabled", "outcome", "exitCode",
    "passCount", "auditMarker", "sentinelVerified", "logSha256", "durationMs", "treeSha", "sourceSha256"];
  exactKeys(actual, keys, `gate ${expected.id}`);
  const liveEnv = expected.liveEnv || "";
  if (actual.script !== expected.script || actual.mode !== expected.mode || actual.liveEnv !== liveEnv) {
    fail("系统门禁 gate 合同漂移", { expected, actual });
  }
  const passCount = expected.passCount || null;
  const auditMarker = actual.outcome === "degraded" ? expected.degradedMarker : expected.auditMarker || "";
  if (actual.passCount !== passCount || actual.auditMarker !== auditMarker) {
    fail("系统门禁浏览器 sentinel 合同漂移", { expected, actual });
  }
  if (actual.treeSha !== expectedTree || actual.sourceSha256 !== expectedSource) {
    fail("系统门禁 gate tree/source 漂移", actual);
  }
  validateLogEvidence(actual, passCount !== null || actual.outcome === "degraded");
  validateGateOutcome(expected, actual);
}

function validateLogEvidence(actual, requiresSentinel) {
  validateSHA(actual.logSha256, 64, `gate ${actual.id} 日志 SHA-256`);
  if (!Number.isInteger(actual.durationMs) || actual.durationMs < 0) {
    fail("系统门禁耗时无效", actual);
  }
  if (typeof actual.sentinelVerified !== "boolean" ||
      actual.sentinelVerified !== requiresSentinel) {
    fail("系统门禁 sentinel 审计状态无效", actual);
  }
}

function validateGateOutcome(expected, actual) {
  if (typeof actual.liveEnabled !== "boolean") fail("系统门禁 liveEnabled 无效", actual);
  if (!expected.liveEnv && actual.liveEnabled) fail("非 live gate 被标记为实时", actual);
  if (expected.mode === "live" && !actual.liveEnabled) {
    if (actual.outcome !== "disabled" || actual.exitCode !== null || actual.sentinelVerified) {
      fail("禁用 live gate 结果无效", actual);
    }
    return;
  }
  if (actual.outcome === "degraded") {
    if (expected.mode !== "live" || actual.exitCode !== expected.degradedExitCode ||
        !actual.sentinelVerified) {
      fail("系统门禁降级结果无效", actual);
    }
    return;
  }
  if (actual.outcome !== "passed" || actual.exitCode !== 0) fail("系统门禁未唯一通过", actual);
}

function validateLimitations(values, scope) {
  if (!Array.isArray(values) || values.length === 0) fail(`系统门禁 ${scope} 降级限制无效`, values);
  const ids = new Set();
  for (const value of values) {
    exactKeys(value, ["id", "state", "reasonCode"], `${scope} limitation`);
    if (!boundedName(value.id) || value.state !== "degraded" || !boundedName(value.reasonCode)) {
      fail(`系统门禁 ${scope} 降级限制字段无效`, value);
    }
    if (ids.has(value.id)) fail(`系统门禁 ${scope} 降级限制重复`, value.id);
    ids.add(value.id);
  }
}

function validateCleanup(value) {
  exactKeys(value, ["containers", "networks"], "cleanup");
  if (!Number.isInteger(value.containers) || !Number.isInteger(value.networks) ||
      value.containers !== 0 || value.networks !== 0) {
    fail("系统门禁存在 ai-gdm 容器或网络遗留", value);
  }
}

function exactKeys(value, expected, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) fail(`${label} 必须是对象`, value);
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(wanted)) fail(`${label} 字段集合漂移`, { actual, wanted });
}

function boundedName(value) {
  return typeof value === "string" && /^[a-z][a-z0-9_-]{0,63}$/.test(value);
}

function validateSHA(value, length, label) {
  if (typeof value !== "string" || value.length !== length || !/^[0-9a-f]+$/.test(value)) {
    fail(`${label} 无效`, value);
  }
}

function fail(message, detail) {
  process.stderr.write(`${message}: ${JSON.stringify(detail)}\n`);
  process.exit(1);
}
