import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const directory = mkdtempSync(join(tmpdir(), "ai-gdm-system-audit-"));
const validator = fileURLToPath(new URL("./audit-results.mjs", import.meta.url));
const manifestPath = fileURLToPath(new URL("./manifest.json", import.meta.url));
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
const tree = "a".repeat(40);
const source = "b".repeat(64);

try {
  assertAudit(baseReport(), manifest, true, "exact");
  assertAudit(enabledLiveReport(), manifest, true, "enabled-live");
  assertAudit(enabledNamedLiveReport("live-amap"), manifest, true, "enabled-live-amap");
  assertAudit(enabledDegradedLLMReport(), manifest, true, "enabled-degraded-llm");
  for (const scenario of scenarios()) {
    const report = baseReport();
    scenario.mutate(report);
    assertAudit(report, manifest, false, scenario.name);
  }
  const duplicateManifest = structuredClone(manifest);
  duplicateManifest.gates.push(structuredClone(duplicateManifest.gates[0]));
  assertAudit(baseReport(), duplicateManifest, false, "manifest-duplicate");
  process.stdout.write("P9.3 系统门禁结果审计行为测试通过\n");
} finally {
  rmSync(directory, { recursive: true, force: true });
}

function enabledNamedLiveReport(id) {
  const report = baseReport();
  const gate = report.gates.find((value) => value.id === id);
  if (!gate || gate.mode !== "live") throw new Error(`缺少 live gate: ${id}`);
  gate.liveEnabled = true;
  gate.outcome = "passed";
  gate.exitCode = 0;
  gate.durationMs = 1000;
  return report;
}

function enabledDegradedLLMReport() {
  const report = baseReport();
  markLLMDegraded(report);
  return report;
}

function baseReport() {
  return {
    version: 1,
    treeSha: tree,
    sourceSha256: source,
    gates: manifest.gates.map((gate) => gateResult(gate, false)),
    limitations: structuredClone(manifest.limitations),
    cleanup: { containers: 0, networks: 0 }
  };
}

function enabledLiveReport() {
  const report = baseReport();
  for (const gate of report.gates) {
    if (gate.liveEnv) gate.liveEnabled = true;
    if (gate.mode === "live") {
      gate.outcome = "passed";
      gate.exitCode = 0;
    }
  }
  return report;
}

function gateResult(gate, enabled) {
  const disabled = gate.mode === "live" && !enabled;
  return {
    id: gate.id,
    script: gate.script,
    mode: gate.mode,
    liveEnv: gate.liveEnv || "",
    liveEnabled: enabled,
    outcome: disabled ? "disabled" : "passed",
    exitCode: disabled ? null : 0,
    passCount: gate.passCount || null,
    auditMarker: gate.auditMarker || "",
    sentinelVerified: Boolean(gate.passCount),
    logSha256: "c".repeat(64),
    durationMs: disabled ? 0 : 1000,
    treeSha: tree,
    sourceSha256: source
  };
}

function scenarios() {
  return [
    { name: "missing", mutate: (value) => value.gates.pop() },
    { name: "extra", mutate: addExtraGate },
    { name: "duplicate", mutate: (value) => value.gates.push(structuredClone(value.gates[0])) },
    { name: "order", mutate: (value) => value.gates.reverse() },
    { name: "failed", mutate: markFailed },
    { name: "top-tree", mutate: (value) => { value.treeSha = "c".repeat(40); } },
    { name: "top-source", mutate: (value) => { value.sourceSha256 = "d".repeat(64); } },
    { name: "gate-tree", mutate: (value) => { value.gates[0].treeSha = "c".repeat(40); } },
    { name: "gate-source", mutate: (value) => { value.gates[0].sourceSha256 = "d".repeat(64); } },
    { name: "container-leftover", mutate: (value) => { value.cleanup.containers = 1; } },
    { name: "network-leftover", mutate: (value) => { value.cleanup.networks = 1; } },
    { name: "required-disabled", mutate: disableRequired },
    { name: "live-disabled-passed", mutate: passDisabledLive },
    { name: "live-enabled-disabled", mutate: enableDisabledLive },
    { name: "degraded-exit-code", mutate: corruptDegradedExitCode },
    { name: "degraded-marker", mutate: corruptDegradedMarker },
    { name: "degraded-sentinel", mutate: clearDegradedSentinel },
    { name: "degraded-limitation-missing", mutate: removeDegradedLimitation },
    { name: "passed-with-degraded-limitation", mutate: addDegradedLimitationWithoutOutcome },
    { name: "required-degraded", mutate: degradeRequiredGate },
    { name: "script-drift", mutate: (value) => { value.gates[0].script = "scripts/validate-security.sh"; } },
    { name: "pass-count-drift", mutate: changePassCount },
    { name: "sentinel-missing", mutate: clearSentinel },
    { name: "log-sha", mutate: (value) => { value.gates[0].logSha256 = "bad"; } },
    { name: "duration", mutate: (value) => { value.gates[0].durationMs = -1; } },
    { name: "extra-field", mutate: (value) => { value.gates[0].requestId = "secret"; } },
    { name: "limitation-missing", mutate: (value) => value.limitations.pop() },
    { name: "limitation-drift", mutate: (value) => { value.limitations[0].state = "passed"; } }
  ];
}

function addExtraGate(value) {
  const extra = structuredClone(value.gates[0]);
  extra.id = "extra-gate";
  value.gates.push(extra);
}

function markFailed(value) {
  value.gates[0].outcome = "failed";
  value.gates[0].exitCode = 7;
}

function changePassCount(value) {
  const gate = value.gates.find((item) => item.passCount !== null);
  gate.passCount++;
}

function clearSentinel(value) {
  const gate = value.gates.find((item) => item.passCount !== null);
  gate.sentinelVerified = false;
}

function disableRequired(value) {
  value.gates[0].outcome = "disabled";
  value.gates[0].exitCode = null;
}

function passDisabledLive(value) {
  const gate = value.gates.find((item) => item.mode === "live");
  gate.outcome = "passed";
  gate.exitCode = 0;
}

function enableDisabledLive(value) {
  const gate = value.gates.find((item) => item.mode === "live");
  gate.liveEnabled = true;
}

function markLLMDegraded(value) {
  const expected = manifest.gates.find((item) => item.id === "live-llm");
  const gate = value.gates.find((item) => item.id === "live-llm");
  gate.liveEnabled = true;
  gate.outcome = "degraded";
  gate.exitCode = expected.degradedExitCode;
  gate.auditMarker = expected.degradedMarker;
  gate.sentinelVerified = true;
  value.limitations.push(structuredClone(expected.degradedLimitation));
}

function corruptDegradedExitCode(value) {
  markLLMDegraded(value);
  value.gates.find((item) => item.id === "live-llm").exitCode--;
}

function corruptDegradedMarker(value) {
  markLLMDegraded(value);
  value.gates.find((item) => item.id === "live-llm").auditMarker = "wrong";
}

function clearDegradedSentinel(value) {
  markLLMDegraded(value);
  value.gates.find((item) => item.id === "live-llm").sentinelVerified = false;
}

function removeDegradedLimitation(value) {
  markLLMDegraded(value);
  value.limitations.pop();
}

function addDegradedLimitationWithoutOutcome(value) {
  const expected = manifest.gates.find((item) => item.id === "live-llm");
  value.limitations.push(structuredClone(expected.degradedLimitation));
}

function degradeRequiredGate(value) {
  const gate = value.gates[0];
  gate.outcome = "degraded";
  gate.exitCode = 75;
  gate.auditMarker = "not-allowed";
  gate.sentinelVerified = true;
}

function assertAudit(report, manifestValue, success, label) {
  const reportPath = join(directory, `${label}-report.json`);
  const currentManifest = join(directory, `${label}-manifest.json`);
  writeFileSync(reportPath, JSON.stringify(report));
  writeFileSync(currentManifest, JSON.stringify(manifestValue));
  const result = spawnSync(process.execPath,
    [validator, currentManifest, reportPath, tree, source], { encoding: "utf8" });
  if ((result.status === 0) !== success) {
    throw new Error(`${label} 审计错误: status=${result.status} stdout=${result.stdout} stderr=${result.stderr}`);
  }
}
