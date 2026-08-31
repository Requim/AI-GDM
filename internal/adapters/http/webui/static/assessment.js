(function () {
  "use strict";

  const MAX_RESPONSE_BYTES = 1024 * 1024;
  const MAX_CASES = 1000;
  const MAX_EVENT_COUNT = 1_000_000;
  const IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
  const DIGEST = /^sha256:[0-9a-f]{64}$/;
  const SHA256 = /^[0-9a-f]{64}$/;
  const RISK_SNAPSHOT_EVENT = "ai-gdm:risk-snapshot";
  const PUBLIC_EVIDENCE_CITATION = "公开搜索证据；原始条目已转换为不可逆审计引用";
  const PUBLIC_EVIDENCE_LICENSE = "来源许可需在公开站点人工核验";
  const PUBLIC_EVIDENCE_LIMITATION = "证据文本、地址路径和未配置子域已最小化；条目身份与批次响应审计分别使用不可逆摘要";
  const SENSITIVE_QUERY = /^(?:.*(?:token|api[_-]?key|access[_-]?key|secret|signature|credential|password|passwd|session|authorization|cookie).*|key|x-amz-.+)$/i;
  const LOSS_REQUIRED_LIMITATIONS = [
    "仅估算道路和风险区内 POI 设施的直接物理损失",
    "结果用于辅助研判，不替代法定灾损核定"
  ];
  const AUTHORITY_SCHEMAS = {
    hazard_snapshot: "ai-gdm-authority-hazard-v1",
    evacuation_route: "ai-gdm-authority-route-v1",
    loss_assessment: "ai-gdm-authority-loss-v1",
    survival_assessment: "ai-gdm-authority-survival-v1"
  };
  const AUTHORITY_FIELDS = {
    hazard_snapshot: ["affectedAreaSquareMeters", "confidenceLevel", "dataStatus", "hazardType", "riskLevel",
      "riskZoneCount", "ruleVersion", "snapshotId"],
    evacuation_route: ["distanceMeters", "durationSeconds", "intersectsRiskZone", "mode", "rank", "riskScore",
      "riskScoreAvailable", "routeAnalysisId", "routeId", "ruleVersion", "snapshotId"],
    loss_assessment: ["affectedPopulation", "assessmentId", "conditionalCentralCents", "conditionalHighCents",
      "conditionalLowCents", "confidence", "confidenceBand", "formulaVersion", "impactAreaSquareMeters",
      "snapshotId", "status"],
    survival_assessment: ["assessmentId", "caseId", "factors", "humanReviewStatus", "limitations", "modelVersion", "priority", "probabilityBand",
      "probabilityHigh", "probabilityLow", "scenarioDigest", "scenarioId", "score", "scoreBand", "usage"]
  };
  const root = document.getElementById("assessment");
  if (!root) return;

  const elements = collectElements();
  const STRICT_UTC_RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?Z$/;
  const state = { caseRequest: 0, replayRequest: 0, aiRequest: 0, lossRequest: 0,
    lossPending: false, lossAutoSnapshotID: "", lossAutoSnapshotState: "",
    caseDetail: null, caseBindings: new Map(), modelCard: null, modelCardState: "loading",
    modelCardError: "", references: new Map() };

  bindTabs();
  activateTab("loss");
  bindLoss();
  bindSurvival();
  bindAI();
  loadCases();
  loadModelCard();

  function collectElements() {
    return {
      tabs: Array.from(root.querySelectorAll("[data-assessment-tab]")),
      panels: Array.from(root.querySelectorAll("[data-assessment-panel]")),
      lossForm: document.getElementById("loss-assessment-form"),
      lossInput: document.getElementById("loss-snapshot-id"),
      lossButton: document.getElementById("loss-assessment-run"),
      lossStatus: document.getElementById("loss-assessment-status"),
      lossID: document.getElementById("loss-assessment-id"),
      lossLow: document.getElementById("loss-low-amount"),
      lossCentral: document.getElementById("loss-central-amount"),
      lossHigh: document.getElementById("loss-high-amount"),
      lossResultState: document.getElementById("loss-result-state"),
      lossArea: document.getElementById("loss-impact-area"),
      lossPopulation: document.getElementById("loss-population"),
      lossRoads: document.getElementById("loss-road-length"),
      lossFacilities: document.getElementById("loss-facilities"),
      lossFormula: document.getElementById("loss-formula-version"),
      lossSources: document.getElementById("loss-source-list"),
      lossLimitations: document.getElementById("loss-limitation-list"),
      caseSelect: document.getElementById("survival-case-select"),
      caseSummary: document.getElementById("survival-case-summary"),
      replayButton: document.getElementById("survival-assessment-run"),
      survivalStatus: document.getElementById("survival-assessment-status"),
      scenarioID: document.getElementById("survival-scenario-id"),
      score: document.getElementById("survival-score"),
      probability: document.getElementById("survival-probability"),
      priority: document.getElementById("survival-priority"),
      scoreBand: document.getElementById("survival-score-band"),
      probabilityBand: document.getElementById("survival-probability-band"),
      modelVersion: document.getElementById("survival-model-version"),
      reviewStatus: document.getElementById("survival-review-status"),
      factors: document.getElementById("survival-factor-list"),
      survivalLimitations: document.getElementById("survival-limitation-list"),
      aiReference: document.getElementById("ai-analysis-reference"),
      aiForm: document.getElementById("ai-report-form"),
      aiButton: document.getElementById("ai-report-run"),
      aiStatus: document.getElementById("ai-report-status"),
      aiNarrative: document.getElementById("ai-report-narrative"),
      aiAuthorityKind: document.getElementById("ai-authority-kind"),
      aiAuthorityID: document.getElementById("ai-authority-id"),
      aiDigest: document.getElementById("ai-analysis-digest"),
      aiProviderStatus: document.getElementById("ai-provider-status"),
      aiEvidence: document.getElementById("ai-evidence-list")
    };
  }

  function bindTabs() {
    elements.tabs.forEach(function (tab, index) {
      tab.addEventListener("click", function () { activateTab(tab.dataset.assessmentTab); });
      tab.addEventListener("keydown", function (event) { moveTabFocus(event, index); });
    });
  }

  function moveTabFocus(event, index) {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const target = event.key === "Home" ? 0 : event.key === "End" ? elements.tabs.length - 1 :
      (index + (event.key === "ArrowRight" ? 1 : -1) + elements.tabs.length) % elements.tabs.length;
    const tab = elements.tabs[target];
    activateTab(tab.dataset.assessmentTab);
    tab.focus();
  }

  function activateTab(name) {
    elements.tabs.forEach(function (tab) {
      const active = tab.dataset.assessmentTab === name;
      tab.setAttribute("aria-selected", String(active));
      tab.tabIndex = active ? 0 : -1;
    });
    elements.panels.forEach(function (panel) { panel.hidden = panel.dataset.assessmentPanel !== name; });
  }

  function bindSurvival() {
    elements.caseSelect.addEventListener("change", function () {
      clearSurvivalResult("案例已改变，旧回放结果已清除。");
      if (elements.caseSelect.value) loadCaseDetail(elements.caseSelect.value);
      else clearCaseDetail("请选择公开历史案例查看合成输入。");
    });
    elements.replayButton.addEventListener("click", runReplay);
  }

  function bindLoss() {
    elements.lossButton.disabled = true;
    elements.lossButton.addEventListener("click", runLoss);
    elements.lossInput.addEventListener("keydown", function (event) {
      if (event.key !== "Enter") return;
      event.preventDefault();
      runLoss();
    });
    elements.lossInput.addEventListener("input", function () {
      state.lossAutoSnapshotID = "";
      state.lossAutoSnapshotState = "";
      clearLossResult("风险快照引用已改变，旧损失评估已清除。");
    });
    document.addEventListener(RISK_SNAPSHOT_EVENT, function (event) {
      bindRiskSnapshot(event && event.detail);
    });
    syncRiskSnapshot();
  }

  async function runLoss() {
    const snapshotID = elements.lossInput.value.trim();
    if (snapshotID === "") {
      clearLossResult("请等待风险地图加载有效快照，或手动输入已知有效的快照 ID。", "warning");
      return;
    }
    if (!validID(snapshotID) || !elements.lossInput.checkValidity()) {
      clearLossResult("风险快照标识无效，请检查后重试。", "error");
      elements.lossInput.reportValidity();
      return;
    }
    const request = ++state.lossRequest;
    state.lossPending = true;
    updateLossButton();
    clearLossValues();
    removeReference("loss_assessment");
    setAssessmentState(elements.lossStatus, "loading", "正在读取服务端权威输入并计算损失区间...");
    try {
      const created = await createLossAssessment(snapshotID);
      if (request !== state.lossRequest || elements.lossInput.value.trim() !== snapshotID) return;
      const loaded = await readCreatedLossAssessment(created, snapshotID);
      if (request !== state.lossRequest || elements.lossInput.value.trim() !== snapshotID) return;
      renderLossResult(loaded.result, loaded.audit);
    } catch (error) {
      if (request !== state.lossRequest) return;
      clearLossValues();
      removeReference("loss_assessment");
      setAssessmentState(elements.lossStatus, "error", errorMessage(error));
    } finally {
      if (request === state.lossRequest) {
        state.lossPending = false;
        updateLossButton();
      }
    }
  }

  function syncRiskSnapshot() {
    const riskMap = document.getElementById("risk-map");
    if (!riskMap || !riskMap.dataset.currentSnapshotId) return;
    bindRiskSnapshot({ available: true, snapshotId: riskMap.dataset.currentSnapshotId,
      state: riskMap.dataset.currentSnapshotState });
  }

  function bindRiskSnapshot(detail) {
    if (!detail || detail.available !== true || !validID(detail.snapshotId)) {
      clearAutoBoundSnapshot("风险地图当前没有可用于评估的快照，请等待刷新或手动输入已知有效 ID。");
      return;
    }
    const current = elements.lossInput.value.trim();
    if (current && current !== state.lossAutoSnapshotID) {
      updateLossButton();
      return;
    }
    const changed = current !== detail.snapshotId || state.lossAutoSnapshotID !== detail.snapshotId ||
      state.lossAutoSnapshotState !== detail.state;
    state.lossAutoSnapshotID = detail.snapshotId;
    state.lossAutoSnapshotState = detail.state;
    elements.lossInput.value = detail.snapshotId;
    if (!changed) {
      updateLossButton();
      return;
    }
    const message = detail.state === "fallback" ?
      "已绑定风险地图最后成功快照，评估结果需人工复核。" :
      "已绑定风险地图当前有效快照，可计算损失区间。";
    clearLossResult(message);
    state.lossAutoSnapshotID = detail.snapshotId;
    state.lossAutoSnapshotState = detail.state;
  }

  function clearAutoBoundSnapshot(message) {
    if (!state.lossAutoSnapshotID) {
      updateLossButton();
      return;
    }
    const shouldClear = elements.lossInput.value.trim() === state.lossAutoSnapshotID;
    state.lossAutoSnapshotID = "";
    state.lossAutoSnapshotState = "";
    if (!shouldClear) {
      updateLossButton();
      return;
    }
    elements.lossInput.value = "";
    clearLossResult(message, "warning");
  }

  function updateLossButton() {
    const snapshotID = elements.lossInput.value.trim();
    elements.lossButton.disabled = state.lossPending || !validID(snapshotID) || !elements.lossInput.checkValidity();
  }

  async function createLossAssessment(snapshotID) {
    const response = await requestJSON(root.dataset.lossEndpoint, {
      method: "POST", body: { snapshotId: snapshotID }, maxResponseBytes: responseLimit(),
      includeResponseMetadata: true
    });
    if (!response || response.status !== 201) throw new Error("损失评估创建状态无效");
    const result = validateLossPayload(response.payload, snapshotID);
    return { result: result, location: validateLossLocation(response.location, result.id) };
  }

  async function readCreatedLossAssessment(created, snapshotID) {
    const payload = await requestJSON(created.location, { maxResponseBytes: responseLimit() });
    const result = validateLossPayload(payload, snapshotID);
    if (result.id !== created.result.id || result.inputDigest !== created.result.inputDigest ||
      JSON.stringify(result) !== JSON.stringify(created.result)) {
      throw new Error("保存后的损失评估与创建结果不一致");
    }
    const auditPayload = await requestJSON(created.location + "/sources", { maxResponseBytes: responseLimit() });
    return { result: result, audit: validateLossAudit(auditPayload, result) };
  }

  function validateLossLocation(value, assessmentID) {
    if (typeof value !== "string" || value === "" || value.trim() !== value || value.length > 512) {
      throw new Error("损失评估 Location 无效");
    }
    let location;
    try { location = new URL(value, window.location.href); } catch (_) { throw new Error("损失评估 Location 无效"); }
    const base = new URL(root.dataset.lossEndpoint, window.location.href);
    const prefix = base.pathname.replace(/\/$/, "") + "/";
    const encodedID = location.pathname.startsWith(prefix) ? location.pathname.slice(prefix.length) : "";
    let decodedID;
    try { decodedID = decodeURIComponent(encodedID); } catch (_) { throw new Error("损失评估 Location 无效"); }
    if (location.origin !== window.location.origin || location.search || location.hash || location.username ||
      location.password || decodedID !== assessmentID || encodedID.includes("/")) {
      throw new Error("损失评估 Location 未绑定同源资源");
    }
    return location.pathname;
  }

  function renderLossResult(result, audit) {
    const available = result.status === "available";
    setAssessmentState(elements.lossStatus, available ? "current" : "warning", available ?
      "确定性损失区间已保存；金额和影响范围来自服务端权威输入。" :
      "损失评估输入不完整，结果仅用于说明数据不足。");
    elements.lossID.textContent = result.id;
    elements.lossLow.textContent = formatCNY(result.conditionalLowCents);
    elements.lossCentral.textContent = formatCNY(result.conditionalCentralCents);
    elements.lossHigh.textContent = formatCNY(result.conditionalHighCents);
    elements.lossResultState.textContent = lossStatusText(result.status) + " / " + bandText(result.confidenceBand);
    elements.lossArea.textContent = metricText(result.impactAreaSquareMeters, "平方米", result.metrics.impactArea);
    elements.lossPopulation.textContent = metricText(result.affectedPopulation, "人", result.metrics.affectedPopulation);
    elements.lossRoads.textContent = metricText(result.affectedRoadMeters, "米", result.metrics.affectedRoads);
    elements.lossFacilities.textContent = metricText(result.affectedFacilities, "处", result.metrics.affectedFacilities);
    elements.lossFormula.textContent = result.formulaVersion;
    renderLossSources(result, audit);
    renderLossLimitations(result, audit);
    rememberReference("loss_assessment", result.id, "损失评估 / " + result.id);
  }

  function clearLossResult(message, status) {
    state.lossRequest++;
    state.lossPending = false;
    clearLossValues();
    removeReference("loss_assessment");
    updateLossButton();
    setAssessmentState(elements.lossStatus, status || "idle", message || "请选择当前有效风险快照。");
  }

  function clearLossValues() {
    elements.lossID.textContent = "尚未生成评估";
    elements.lossLow.textContent = "--";
    elements.lossCentral.textContent = "--";
    elements.lossHigh.textContent = "--";
    elements.lossResultState.textContent = "未知";
    elements.lossArea.textContent = "未知";
    elements.lossPopulation.textContent = "未知";
    elements.lossRoads.textContent = "未知";
    elements.lossFacilities.textContent = "未知";
    elements.lossFormula.textContent = "未提供";
    const empty = document.createElement("p");
    empty.textContent = "尚未获得来源审计。";
    elements.lossSources.replaceChildren(empty);
    renderTextList(elements.lossLimitations, [], "损失金额不包含当前基线无法量化的间接损失。");
  }

  function validateLossPayload(payload, snapshotID) {
    if (!hasExactKeys(payload, ["data", "requestId"]) || typeof payload.requestId !== "string" ||
      payload.requestId.length > 256) throw new Error("损失评估响应封包无效");
    const value = payload.data;
    const allowed = ["id", "snapshotId", "formulaVersion", "scenarioMethod", "hazardType", "regionCode",
      "conditionalLowCents", "conditionalCentralCents", "conditionalHighCents", "expectedLowCents",
      "expectedCentralCents", "expectedHighCents", "impactAreaSquareMeters", "affectedPopulation",
      "affectedRoadMeters", "affectedFacilities", "inputReferences", "includedAssets", "excludedLosses",
      "status", "confidence", "confidenceBand", "limitations", "calculatedAt", "inputDigest", "metrics", "evidence"];
    const required = allowed.filter(function (name) { return !name.startsWith("expected"); });
    if (!hasRequiredKeys(value, allowed, required) || !validID(value.id) || value.snapshotId !== snapshotID ||
      value.formulaVersion !== "ai-gdm-loss-formula-v2" || !validText(value.scenarioMethod, 256) ||
      !["landslide", "debris_flow"].includes(value.hazardType) || !validText(value.regionCode, 128) ||
      value.status !== "available" || !strictUTC(value.calculatedAt, true) || !SHA256.test(value.inputDigest)) {
      throw new Error("损失评估身份、状态或时间契约无效");
    }
    validateLossAmounts(value);
    validateLossTotals(value);
    validateLossMetrics(value.metrics, value.status);
    const bindings = validateLossEvidence(value.evidence, value);
    value.inputReferences = validateLossReferences(value.inputReferences, "损失输入引用");
    value.includedAssets = validateEnumArray(value.includedAssets, ["building", "road", "facility"], "计入资产");
    value.excludedLosses = validateTextArray(value.excludedLosses, 1000, 4096, "排除损失");
    value.limitations = validateTextArray(value.limitations, 1000, 4096, "损失限制");
    if (!sameStringArray(value.includedAssets, bindings.includedAssets) ||
      !sameStringArray(value.inputReferences, bindings.inputReferences) ||
      !LOSS_REQUIRED_LIMITATIONS.every(function (item) { return value.limitations.includes(item); }) ||
      !value.evidence.spatialAnalysis.projectionLimitations.every(function (item) {
        return value.limitations.includes(item);
      })) {
      throw new Error("损失评估资产、来源或强制限制未与权威证据绑定");
    }
    return value;
  }

  function validateLossAudit(payload, result) {
    const fields = ["assessmentId", "snapshotId", "analysisId", "analysisVersion", "analysisDigest", "projectionId",
      "projectionVersion", "projectionDigest", "projectionCollectedAt", "projectionValidFrom", "projectionValidTo",
      "projectionLimitations", "sourceReferenceDigests", "adminBoundaryId", "adminBoundaryDigest", "formulaVersion",
      "inputDigest", "status", "calculatedAt", "inputReferences", "inputReferenceCount", "evidence", "scope",
      "limitations"];
    if (!hasExactKeys(payload, ["data", "requestId"]) || typeof payload.requestId !== "string" ||
      payload.requestId.length > 256 || !hasExactKeys(payload.data, fields)) throw new Error("损失来源审计封包无效");
    const audit = payload.data;
    if (audit.assessmentId !== result.id || audit.snapshotId !== result.snapshotId ||
      audit.formulaVersion !== result.formulaVersion || audit.inputDigest !== result.inputDigest ||
      audit.status !== result.status || audit.calculatedAt !== result.calculatedAt || !validID(audit.analysisId) ||
      !validText(audit.analysisVersion, 128) || !SHA256.test(audit.analysisDigest) || !validText(audit.scope, 1024)) {
      throw new Error("损失来源审计绑定无效");
    }
    const spatial = result.evidence.spatialAnalysis;
    if (audit.projectionId !== spatial.projectionId || audit.projectionVersion !== spatial.projectionVersion ||
      audit.projectionDigest !== spatial.projectionDigest || audit.projectionCollectedAt !== spatial.projectionCollectedAt ||
      audit.projectionValidFrom !== spatial.projectionValidFrom || audit.projectionValidTo !== spatial.projectionValidTo ||
      audit.adminBoundaryId !== spatial.adminBoundaryId || audit.adminBoundaryDigest !== spatial.adminBoundaryDigest ||
      !sameStringArray(audit.projectionLimitations, spatial.projectionLimitations) ||
      !sameStringArray(audit.sourceReferenceDigests, spatial.sourceReferenceDigests)) {
      throw new Error("损失来源审计投影身份不一致");
    }
    audit.projectionLimitations = validateSortedStrings(
      audit.projectionLimitations, 100, 4096, false, "损失审计投影限制");
    audit.inputReferences = validateLossReferences(audit.inputReferences, "损失审计输入引用");
    audit.limitations = validateTextArray(audit.limitations, 1000, 4096, "损失审计限制");
    if (!safeInteger(audit.inputReferenceCount, 1, 1000) || audit.inputReferenceCount !== audit.inputReferences.length ||
      !sameStringArray(audit.inputReferences, result.inputReferences) ||
      audit.analysisId !== result.evidence.spatialAnalysis.id || audit.analysisVersion !== result.evidence.spatialAnalysis.version ||
      audit.analysisDigest !== result.evidence.spatialAnalysis.digest || JSON.stringify(audit.evidence) !== JSON.stringify(result.evidence)) {
      throw new Error("损失来源审计证据与评估不一致");
    }
    validateLossEvidence(audit.evidence, result);
    return audit;
  }

  function validateLossAmounts(value) {
    const amounts = [value.conditionalLowCents, value.conditionalCentralCents, value.conditionalHighCents];
    if (!amounts.every(canonicalDecimal) || BigInt(amounts[0]) > BigInt(amounts[1]) ||
      BigInt(amounts[1]) > BigInt(amounts[2])) throw new Error("损失金额字符串契约无效");
    const expected = [value.expectedLowCents, value.expectedCentralCents, value.expectedHighCents];
    const present = expected.filter(function (item) { return item !== undefined; });
    if (present.length !== 0 && (present.length !== 3 || !expected.every(canonicalDecimal) ||
      BigInt(expected[0]) > BigInt(expected[1]) || BigInt(expected[1]) > BigInt(expected[2]))) {
      throw new Error("期望损失金额字符串契约无效");
    }
  }

  function validateLossTotals(value) {
    if (!finiteRange(value.impactAreaSquareMeters, 0, Number.MAX_VALUE) ||
      !finiteRange(value.affectedPopulation, 0, Number.MAX_VALUE) ||
      !finiteRange(value.affectedRoadMeters, 0, Number.MAX_VALUE) ||
      !safeInteger(value.affectedFacilities, 0, Number.MAX_SAFE_INTEGER) ||
      !finiteRange(value.confidence, 0, 1) || value.confidenceBand !== confidenceBand(value.confidence)) {
      throw new Error("损失影响范围或置信度契约无效");
    }
  }

  function validateLossMetrics(metrics, status) {
    const fields = ["impactArea", "affectedPopulation", "affectedRoads", "affectedFacilities", "conditionalDirectLoss"];
    if (!hasExactKeys(metrics, fields)) throw new Error("损失分项状态契约无效");
    fields.forEach(function (name) {
      const metric = metrics[name];
      if (!hasExactKeys(metric, ["provided", "status", "baselineLevel"]) || metric.provided !== true ||
        metric.status !== status || !["not_applicable", "regional", "national", "mixed"].includes(metric.baselineLevel)) {
        throw new Error("损失分项可用性或基线层级无效");
      }
    });
    if (metrics.impactArea.baselineLevel !== "not_applicable" ||
      metrics.affectedPopulation.baselineLevel !== "not_applicable" ||
      [metrics.affectedRoads, metrics.affectedFacilities, metrics.conditionalDirectLoss].some(function (metric) {
        return metric.baselineLevel === "not_applicable";
      })) throw new Error("损失分项基线层级无效");
  }

  function validateLossEvidence(evidence, result) {
    const fields = ["version", "snapshot", "spatialAnalysis", "baselineSet", "intensityBand", "riskZones",
      "population", "exposures", "costBaselines", "vulnerabilities"];
    if (!hasExactKeys(evidence, fields) || evidence.version !== "ai-gdm-loss-evidence-v1") {
      throw new Error("损失证据封包无效");
    }
    validateLossSnapshotEvidence(evidence.snapshot, result);
    validateLossSpatialEvidence(evidence.spatialAnalysis, result);
    validateLossBaselineSet(evidence.baselineSet);
    validateLossRiskZones(evidence.riskZones, evidence.intensityBand);
    const exposures = validateLossFeatures(evidence, result);
    validateLossBaselines(evidence.costBaselines, evidence.vulnerabilities, result, evidence.baselineSet, exposures);
    return {
      includedAssets: Array.from(new Set(exposures.map(function (value) { return value.assetType; }))).sort(),
      inputReferences: lossEvidenceReferences(evidence)
    };
  }

  function validateLossSnapshotEvidence(value, result) {
    const fields = ["id", "hazardType", "modelName", "modelVersion", "status", "runAt", "validFrom", "validTo", "source"];
    if (!hasExactKeys(value, fields) || value.id !== result.snapshotId || value.hazardType !== result.hazardType ||
      !validText(value.modelName, 128) || !validText(value.modelVersion, 128) || value.status !== "available" ||
      !strictUTC(value.runAt, true) || !strictUTC(value.validFrom, false) || !strictUTC(value.validTo, false) ||
      Date.parse(value.validFrom) >= Date.parse(value.validTo) || Date.parse(value.runAt) > Date.parse(result.calculatedAt) ||
      Date.parse(value.validTo) <= Date.parse(result.calculatedAt)) throw new Error("损失快照证据无效或已过期");
    validateLossSource(value.source, undefined, result.calculatedAt);
    if (value.source.stale || !strictUTC(value.source.validTo, false) ||
      Date.parse(value.source.validTo) <= Date.parse(result.calculatedAt)) throw new Error("损失快照来源已过期");
  }

  function validateLossSpatialEvidence(value, result) {
    const fields = ["id", "version", "digest", "projectionId", "projectionVersion", "projectionDigest",
      "projectionCollectedAt", "projectionValidFrom", "projectionValidTo", "projectionLimitations", "sourceReferenceDigests",
      "adminBoundaryId", "adminBoundaryDigest", "status", "regionCode", "totalAreaSquareMeters", "calculatedAt",
      "inputReferences", "datasetReferences"];
    if (!hasExactKeys(value, fields) || !validID(value.id) || !validText(value.version, 128) || !SHA256.test(value.digest) ||
      !validID(value.projectionId) || value.projectionVersion !== "ai-gdm-loss-risk-projection-v1" ||
      !SHA256.test(value.projectionDigest) || value.projectionId !== "exposure-" + value.projectionDigest ||
      !strictUTC(value.projectionCollectedAt, true) || !strictUTC(value.projectionValidFrom, true) ||
      !strictUTC(value.projectionValidTo, false) || !validID(value.adminBoundaryId) ||
      value.regionCode !== "CN" || !value.adminBoundaryId.startsWith("CHN-ADM0-") ||
      !SHA256.test(value.adminBoundaryDigest) ||
      value.status !== "available" || value.regionCode !== result.regionCode ||
      !finiteRange(value.totalAreaSquareMeters, 0, Number.MAX_VALUE) || !strictUTC(value.calculatedAt, true) ||
      Date.parse(value.calculatedAt) > Date.parse(value.projectionCollectedAt) ||
      Date.parse(value.projectionValidFrom) > Date.parse(value.projectionCollectedAt) ||
      Date.parse(value.projectionCollectedAt) >= Date.parse(value.projectionValidTo) ||
      Date.parse(value.projectionCollectedAt) > Date.parse(result.calculatedAt) ||
      Date.parse(value.projectionValidFrom) > Date.parse(result.calculatedAt) ||
      Date.parse(value.projectionValidTo) <= Date.parse(result.calculatedAt) ||
      !approximatelyEqual(value.totalAreaSquareMeters, result.impactAreaSquareMeters)) {
      throw new Error("空间分析证据无效");
    }
    value.projectionLimitations = validateSortedStrings(
      value.projectionLimitations, 100, 4096, false, "空间投影限制");
    validateSortedDigests(value.sourceReferenceDigests, "空间投影来源摘要");
    validateLossReferences(value.inputReferences, "空间输入引用");
    validateLossReferences(value.datasetReferences, "空间数据集引用");
  }

  function validateSortedDigests(values, label) {
    if (!Array.isArray(values) || values.length === 0 || values.length > 1000 || values.some(function (value, index) {
      return !SHA256.test(value) || (index > 0 && value <= values[index - 1]);
    })) throw new Error(label + "必须是有界、排序且唯一的 SHA-256 列表");
    return values;
  }

  function validateLossBaselineSet(value) {
    if (!hasExactKeys(value, ["provider", "dataset", "version"]) || !validText(value.provider, 256) ||
      !validText(value.dataset, 256) || !validText(value.version, 128)) throw new Error("损失基线集合身份无效");
  }

  function validateLossRiskZones(values, intensityBand) {
    if (!Array.isArray(values) || values.length === 0 || values.length > 1000) throw new Error("损失风险区证据无效");
    const levels = { low: 1, moderate: 2, high: 3, very_high: 4 };
    const seen = new Set();
    let maximum = "low";
    values.forEach(function (value) {
      if (!hasExactKeys(value, ["id", "level", "areaSquareMeters", "adminCodes"]) || !validID(value.id) ||
        !levels[value.level] || !finiteRange(value.areaSquareMeters, 0, Number.MAX_VALUE) || seen.has(value.id)) {
        throw new Error("损失风险区条目无效");
      }
      validateSortedStrings(value.adminCodes, 1000, 128, true, "风险区行政编码");
      seen.add(value.id);
      if (levels[value.level] > levels[maximum]) maximum = value.level;
    });
    if (intensityBand !== maximum) throw new Error("损失最高风险等级摘要不一致");
  }

  function validateLossFeatures(evidence, result) {
    const zones = new Map(evidence.riskZones.map(function (value) { return [value.id, value.level]; }));
    const features = new Set();
    const population = validatePopulationFeatures(evidence.population, zones, features);
    const exposures = validateAssetFeatures(evidence.exposures, zones, features, evidence.spatialAnalysis);
    const roads = exposures.filter(function (value) { return value.assetType === "road"; });
    const facilities = exposures.filter(function (value) { return value.assetType === "facility"; });
    const coverageConfidence = exposures.reduce(function (value, item) { return value * item.coverageRatio; }, 1);
    const expectedConfidence = evidence.spatialAnalysis.projectionLimitations.length > 0 ?
      Math.min(coverageConfidence, 0.79) : coverageConfidence;
    if (!approximatelyEqual(sumQuantity(population), result.affectedPopulation) ||
      !approximatelyEqual(sumQuantity(roads), result.affectedRoadMeters) ||
      !approximatelyEqual(sumQuantity(facilities), result.affectedFacilities) ||
      !approximatelyEqual(expectedConfidence, result.confidence)) {
      throw new Error("损失分项合计与全局去重证据不一致");
    }
    return exposures;
  }

  function validatePopulationFeatures(values, zones, seen) {
    if (!Array.isArray(values) || values.length === 0 || values.length > 1000) throw new Error("人口暴露证据无效");
    values.forEach(function (value) {
      const fields = ["featureId", "zoneId", "zoneIds", "quantity", "unit", "coverageRatio", "provided",
        "metricStatus", "inputReferences"];
      if (!hasExactKeys(value, fields) || !validLossFeatureCore(value, zones, seen) || value.unit !== "people") {
        throw new Error("人口暴露条目无效");
      }
      validateLossReferences(value.inputReferences, "人口暴露来源");
    });
    return values;
  }

  function validateAssetFeatures(values, zones, seen, analysis) {
    if (!Array.isArray(values) || values.length === 0 || values.length > 1000) throw new Error("资产暴露证据无效");
    values.forEach(function (value) {
      const fields = ["featureId", "zoneId", "zoneIds", "assetType", "quantity", "unit", "coverageRatio", "provided",
        "metricStatus", "intensityBand", "analysisId", "analysisVersion", "inputReferences"];
      const expectedUnit = value.assetType === "road" ? "meters" : value.assetType === "facility" ? "count" : "";
      if (!hasExactKeys(value, fields) || !validLossFeatureCore(value, zones, seen) || !expectedUnit ||
        value.unit !== expectedUnit || (value.assetType === "facility" && !safeInteger(value.quantity, 0, Number.MAX_SAFE_INTEGER)) ||
        value.analysisId !== analysis.id || value.analysisVersion !== analysis.version ||
        value.intensityBand !== maximumZoneLevel(value.zoneIds, zones)) throw new Error("资产暴露条目无效");
      validateLossReferences(value.inputReferences, "资产暴露来源");
    });
    return values;
  }

  function validLossFeatureCore(value, zones, seen) {
    if (!validID(value.featureId) || seen.has(value.featureId) || !finiteRange(value.quantity, 0, Number.MAX_VALUE) ||
      !finiteRange(value.coverageRatio, Number.MIN_VALUE, 1) || value.provided !== true || value.metricStatus !== "available") {
      return false;
    }
    if (!Array.isArray(value.zoneIds) || value.zoneIds.length === 0 || value.zoneIds.length > 1000 ||
      value.zoneId !== value.zoneIds[0] || !value.zoneIds.every(function (id, index) {
        return validID(id) && zones.has(id) && (index === 0 || id > value.zoneIds[index - 1]);
      })) return false;
    seen.add(value.featureId);
    return true;
  }

  function maximumZoneLevel(zoneIDs, zones) {
    const levels = { low: 1, moderate: 2, high: 3, very_high: 4 };
    return zoneIDs.reduce(function (selected, id) {
      return levels[zones.get(id)] > levels[selected] ? zones.get(id) : selected;
    }, "low");
  }

  function validateLossBaselines(costs, vulnerabilities, result, set, exposures) {
    if (!Array.isArray(costs) || costs.length === 0 || costs.length > 1000 || !Array.isArray(vulnerabilities) ||
      vulnerabilities.length === 0 || vulnerabilities.length > 1000) throw new Error("损失基线证据无效");
    const assets = new Map();
    const pairs = new Set();
    exposures.forEach(function (value) {
      assets.set(value.assetType, value.unit);
      pairs.add(value.assetType + "\u0000" + value.intensityBand);
    });
    const costAssets = new Set();
    let previousCost = "";
    costs.forEach(function (value) {
      const key = validateLossCost(value, set, result);
      if (key <= previousCost || costAssets.has(value.assetType) || !assets.has(value.assetType) ||
        assets.get(value.assetType) !== value.unit) throw new Error("成本基线与暴露资产或单位未完整绑定");
      costAssets.add(value.assetType);
      previousCost = key;
    });
    const vulnerabilityPairs = new Set();
    let previousVulnerability = "";
    vulnerabilities.forEach(function (value) {
      const key = validateLossVulnerability(value, set, result);
      const pair = value.assetType + "\u0000" + value.intensityBand;
      if (key <= previousVulnerability || vulnerabilityPairs.has(pair) || !pairs.has(pair)) {
        throw new Error("脆弱性基线与暴露资产和强度未完整绑定");
      }
      vulnerabilityPairs.add(pair);
      previousVulnerability = key;
    });
    if (costAssets.size !== assets.size || vulnerabilityPairs.size !== pairs.size) {
      throw new Error("损失基线存在缺失或多余语义项");
    }
  }

  function validateLossCost(value, set, result) {
    const allowed = ["id", "assetType", "regionCode", "unit", "lowCents", "centralCents", "highCents", "currency",
      "priceBaseDate", "status", "provided", "baselineLevel", "approvedBy", "source"];
    const required = allowed.filter(function (name) { return name !== "approvedBy"; });
    if (!hasRequiredKeys(value, allowed, required) || !validID(value.id) ||
      !["building", "road", "facility"].includes(value.assetType) || !validText(value.regionCode, 128) ||
      !validText(value.unit, 64) || ![value.lowCents, value.centralCents, value.highCents].every(canonicalDecimal) ||
      BigInt(value.lowCents) > BigInt(value.centralCents) || BigInt(value.centralCents) > BigInt(value.highCents) ||
      value.currency !== "CNY" || !strictUTC(value.priceBaseDate, true) ||
      Date.parse(value.priceBaseDate) > Date.parse(result.calculatedAt) || !validApprovedBaseline(value) ||
      !baselineRegionMatches(value.baselineLevel, value.regionCode, result.regionCode)) {
      throw new Error("成本基线条目无效");
    }
    validateLossSource(value.source, set.version, result.calculatedAt);
    return value.assetType + "\u0000" + value.id;
  }

  function validateLossVulnerability(value, set, result) {
    const allowed = ["id", "assetType", "hazardType", "intensityBand", "impactFractionLow", "impactFractionMid",
      "impactFractionHigh", "damageRatioLow", "damageRatioMid", "damageRatioHigh", "calibrationRegion", "status",
      "provided", "baselineLevel", "approvedBy", "source"];
    const required = allowed.filter(function (name) { return name !== "approvedBy"; });
    if (!hasRequiredKeys(value, allowed, required) || !validID(value.id) ||
      !["building", "road", "facility"].includes(value.assetType) || value.hazardType !== result.hazardType ||
      !["low", "moderate", "high", "very_high"].includes(value.intensityBand) ||
      !validFractionBand(value.impactFractionLow, value.impactFractionMid, value.impactFractionHigh) ||
      !validFractionBand(value.damageRatioLow, value.damageRatioMid, value.damageRatioHigh) ||
      !validText(value.calibrationRegion, 128) || !validApprovedBaseline(value) ||
      !baselineRegionMatches(value.baselineLevel, value.calibrationRegion, result.regionCode)) {
      throw new Error("脆弱性基线条目无效");
    }
    validateLossSource(value.source, set.version, result.calculatedAt);
    return value.assetType + "\u0000" + value.intensityBand + "\u0000" + value.id;
  }

  function validApprovedBaseline(value) {
    return value.status === "approved" && value.provided === true &&
      ["regional", "national"].includes(value.baselineLevel) && validText(value.approvedBy, 128);
  }

  function validateLossSource(source, expectedVersion, calculatedAt) {
    const allowed = ["provider", "dataset", "datasetVersion", "sourceRevision", "sourceUri", "citation", "license",
      "dataKind", "observedAt", "publishedAt", "revisionFirstSeenAt", "fetchedAt", "validFrom", "validTo",
      "spatialResolution", "temporalResolution", "crs", "bbox", "sha256", "transformVersion", "providerRequestId",
      "model", "stale", "qualityFlags", "limitations", "sourceParts"];
    const required = ["provider", "dataset", "sourceUri", "dataKind", "fetchedAt", "stale"];
    const safeURI = source && (source.sourceUri === "unavailable" || publicHTTPSURL(source.sourceUri, null));
    if (!hasRequiredKeys(source, allowed, required) || !validText(source.provider, 256) || !validText(source.dataset, 256) ||
      !safeURI || !["observation", "nowcast", "forecast", "baseline", "historical"].includes(source.dataKind) ||
      !strictUTC(source.fetchedAt, true) || typeof source.stale !== "boolean" ||
      (expectedVersion && (source.datasetVersion !== expectedVersion || source.dataKind !== "baseline")) ||
      !optionalText(source.datasetVersion, 128) ||
      !optionalText(source.sourceRevision, 256) || !optionalText(source.citation, 1024) ||
      !optionalText(source.license, 256) || !optionalText(source.spatialResolution, 256) ||
      !optionalText(source.temporalResolution, 128) || !optionalText(source.crs, 64) ||
      !optionalText(source.transformVersion, 128) || !optionalText(source.providerRequestId, 256) ||
      !optionalText(source.model, 128) || !optionalSHA256(source.sha256) || !validBBox(source.bbox)) {
      throw new Error("损失来源契约无效");
    }
    validateLossSourceTimes(source, calculatedAt);
    validateTextArray(source.qualityFlags || [], 32, 512, "损失来源质量标记");
    validateTextArray(source.limitations || [], 32, 1024, "损失来源限制");
    validateLossSourceParts(source.sourceParts || []);
  }

  function validateLossSourceTimes(source, calculatedAt) {
    ["observedAt", "publishedAt", "revisionFirstSeenAt", "validFrom", "validTo"].forEach(function (name) {
      if (source[name] !== undefined && !strictUTC(source[name], name !== "validTo")) {
        throw new Error("损失来源时间契约无效");
      }
    });
    if (source.validFrom !== undefined && source.validTo !== undefined &&
      Date.parse(source.validTo) <= Date.parse(source.validFrom)) throw new Error("损失来源有效期无效");
    if (calculatedAt && Date.parse(source.fetchedAt) > Date.parse(calculatedAt)) {
      throw new Error("损失来源抓取时间晚于评估时间");
    }
    if (calculatedAt && ["observedAt", "publishedAt", "revisionFirstSeenAt", "validFrom"].some(function (name) {
      return source[name] !== undefined && Date.parse(source[name]) > Date.parse(calculatedAt);
    })) throw new Error("损失来源时间晚于评估时间");
    if (calculatedAt && source.validTo !== undefined && Date.parse(source.validTo) <= Date.parse(calculatedAt)) {
      throw new Error("损失来源在评估时已经过期");
    }
  }

  function validateLossSourceParts(parts) {
    if (!Array.isArray(parts) || parts.length > 32) throw new Error("损失来源分片契约无效");
    const seen = new Set();
    parts.forEach(function (part) {
      if (!hasOnlyKeys(part, ["reference", "revision", "sizeBytes", "bbox", "sha256"]) ||
        !(part.reference === "unavailable" || publicHTTPSURL(part.reference, null)) || !validText(part.revision, 256) ||
        !safeInteger(part.sizeBytes, 1, Number.MAX_SAFE_INTEGER) || !validBBox(part.bbox) ||
        !optionalSHA256(part.sha256) || seen.has(part.reference)) throw new Error("损失来源分片契约无效");
      seen.add(part.reference);
    });
  }

  function validateLossReferences(values, label) {
    if (!Array.isArray(values) || values.length === 0 || values.length > 1000) throw new Error(label + "契约无效");
    values.forEach(function (value, index) {
      if (!(value === "unavailable" || publicHTTPSURL(value, null)) || (index > 0 && value <= values[index - 1])) {
        throw new Error(label + "必须脱敏、排序且不能重复");
      }
    });
    return values;
  }

  function lossEvidenceReferences(evidence) {
    let values = lossSourceReferences(evidence.snapshot.source)
      .concat(evidence.spatialAnalysis.inputReferences, evidence.spatialAnalysis.datasetReferences);
    evidence.population.forEach(function (value) { values = values.concat(value.inputReferences); });
    evidence.exposures.forEach(function (value) { values = values.concat(value.inputReferences); });
    evidence.costBaselines.forEach(function (value) { values = values.concat(lossSourceReferences(value.source)); });
    evidence.vulnerabilities.forEach(function (value) { values = values.concat(lossSourceReferences(value.source)); });
    return Array.from(new Set(values.map(function (value) { return value.trim(); }).filter(Boolean))).sort();
  }

  function lossSourceReferences(source) {
    return [source.sourceUri].concat((source.sourceParts || []).map(function (part) { return part.reference; }));
  }

  function baselineRegionMatches(level, actual, region) {
    return level === "regional" ? actual === region : level === "national" && actual === "CN";
  }

  function validateSortedStrings(values, maximum, maxRunes, required, label) {
    if (!Array.isArray(values) || values.length > maximum || (required && values.length === 0)) {
      throw new Error(label + "契约无效");
    }
    values.forEach(function (value, index) {
      if (!validText(value, maxRunes) || (index > 0 && value <= values[index - 1])) {
        throw new Error(label + "必须有界、排序且不能重复");
      }
    });
    return values;
  }

  function validateEnumArray(values, allowed, label) {
    if (!Array.isArray(values) || values.length === 0 || values.length > allowed.length ||
      values.some(function (value, index) { return !allowed.includes(value) || values.indexOf(value) !== index; })) {
      throw new Error(label + "契约无效");
    }
    return values;
  }

  function renderLossSources(result, audit) {
    const nodes = [lossAuditSummary(audit), lossBaselineSummary(result)];
    nodes.push(lossReferenceNode("来源审计引用", audit.inputReferences));
    nodes.push(lossReferenceNode("空间分析输入", result.evidence.spatialAnalysis.inputReferences));
    nodes.push(lossReferenceNode("空间分析数据集", result.evidence.spatialAnalysis.datasetReferences));
    nodes.push(lossReferenceNode("人口暴露来源", flattenLossReferences(result.evidence.population)));
    nodes.push(lossReferenceNode("道路暴露来源", flattenLossReferences(result.evidence.exposures.filter(function (value) {
      return value.assetType === "road";
    }))));
    nodes.push(lossReferenceNode("设施暴露来源", flattenLossReferences(result.evidence.exposures.filter(function (value) {
      return value.assetType === "facility";
    }))));
    const sources = [result.evidence.snapshot.source].concat(result.evidence.costBaselines.map(function (value) {
      return value.source;
    }), result.evidence.vulnerabilities.map(function (value) { return value.source; }));
    const seen = new Set();
    sources.forEach(function (source) {
      const key = [source.provider, source.dataset, source.datasetVersion || "", source.sourceUri].join("|");
      if (seen.has(key)) return;
      seen.add(key);
      nodes.push(lossSourceNode(source));
    });
    elements.lossSources.replaceChildren.apply(elements.lossSources, nodes);
  }

  function flattenLossReferences(values) {
    const result = [];
    values.forEach(function (value) { result.push.apply(result, value.inputReferences); });
    return Array.from(new Set(result)).sort();
  }

  function lossReferenceNode(label, values) {
    const item = document.createElement("article");
    item.className = "assessment-source-item";
    appendText(item, "strong", label);
    const visible = values.slice(0, 16);
    visible.forEach(function (value) {
      const safeURL = value === "unavailable" ? "" : publicHTTPSURL(value, null);
      if (!safeURL) {
        appendText(item, "span", "引用不可用或已移除敏感参数");
        return;
      }
      const link = document.createElement("a");
      link.href = safeURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      link.textContent = safeURL;
      item.appendChild(link);
    });
    if (values.length > visible.length) appendText(item, "span", "另有 " + (values.length - visible.length) + " 条引用未展开");
    return item;
  }

  function renderLossLimitations(result, audit) {
    const values = uniqueTextValues(result.limitations.concat(result.excludedLosses, audit.limitations));
    const required = LOSS_REQUIRED_LIMITATIONS.filter(function (value) { return values.includes(value); });
    const visible = required.concat(values.filter(function (value) {
      return !LOSS_REQUIRED_LIMITATIONS.includes(value);
    }).slice(0, 29));
    const nodes = visible.map(function (value) {
      const item = document.createElement("li");
      item.textContent = value;
      return item;
    });
    if (values.length > visible.length) {
      const item = document.createElement("li");
      item.textContent = "另有 " + (values.length - visible.length) + " 条限制未展开；完整内容保留在来源审计响应中。";
      nodes.push(item);
    }
    elements.lossLimitations.replaceChildren.apply(elements.lossLimitations, nodes);
  }

  function lossAuditSummary(audit) {
    const item = document.createElement("article");
    item.className = "assessment-source-item";
    appendText(item, "strong", "空间分析审计 " + audit.analysisId);
    appendText(item, "span", "版本 " + audit.analysisVersion + " · 输入引用 " + audit.inputReferenceCount + " 条");
    appendText(item, "span", "摘要 " + audit.analysisDigest);
    appendText(item, "span", "暴露投影 " + audit.evidence.spatialAnalysis.projectionId +
      " · " + audit.evidence.spatialAnalysis.projectionVersion);
    appendText(item, "span", "投影摘要 " + audit.evidence.spatialAnalysis.projectionDigest);
    appendText(item, "span", "行政边界 " + audit.adminBoundaryId +
      " · 摘要 " + audit.adminBoundaryDigest.slice(0, 12) + "...");
    return item;
  }

  function lossBaselineSummary(result) {
    const item = document.createElement("article");
    item.className = "assessment-source-item";
    appendText(item, "strong", "基线集合 " + result.evidence.baselineSet.version);
    appendText(item, "span", result.evidence.baselineSet.provider + " / " + result.evidence.baselineSet.dataset);
    appendText(item, "span", "道路 " + baselineLevelText(result.metrics.affectedRoads.baselineLevel) +
      " · 设施 " + baselineLevelText(result.metrics.affectedFacilities.baselineLevel) +
      " · 金额 " + baselineLevelText(result.metrics.conditionalDirectLoss.baselineLevel));
    return item;
  }

  function lossSourceNode(source) {
    const item = document.createElement("article");
    item.className = "assessment-source-item";
    appendText(item, "strong", source.provider + " / " + source.dataset);
    appendText(item, "span", source.citation || "未提供公开引用说明");
    appendText(item, "span", source.datasetVersion ? "版本 " + source.datasetVersion : "未提供数据集版本");
    const safeURL = source.sourceUri === "unavailable" ? "" : publicHTTPSURL(source.sourceUri, null);
    if (safeURL) {
      const link = document.createElement("a");
      link.href = safeURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      link.textContent = "查看 HTTPS 公开来源";
      item.appendChild(link);
    } else appendText(item, "span", "来源链接不可用或已移除敏感参数");
    return item;
  }

  function formatCNY(cents) {
    const value = BigInt(cents);
    const whole = (value / 100n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ",");
    return "¥" + whole + "." + (value % 100n).toString().padStart(2, "0");
  }

  function metricText(value, unit, metric) {
    const formatted = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 }).format(value);
    return formatted + " " + unit + " · " + (metric.provided ? "已提供" : "未提供") +
      "/" + lossStatusText(metric.status) + " · " + baselineLevelText(metric.baselineLevel);
  }

  function lossStatusText(value) { return value === "available" ? "可用" : value === "insufficient_data" ? "数据不足" : "未知"; }
  function baselineLevelText(value) {
    return ({ not_applicable: "不适用", regional: "区域级", national: "国家级", mixed: "混合层级" })[value] || "未知";
  }
  function confidenceBand(value) { return value >= 0.8 ? "high" : value >= 0.5 ? "moderate" : value >= 0.25 ? "low" : "very_low"; }
  function validFractionBand(low, middle, high) {
    return finiteRange(low, 0, 1) && finiteRange(middle, low, 1) && finiteRange(high, middle, 1);
  }
  function sumQuantity(values) { return values.reduce(function (sum, value) { return sum + value.quantity; }, 0); }
  function approximatelyEqual(left, right) { return Math.abs(left - right) <= Math.max(1e-9, Math.abs(right) * 1e-9); }

  function bindAI() {
    elements.aiForm.addEventListener("submit", function (event) {
      event.preventDefault();
      runAIReport();
    });
    elements.aiReference.addEventListener("change", function () {
      clearAIResult("权威引用已改变，旧解释已清除。");
    });
  }

  async function runAIReport() {
    const selected = elements.aiReference.value;
    const reference = parseReference(selected);
    if (!reference) {
      clearAIResult("请选择服务端已保存的确定性结果。");
      return;
    }
    const request = ++state.aiRequest;
    elements.aiButton.disabled = true;
    clearAIValues();
    setAssessmentState(elements.aiStatus, "loading", "正在按权威引用生成非权威解释...");
    try {
      const payload = await requestJSON(root.dataset.aiEndpoint, {
        method: "POST", body: { analysisRef: reference, evidenceLimit: 5 },
        timeoutMs: aiRequestTimeout(), maxResponseBytes: responseLimit()
      });
      if (request !== state.aiRequest || elements.aiReference.value !== selected) return;
      const result = await validateAIResult(payload, reference);
      if (request !== state.aiRequest || elements.aiReference.value !== selected) return;
      renderAIResult(result);
    } catch (error) {
      if (request !== state.aiRequest) return;
      clearAIValues();
      setAssessmentState(elements.aiStatus, "error", errorMessage(error));
    } finally {
      if (request === state.aiRequest) elements.aiButton.disabled = state.references.size === 0;
    }
  }

  async function validateAIResult(payload, reference) {
    const result = payload && payload.data;
    const resultKeys = ["authority", "authoritySha256", "authorityEnvelopeVersion", "evidence", "narrative",
      "evidenceAvailable", "narrativeAvailable", "limitations", "generatedAt"];
    if (!hasOnlyKeys(result, resultKeys) || result.authorityEnvelopeVersion !== "ai-gdm-authority-v1" ||
      typeof result.authoritySha256 !== "string" || !SHA256.test(result.authoritySha256) ||
      !strictUTC(result.generatedAt, true) || typeof result.evidenceAvailable !== "boolean" ||
      typeof result.narrativeAvailable !== "boolean") throw new Error("智能研判结果契约无效");
    validateAuthority(result.authority, reference);
    if (Date.parse(result.authority.resolvedAt) > Date.parse(result.generatedAt)) {
      throw new Error("权威分析解析时间晚于研判结果生成时间");
    }
    await verifyAuthorityDigest(result.authority, result.authoritySha256);
    result.evidence = validateEvidence(result.evidence);
    result.limitations = validateTextArray(result.limitations, 32, 1024, "智能研判限制");
    validateNarrative(result.narrative);
    if (result.evidenceAvailable !== (result.evidence.length > 0) ||
      result.narrativeAvailable !== result.narrative.available) throw new Error("智能研判可用状态不一致");
    validateReportTimeOrder(result);
    return result;
  }

  function validateReportTimeOrder(result) {
    const authorityTime = Date.parse(result.authority.resolvedAt);
    const reportTime = Date.parse(result.generatedAt);
    const narrativeTime = Date.parse(result.narrative.generatedAt);
    const sourceTimes = result.evidence.map(function (value) { return Date.parse(value.source.fetchedAt); });
    if (result.narrative.source) sourceTimes.push(Date.parse(result.narrative.source.fetchedAt));
    if (narrativeTime < authorityTime || sourceTimes.some(function (value) { return value < authorityTime; })) {
      throw new Error("智能研判子记录时间早于权威分析解析时间");
    }
    const crawledTimes = result.evidence.filter(function (value) { return value.crawledAt !== undefined; })
      .map(function (value) { return Date.parse(value.crawledAt); });
    if ([narrativeTime].concat(sourceTimes, crawledTimes).some(function (value) { return value > reportTime; })) {
      throw new Error("智能研判子记录时间晚于报告生成时间");
    }
    if (result.narrative.source && Date.parse(result.narrative.source.fetchedAt) > narrativeTime) {
      throw new Error("AI 解释来源抓取时间晚于 AI 解释生成时间");
    }
    if (result.evidence.some(function (value) { return Date.parse(value.source.fetchedAt) > narrativeTime; })) {
      throw new Error("智能研判证据时间晚于 AI 解释生成时间");
    }
    if (result.evidence.some(function (value) {
      return value.crawledAt !== undefined && Date.parse(value.crawledAt) > Date.parse(value.source.fetchedAt);
    })) throw new Error("搜索证据抓取时间晚于来源获取时间");
  }

  async function verifyAuthorityDigest(authority, expectedDigest) {
    const fields = AUTHORITY_FIELDS[authority.kind];
    const analysis = {};
    fields.forEach(function (field) { analysis[field] = authority.analysis[field]; });
    const envelope = {
      envelopeVersion: "ai-gdm-authority-v1", kind: authority.kind, id: authority.id,
      version: authority.version, schemaVersion: authority.schemaVersion,
      analysis: analysis, immutableFields: fields
    };
    const bytes = new TextEncoder().encode(JSON.stringify(envelope));
    const actual = bytesToHex(await sha256Bytes(bytes));
    if (actual !== expectedDigest) throw new Error("权威分析摘要校验失败");
  }

  async function sha256Bytes(bytes) {
    if (window.crypto && window.crypto.subtle) {
      try { return new Uint8Array(await window.crypto.subtle.digest("SHA-256", bytes)); } catch (_) { /* HTTP 下回退。 */ }
    }
    return sha256Fallback(bytes);
  }

  async function survivalDigest(value) {
    const bytes = new TextEncoder().encode(goCanonicalJSON(value));
    return "sha256:" + bytesToHex(await sha256Bytes(bytes));
  }

  function goCanonicalJSON(value) {
    return JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, function (character) {
      return "\\u" + character.charCodeAt(0).toString(16).padStart(4, "0");
    });
  }

  function validateAuthority(authority, reference) {
    const keys = ["kind", "id", "version", "schemaVersion", "analysis", "immutableFields", "resolvedAt"];
    if (!hasOnlyKeys(authority, keys) || authority.kind !== reference.kind || authority.id !== reference.id ||
      !validText(authority.version, 128) || authority.schemaVersion !== AUTHORITY_SCHEMAS[authority.kind] ||
      !strictUTC(authority.resolvedAt, true)) throw new Error("权威分析引用或版本绑定不一致");
    const expected = AUTHORITY_FIELDS[authority.kind];
    if (!expected || !hasExactKeys(authority.analysis, expected) || !sameStringArray(authority.immutableFields, expected)) {
      throw new Error("权威分析固定字段契约无效");
    }
    validateAuthorityAnalysis(authority);
  }

  function validateAuthorityAnalysis(authority) {
    const value = authority.analysis;
    switch (authority.kind) {
    case "hazard_snapshot":
      if (value.snapshotId !== authority.id || value.ruleVersion !== authority.version ||
        !finiteRange(value.affectedAreaSquareMeters, 0, Number.MAX_VALUE) || !safeInteger(value.riskZoneCount, 0, 1_000_000) ||
        !["landslide", "debris_flow"].includes(value.hazardType) ||
        !["low", "moderate", "high", "very_high"].includes(value.riskLevel) || !validHazardQuality(value) ||
        (value.riskZoneCount === 0 && (value.riskLevel !== "low" || value.affectedAreaSquareMeters !== 0)) ||
        (value.riskZoneCount > 0 && value.affectedAreaSquareMeters <= 0)) {
        throw new Error("风险 Authority 契约无效");
      }
      break;
    case "evacuation_route":
      if (value.routeAnalysisId !== authority.id || value.ruleVersion !== authority.version || !validID(value.routeId) ||
        !validID(value.snapshotId) || !["driving", "walking", "transit"].includes(value.mode) ||
        !safeInteger(value.rank, 1, 1000) || !finiteRange(value.distanceMeters, Number.MIN_VALUE, Number.MAX_VALUE) ||
        !safeInteger(value.durationSeconds, 1, Number.MAX_SAFE_INTEGER) || value.intersectsRiskZone !== false ||
        typeof value.riskScoreAvailable !== "boolean" || !finiteRange(value.riskScore, 0, 100) ||
        (!value.riskScoreAvailable && value.riskScore !== 0)) throw new Error("路线 Authority 契约无效");
      break;
    case "loss_assessment":
      if (value.assessmentId !== authority.id || value.formulaVersion !== authority.version || !validID(value.snapshotId) ||
        !canonicalDecimal(value.conditionalLowCents) || !canonicalDecimal(value.conditionalCentralCents) ||
        !canonicalDecimal(value.conditionalHighCents) || BigInt(value.conditionalLowCents) > BigInt(value.conditionalCentralCents) ||
        BigInt(value.conditionalCentralCents) > BigInt(value.conditionalHighCents) ||
        !finiteRange(value.impactAreaSquareMeters, 0, Number.MAX_VALUE) ||
        !finiteRange(value.affectedPopulation, 0, Number.MAX_VALUE) || !finiteRange(value.confidence, 0, 1) ||
        !["available", "insufficient_data"].includes(value.status) ||
        value.confidenceBand !== confidenceBand(value.confidence)) throw new Error("损失 Authority 契约无效");
      break;
    case "survival_assessment":
      value.factors = validateTextArray(value.factors, 32, 1024, "生还 Authority 因素");
      value.limitations = validateTextArray(value.limitations, 32, 1024, "生还 Authority 限制");
      if (value.assessmentId !== authority.id || !DIGEST.test(value.assessmentId) ||
        value.modelVersion !== authority.version || !validID(value.caseId) || !validID(value.scenarioId) ||
        !DIGEST.test(value.scenarioDigest) || value.humanReviewStatus !== "required" ||
        value.factors.length === 0 || value.limitations.length === 0 ||
        !validSurvivalAuthorityBands(value) || !validSurvivalAuthorityPriority(value.score, value.priority) ||
        !validReplayUsage(value.usage)) {
        throw new Error("生还 Authority 契约无效");
      }
      break;
    default:
      throw new Error("权威分析类型不受支持");
    }
  }

  function validHazardQuality(value) {
    if (value.dataStatus === "current") return value.confidenceLevel === "high" || value.confidenceLevel === "medium";
    return value.dataStatus === "fallback" && value.confidenceLevel === "low";
  }

  function validSurvivalAuthorityBands(value) {
    if (!safeInteger(value.score, 0, 100) || !finiteRange(value.probabilityLow, 0, 1) ||
      !finiteRange(value.probabilityHigh, value.probabilityLow, 1)) return false;
    const expected = value.score >= 75 ? [0.60, 0.85, "high", "high"] :
      value.score >= 50 ? [0.35, 0.59, "moderate", "moderate"] :
        value.score >= 25 ? [0.15, 0.34, "low", "low"] : [0.05, 0.14, "very_low", "very_low"];
    return value.probabilityLow === expected[0] && value.probabilityHigh === expected[1] &&
      value.scoreBand === expected[2] && value.probabilityBand === expected[3];
  }

  function validSurvivalAuthorityPriority(score, priority) {
    if (score >= 75) return priority === "immediate";
    if (score >= 55) return priority === "urgent" || priority === "immediate";
    if (score >= 50) return priority === "urgent";
    if (score >= 25) return priority === "elevated";
    return priority === "routine";
  }

  function validateEvidence(values) {
    if (!Array.isArray(values) || values.length > 20) throw new Error("搜索证据契约无效");
    const seenReferences = new Set();
    return values.map(function (evidence) {
      const keys = ["title", "url", "summary", "siteName", "crawledAt", "source"];
      if (!hasOnlyKeys(evidence, keys) || !validText(evidence.title, 512) || !validText(evidence.summary, 4096) ||
        !optionalText(evidence.siteName, 256) || (evidence.crawledAt !== undefined && !strictUTC(evidence.crawledAt, true))) {
        throw new Error("搜索证据条目无效");
      }
      evidence.safeURL = publicHTTPSURL(evidence.url, null);
      if (!evidence.safeURL) throw new Error("搜索证据链接不是安全 HTTPS 地址");
      validatePublicSource(evidence.source);
      validateMinimizedEvidenceSource(evidence.source);
      evidence.auditReference = evidence.source.sourceRevision;
      if (!DIGEST.test(evidence.auditReference || "") || seenReferences.has(evidence.auditReference)) {
        throw new Error("搜索证据不可逆审计引用无效或重复");
      }
      seenReferences.add(evidence.auditReference);
      return evidence;
    });
  }

  function validateMinimizedEvidenceSource(source) {
    const sourceURL = new URL(source.sourceUri);
    const expectedFlags = ["trusted_domain", "trusted_domain=" + sourceURL.hostname,
      "per_item_audit_reference", "response_audit_separated"];
    const forbidden = ["observedAt", "revisionFirstSeenAt", "spatialResolution", "temporalResolution",
      "crs", "model", "sourceParts"];
    if (source.provider !== "public-search" || source.dataset !== "public-disaster-information" ||
      source.datasetVersion !== "redacted-v2" || !DIGEST.test(source.sourceRevision || "") ||
      source.citation !== PUBLIC_EVIDENCE_CITATION || source.license !== PUBLIC_EVIDENCE_LICENSE ||
      source.dataKind !== "observation" || source.transformVersion !== "ai-gdm-public-evidence-redaction-v2" ||
      (source.providerRequestId !== undefined && !DIGEST.test(source.providerRequestId)) ||
      (source.sha256 !== undefined && !SHA256.test(source.sha256)) ||
      !sameStringArray(source.qualityFlags, expectedFlags) ||
      !sameStringArray(source.limitations, [PUBLIC_EVIDENCE_LIMITATION]) ||
      forbidden.some(function (name) { return source[name] !== undefined; })) {
      throw new Error("搜索证据公开来源最小化契约无效");
    }
  }

  function validateNarrative(value) {
    const keys = ["summary", "keyFindings", "actions", "caveats", "generatedAt", "model", "available", "source"];
    if (!hasOnlyKeys(value, keys) || typeof value.available !== "boolean" || !validText(value.summary, 4096) ||
      !strictUTC(value.generatedAt, true) || !optionalText(value.model, 256)) throw new Error("AI 解释契约无效");
    value.keyFindings = validateTextArray(value.keyFindings, 16, 4096, "AI 关键发现");
    value.actions = validateTextArray(value.actions, 16, 4096, "AI 行动建议");
    value.caveats = validateTextArray(value.caveats, 16, 4096, "AI 限制说明");
    if (value.available && (!validText(value.model, 256) || !value.source)) throw new Error("可用 AI 解释缺少模型来源");
    if (value.source !== undefined) validatePublicSource(value.source);
  }

  function renderAIResult(result) {
    const narrative = result.narrative;
    const complete = narrative.available && result.evidenceAvailable;
    const status = complete ? "replay" : "warning";
    const message = complete ? "非权威 AI 解释已生成；确定性数值保持不变。" : narrative.available ?
      "实时搜索证据不可用；已保留服务端确定性分析与非权威解释。" :
      "解释供应商不可用；已保留服务端确定性分析，未生成替代结论。";
    setAssessmentState(elements.aiStatus, status, message);
    elements.aiAuthorityKind.textContent = authorityKindText(result.authority.kind);
    elements.aiAuthorityID.textContent = result.authority.id;
    elements.aiDigest.textContent = result.authoritySha256;
    elements.aiProviderStatus.textContent = narrative.available ? narrative.model + " / " + formatTime(narrative.generatedAt) : "未调用或不可用";
    renderNarrative(narrative, result.limitations);
    renderEvidence(result.evidence);
  }

  function renderNarrative(narrative, limitations) {
    const nodes = [];
    const title = document.createElement("h3");
    title.textContent = narrative.available ? "非权威解释" : "暂无解释性说明";
    nodes.push(title);
    const summary = document.createElement("p");
    summary.textContent = narrative.summary;
    nodes.push(summary);
    appendNarrativeList(nodes, "关键发现", narrative.keyFindings);
    appendNarrativeList(nodes, "人工行动建议", narrative.actions);
    appendNarrativeList(nodes, "模型限制", narrative.caveats.concat(limitations));
    elements.aiNarrative.replaceChildren.apply(elements.aiNarrative, nodes);
  }

  function appendNarrativeList(nodes, label, values) {
    if (!values.length) return;
    const heading = document.createElement("span");
    heading.className = "ai-narrative-label";
    heading.textContent = label;
    const list = document.createElement("ul");
    values.forEach(function (value) { appendText(list, "li", value); });
    nodes.push(heading, list);
  }

  function renderEvidence(values) {
    if (values.length === 0) {
      const empty = document.createElement("p");
      empty.textContent = "本次没有通过校验的实时搜索证据。";
      elements.aiEvidence.replaceChildren(empty);
      return;
    }
    const nodes = values.map(function (value) {
      const item = document.createElement("article");
      item.className = "assessment-source-item";
      appendText(item, "strong", value.title);
      appendText(item, "span", value.siteName || value.source.provider);
      appendText(item, "span", value.summary);
      appendText(item, "code", "证据引用 " + value.auditReference);
      const link = document.createElement("a");
      link.href = value.safeURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      link.textContent = "查看 HTTPS 公开来源";
      item.appendChild(link);
      return item;
    });
    elements.aiEvidence.replaceChildren.apply(elements.aiEvidence, nodes);
  }

  function clearAIResult(message, status) {
    state.aiRequest++;
    clearAIValues();
    elements.aiButton.disabled = !parseReference(elements.aiReference.value);
    setAssessmentState(elements.aiStatus, status || "idle", message || "请选择服务端已保存的确定性结果。");
  }

  function clearAIValues() {
    elements.aiAuthorityKind.textContent = "未提供";
    elements.aiAuthorityID.textContent = "未提供";
    elements.aiDigest.textContent = "未提供";
    elements.aiProviderStatus.textContent = "未调用";
    const title = document.createElement("h3");
    title.textContent = "尚未生成解释";
    const paragraph = document.createElement("p");
    paragraph.textContent = "模型只可汇总证据、限制和人工行动建议。";
    elements.aiNarrative.replaceChildren(title, paragraph);
    const empty = document.createElement("p");
    empty.textContent = "尚未获得搜索证据。";
    elements.aiEvidence.replaceChildren(empty);
  }

  async function loadCases() {
    const request = ++state.caseRequest;
    elements.caseSelect.disabled = true;
    replaceOptions([{ value: "", label: "正在读取案例目录..." }]);
    try {
      const payload = await requestJSON(root.dataset.survivalCasesEndpoint, { maxResponseBytes: responseLimit() });
      if (request !== state.caseRequest) return;
      const cases = validateCaseList(payload);
      state.caseBindings = new Map(cases.map(function (entry) { return [entry.event.id, entry.scenarioId]; }));
      const options = [{ value: "", label: cases.length ? "请选择公开历史案例" : "没有可用历史案例" }];
      cases.forEach(function (entry) { options.push({ value: entry.event.id, label: caseLabel(entry.event) }); });
      replaceOptions(options);
      elements.caseSelect.disabled = cases.length === 0;
      if (cases.length === 0) clearCaseDetail("当前没有通过校验的公开历史案例。", "idle");
    } catch (error) {
      if (request !== state.caseRequest) return;
      replaceOptions([{ value: "", label: "历史案例目录不可用" }]);
      state.caseBindings = new Map();
      elements.caseSelect.disabled = true;
      clearCaseDetail(errorMessage(error), "error");
    }
  }

  async function loadCaseDetail(caseID) {
    const request = ++state.caseRequest;
    elements.caseSelect.disabled = true;
    elements.replayButton.disabled = true;
    clearCaseDetail("正在读取合成匿名场景...", "loading");
    try {
      const endpoint = root.dataset.survivalCasesEndpoint + "/" + encodeURIComponent(caseID);
      const payload = await requestJSON(endpoint, { maxResponseBytes: responseLimit() });
      if (request !== state.caseRequest || elements.caseSelect.value !== caseID) return;
      const detail = await validateCaseDetail(payload, caseID, state.caseBindings.get(caseID));
      if (request !== state.caseRequest || elements.caseSelect.value !== caseID) return;
      state.caseDetail = detail;
      renderCaseDetail(detail);
      renderReplayReadiness();
    } catch (error) {
      if (request !== state.caseRequest) return;
      clearCaseDetail(errorMessage(error), "error");
    } finally {
      if (request === state.caseRequest) elements.caseSelect.disabled = false;
    }
  }

  async function loadModelCard() {
    try {
      const payload = await requestJSON(root.dataset.survivalModelCardEndpoint, { maxResponseBytes: responseLimit() });
      state.modelCard = validateModelCard(payload);
      state.modelCardState = "ready";
      state.modelCardError = "";
      renderReplayReadiness();
    } catch (error) {
      state.modelCard = null;
      state.modelCardState = "error";
      state.modelCardError = errorMessage(error);
      clearSurvivalResult("模型卡不可用，历史回放按不可解释处理。" );
      setAssessmentState(elements.survivalStatus, "error", state.modelCardError);
      updateReplayAvailability();
    }
  }

  async function runReplay() {
    const detail = state.caseDetail;
    if (!detail) return;
    const request = ++state.replayRequest;
    elements.replayButton.disabled = true;
    setAssessmentState(elements.survivalStatus, "loading", "正在运行确定性历史回放...");
    clearSurvivalValues();
    try {
      const endpoint = root.dataset.survivalCasesEndpoint.replace(/\/cases$/, "") +
        "/replays/cases/" + encodeURIComponent(detail.event.id) + "/assessment";
      const payload = await requestJSON(endpoint, { method: "POST", maxResponseBytes: responseLimit() });
      if (request !== state.replayRequest || state.caseDetail !== detail) return;
      const replay = await validateReplay(payload, detail, state.modelCard);
      if (request !== state.replayRequest || state.caseDetail !== detail) return;
      renderReplay(replay);
      rememberReference("survival_assessment", replay.assessmentId,
        "历史回放 / " + detail.event.adminArea + " / " + replay.assessment.modelVersion);
    } catch (error) {
      if (request !== state.replayRequest) return;
      removeReference("survival_assessment");
      clearSurvivalValues();
      setAssessmentState(elements.survivalStatus, "error", errorMessage(error));
    } finally {
      if (request === state.replayRequest && state.caseDetail === detail) elements.replayButton.disabled = false;
    }
  }

  function validateCaseList(payload) {
    const values = payload && payload.data;
    if (!Array.isArray(values) || values.length > MAX_CASES) throw new Error("历史案例目录契约无效");
    const seenCases = new Set();
    const seenScenarios = new Set();
    return values.map(function (entry) {
      if (!entry || !entry.event || !validID(entry.event.id) || !validID(entry.scenarioId) ||
        seenCases.has(entry.event.id) || seenScenarios.has(entry.scenarioId)) {
        throw new Error("历史案例目录包含无效或重复绑定");
      }
      seenCases.add(entry.event.id);
      seenScenarios.add(entry.scenarioId);
      validateHistoricalEvent(entry.event);
      return entry;
    });
  }

  async function validateCaseDetail(payload, expectedCaseID, expectedScenarioID) {
    const detail = payload && payload.data;
    const fields = ["event", "scenario", "scenarioDigest", "usage"];
    if (!hasExactKeys(detail, fields) || !detail.event || !detail.scenario || detail.event.id !== expectedCaseID ||
      !validID(expectedScenarioID) || detail.scenario.id !== expectedScenarioID ||
      detail.scenario.caseId !== expectedCaseID || !validID(detail.scenario.id) ||
      !DIGEST.test(detail.scenarioDigest || "") || !validReplayUsage(detail.usage)) {
      throw new Error("历史案例详情绑定不一致");
    }
    validateHistoricalEvent(detail.event);
    validateScenario(detail.scenario);
    if (Date.parse(detail.event.eventDate) > Date.parse(detail.scenario.asOf)) {
      throw new Error("合成匿名场景时刻早于历史事件日期");
    }
    if (await survivalDigest(canonicalScenario(detail.scenario)) !== detail.scenarioDigest) {
      throw new Error("历史案例场景摘要校验失败");
    }
    return detail;
  }

  async function validateReplay(payload, detail, modelCard) {
    const replay = payload && payload.data;
    const value = replay && replay.assessment;
    const replayFields = ["assessmentId", "caseId", "scenarioId", "scenarioDigest", "usage", "assessment"];
    const assessmentFields = ["scenarioId", "score", "scoreBand", "probabilityLow", "probabilityHigh",
      "probabilityBand", "priority", "factors", "modelVersion", "humanReviewStatus", "calculatedAt", "limitations"];
    if (!hasExactKeys(replay, replayFields) || !hasExactKeys(value, assessmentFields) ||
      !DIGEST.test(replay.assessmentId || "") || replay.caseId !== detail.event.id || replay.scenarioId !== detail.scenario.id ||
      replay.scenarioDigest !== detail.scenarioDigest || !sameUsage(replay.usage, detail.usage) ||
      !value || !modelCard || value.modelVersion !== modelCard.modelVersion ||
      value.scenarioId !== replay.scenarioId || value.humanReviewStatus !== "required" ||
      !strictUTC(value.calculatedAt, true) || Date.parse(value.calculatedAt) < Date.parse(detail.scenario.asOf)) {
      throw new Error("历史回放结果未保持案例、场景或人工复核绑定");
    }
    const expected = expectedSurvivalBands(value.score, detail.scenario.elapsedMinutes);
    if (!safeInteger(value.score, 0, 100) || !finiteRange(value.probabilityLow, 0, 1) ||
      !finiteRange(value.probabilityHigh, value.probabilityLow, 1) || !validText(value.modelVersion, 128) ||
      value.priority !== expected.priority || value.scoreBand !== expected.scoreBand ||
      value.probabilityBand !== expected.probabilityBand || value.probabilityLow !== expected.low ||
      value.probabilityHigh !== expected.high) {
      throw new Error("历史回放数值或模型契约无效");
    }
    value.factors = validateTextArray(value.factors, 32, 1024, "回放因素");
    if (value.factors.length === 0) throw new Error("历史回放必须包含解释因素");
    value.limitations = validateTextArray(value.limitations, 32, 1024, "回放限制");
    if (await survivalDigest(replayAssessmentIdentity(replay, value)) !== replay.assessmentId) {
      throw new Error("历史回放评估标识校验失败");
    }
    return replay;
  }

  function canonicalScenario(value) {
    return {
      id: value.id, caseId: value.caseId, asOf: value.asOf, elapsedMinutes: value.elapsedMinutes,
      environment: { airPocket: value.environment.airPocket, waterAvailable: value.environment.waterAvailable,
        hazardStable: value.environment.hazardStable },
      entrapment: { communication: value.entrapment.communication, injury: value.entrapment.injury },
      inputCompleteness: value.inputCompleteness, synthetic: value.synthetic
    };
  }

  function replayAssessmentIdentity(replay, value) {
    return {
      caseId: replay.caseId, scenarioId: replay.scenarioId, scenarioDigest: replay.scenarioDigest,
      modelVersion: value.modelVersion, score: value.score, scoreBand: value.scoreBand,
      probabilityLow: value.probabilityLow, probabilityHigh: value.probabilityHigh,
      probabilityBand: value.probabilityBand, priority: value.priority, factors: value.factors,
      humanReviewStatus: value.humanReviewStatus, limitations: value.limitations
    };
  }

  function validateHistoricalEvent(event) {
    const allowed = ["id", "datasetEventId", "eventDate", "timePrecision", "category", "trigger", "size",
      "location", "locationAccuracy", "country", "adminArea", "fatalities", "injuries", "source", "limitations"];
    if (!hasOnlyKeys(event, allowed) || !validID(event.id) || !validID(event.datasetEventId) ||
      !strictUTC(event.eventDate, true) || !["day", "hour", "minute", "second"].includes(event.timePrecision) ||
      !["landslide", "debris_flow"].includes(event.category) || !validText(event.trigger, 256) ||
      (event.size !== undefined && !validText(event.size, 128)) || !validText(event.locationAccuracy, 128) ||
      !validText(event.adminArea, 256) || !validText(event.country, 128) ||
      !validReportedCount(event.fatalities) || !validReportedCount(event.injuries) ||
      !validWGS84Point(event.location)) {
      throw new Error("历史案例公开事件摘要无效");
    }
    event.limitations = validateTextArray(event.limitations, 32, 1024, "历史案例限制");
    if (event.limitations.length === 0) throw new Error("历史案例必须说明公开数据限制");
    validateHistoricalSource(event.source);
  }

  function validateScenario(scenario) {
    const scenarioKeys = ["id", "caseId", "asOf", "elapsedMinutes", "environment", "entrapment",
      "inputCompleteness", "synthetic"];
    const environment = scenario.environment || {};
    const entrapment = scenario.entrapment || {};
    const signals = [environment.airPocket, environment.waterAvailable, environment.hazardStable,
      entrapment.communication];
    const known = signals.concat([entrapment.injury]).filter(function (value) { return value !== "unknown"; }).length;
    const expectedCompleteness = known / 5;
    if (!hasOnlyKeys(scenario, scenarioKeys) || !hasOnlyKeys(environment, ["airPocket", "waterAvailable", "hazardStable"]) ||
      !hasOnlyKeys(entrapment, ["communication", "injury"]) || !validID(scenario.id) || !validID(scenario.caseId) ||
      scenario.synthetic !== true || !strictUTC(scenario.asOf, true) || !safeInteger(scenario.elapsedMinutes, 0, 10_000_000) ||
      !finiteRange(scenario.inputCompleteness, 0, 1) || signals.some(function (value) {
        return !["unknown", "yes", "no"].includes(value);
      }) || !["unknown", "none", "minor", "severe", "critical"].includes(entrapment.injury) ||
      Math.abs(scenario.inputCompleteness - expectedCompleteness) > 1e-9) {
      throw new Error("合成匿名场景契约无效");
    }
  }

  function validateHistoricalSource(source) {
    const allowed = ["provider", "dataset", "datasetVersion", "sourceRevision", "sourceUri", "citation", "license",
      "dataKind", "observedAt", "publishedAt", "revisionFirstSeenAt", "fetchedAt", "validFrom", "validTo",
      "spatialResolution", "temporalResolution", "crs", "bbox", "sha256", "transformVersion", "providerRequestId",
      "model", "stale", "qualityFlags", "limitations", "sourceParts"];
    if (!hasOnlyKeys(source, allowed) || source.provider !== "USGS" || !validText(source.dataset, 256) ||
      !validText(source.datasetVersion, 128) || !validText(source.sourceRevision, 128) ||
      !validText(source.citation, 1024) || source.dataKind !== "historical" || source.stale !== false ||
      !strictUTC(source.fetchedAt, true) || !strictUTC(source.revisionFirstSeenAt, true) ||
      !validHistoricalURL(source.sourceUri) || !optionalText(source.license, 256) ||
      !optionalText(source.spatialResolution, 256) || !validText(source.temporalResolution, 128) ||
      source.crs !== "EPSG:4326" || !optionalText(source.transformVersion, 128) ||
      !optionalText(source.providerRequestId, 256) || !optionalText(source.model, 128) ||
      !optionalSHA256(source.sha256) || !validBBox(source.bbox)) {
      throw new Error("历史案例来源契约无效");
    }
    ["observedAt", "publishedAt", "validFrom", "validTo"].forEach(function (name) {
      if (source[name] !== undefined && !strictUTC(source[name], true)) throw new Error("历史案例来源时间无效");
    });
    const hasValidFrom = source.validFrom !== undefined;
    const hasValidTo = source.validTo !== undefined;
    if (hasValidFrom !== hasValidTo || (hasValidFrom && Date.parse(source.validTo) < Date.parse(source.validFrom))) {
      throw new Error("历史案例来源有效期无效");
    }
    validateTextArray(source.qualityFlags || [], 32, 512, "历史来源质量标记");
    validateTextArray(source.limitations || [], 32, 1024, "历史来源限制");
    validateSourceParts(source.sourceParts || []);
  }

  function validatePublicSource(source) {
    const allowed = ["provider", "dataset", "datasetVersion", "sourceRevision", "sourceUri", "citation", "license",
      "dataKind", "observedAt", "publishedAt", "revisionFirstSeenAt", "fetchedAt", "validFrom", "validTo",
      "spatialResolution", "temporalResolution", "crs", "bbox", "sha256", "transformVersion", "providerRequestId",
      "model", "stale", "qualityFlags", "limitations", "sourceParts"];
    if (!hasOnlyKeys(source, allowed) || !validText(source.provider, 256) || !validText(source.dataset, 256) ||
      !publicHTTPSURL(source.sourceUri, null) ||
      !["observation", "nowcast", "forecast", "baseline", "historical"].includes(source.dataKind) ||
      !strictUTC(source.fetchedAt, true) || typeof source.stale !== "boolean" || !optionalText(source.datasetVersion, 128) ||
      !optionalText(source.sourceRevision, 256) || !optionalText(source.citation, 1024) ||
      !optionalText(source.license, 256) || !optionalText(source.spatialResolution, 256) ||
      !optionalText(source.temporalResolution, 128) || !optionalText(source.crs, 64) ||
      !optionalText(source.transformVersion, 128) || !optionalText(source.providerRequestId, 256) ||
      !optionalText(source.model, 128) || !optionalSHA256(source.sha256) || !validBBox(source.bbox)) {
      throw new Error("公开来源契约无效");
    }
    const times = ["observedAt", "publishedAt", "revisionFirstSeenAt", "validFrom", "validTo"];
    times.forEach(function (name) {
      if (source[name] !== undefined && !strictUTC(source[name], true)) throw new Error("公开来源时间契约无效");
    });
    if (source.validFrom !== undefined && source.validTo !== undefined &&
      Date.parse(source.validTo) < Date.parse(source.validFrom)) throw new Error("公开来源有效期无效");
    validateTextArray(source.qualityFlags || [], 32, 512, "公开来源质量标记");
    validateTextArray(source.limitations || [], 32, 1024, "公开来源限制");
    validateSourceParts(source.sourceParts || []);
  }

  function validateSourceParts(parts) {
    if (!Array.isArray(parts) || parts.length > 32) throw new Error("历史来源分片契约无效");
    const seen = new Set();
    parts.forEach(function (part) {
      if (!hasOnlyKeys(part, ["reference", "revision", "sizeBytes", "bbox", "sha256"]) ||
        !validText(part.reference, 512) || !validText(part.revision, 256) ||
        !safeInteger(part.sizeBytes, 1, Number.MAX_SAFE_INTEGER) || !validBBox(part.bbox) ||
        !optionalSHA256(part.sha256) || seen.has(part.reference)) {
        throw new Error("历史来源分片契约无效");
      }
      seen.add(part.reference);
    });
  }

  function validateModelCard(payload) {
    const card = payload && payload.data;
    const fields = ["modelVersion", "name", "purpose", "scope", "inputs", "outputs", "limitations", "review"];
    if (!hasExactKeys(card, fields) || !validText(card.modelVersion, 128) || !validText(card.name, 256) ||
      !validText(card.purpose, 1024) || !validText(card.scope, 1024) || !validText(card.review, 1024)) {
      throw new Error("生还评估模型卡契约无效");
    }
    card.inputs = validateTextArray(card.inputs, 32, 1024, "模型卡输入");
    card.outputs = validateTextArray(card.outputs, 32, 1024, "模型卡输出");
    card.limitations = validateTextArray(card.limitations, 32, 1024, "模型卡限制");
    if (card.inputs.length === 0 || card.outputs.length === 0 || card.limitations.length === 0) {
      throw new Error("生还评估模型卡内容不完整");
    }
    return card;
  }

  function renderCaseDetail(detail) {
    const scenario = detail.scenario;
    const values = [
      ["案例范围", detail.event.country + " / " + detail.event.adminArea],
      ["事件日期", formatDate(detail.event.eventDate)],
      ["事件类别", detail.event.category],
      ["公开死亡数", reportedCountText(detail.event.fatalities)],
      ["公开受伤数", reportedCountText(detail.event.injuries)],
      ["失联时长", String(scenario.elapsedMinutes) + " 分钟"],
      ["空气空间", signalText(scenario.environment.airPocket)],
      ["可用水源", signalText(scenario.environment.waterAvailable)],
      ["环境稳定", signalText(scenario.environment.hazardStable)],
      ["通信信号", signalText(scenario.entrapment.communication)],
      ["合成伤情", injuryText(scenario.entrapment.injury)],
      ["输入完整度", (scenario.inputCompleteness * 100).toFixed(0) + "%"]
    ];
    const list = document.createElement("dl");
    values.forEach(function (value) {
      const row = document.createElement("div");
      appendText(row, "dt", value[0]);
      appendText(row, "dd", value[1]);
      list.appendChild(row);
    });
    elements.caseSummary.replaceChildren(list, historicalSourceNode(detail.event.source));
    elements.scenarioID.textContent = scenario.id;
    renderTextList(elements.survivalLimitations,
      [].concat(detail.event.limitations || [], [detail.usage.disclaimer]), "暂无限制说明");
  }

  function historicalSourceNode(source) {
    const item = document.createElement("article");
    item.className = "assessment-source-item";
    appendText(item, "strong", "公开来源：" + source.provider + " / " + source.dataset);
    appendText(item, "span", source.citation);
    const safeURL = publicHTTPSURL(source.sourceUri,
      function (host) { return host === "usgs.gov" || host.endsWith(".usgs.gov"); });
    const link = document.createElement("a");
    link.href = safeURL;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.textContent = "查看 USGS HTTPS 来源";
    item.appendChild(link);
    return item;
  }

  function renderReplay(replay) {
    const value = replay.assessment;
    setAssessmentState(elements.survivalStatus, "replay", "公开历史案例与合成匿名场景回放完成；结果强制要求人工复核。" );
    elements.scenarioID.textContent = replay.scenarioId;
    elements.score.textContent = String(value.score) + " / 100";
    elements.probability.textContent = percent(value.probabilityLow) + " - " + percent(value.probabilityHigh);
    elements.priority.textContent = priorityText(value.priority);
    elements.scoreBand.textContent = bandText(value.scoreBand);
    elements.probabilityBand.textContent = bandText(value.probabilityBand);
    elements.modelVersion.textContent = value.modelVersion;
    elements.reviewStatus.textContent = "必须人工复核";
    renderTextList(elements.factors, value.factors, "未提供评估因素");
    renderTextList(elements.survivalLimitations, uniqueTextValues(
      detailLimitations().concat(value.limitations, state.modelCard.limitations, [replay.usage.disclaimer])), "暂无限制说明");
  }

  function detailLimitations() {
    return state.caseDetail && state.caseDetail.event ? state.caseDetail.event.limitations : [];
  }

  function clearCaseDetail(message, status) {
    state.caseDetail = null;
    elements.replayButton.disabled = true;
    removeReference("survival_assessment");
    clearSurvivalValues();
    const paragraph = document.createElement("p");
    paragraph.textContent = message;
    elements.caseSummary.replaceChildren(paragraph);
    elements.scenarioID.textContent = "尚未绑定合成场景";
    setAssessmentState(elements.survivalStatus, status || "idle", message);
  }

  function updateReplayAvailability() {
    elements.replayButton.disabled = !state.caseDetail || !state.modelCard;
  }

  function renderReplayReadiness() {
    updateReplayAvailability();
    if (!state.caseDetail) return;
    if (state.modelCardState === "ready") {
      setAssessmentState(elements.survivalStatus, "replay", "公开历史案例与合成匿名场景已就绪；尚未运行回放。");
      return;
    }
    if (state.modelCardState === "error") {
      setAssessmentState(elements.survivalStatus, "error", state.modelCardError || "生还评估模型卡不可用");
      return;
    }
    setAssessmentState(elements.survivalStatus, "loading", "案例已就绪，正在校验生还评估模型卡...");
  }

  function clearSurvivalResult(message) {
    state.replayRequest++;
    removeReference("survival_assessment");
    clearSurvivalValues();
    setAssessmentState(elements.survivalStatus, "idle", message || "尚未运行历史回放。" );
  }

  function clearSurvivalValues() {
    elements.score.textContent = "--";
    elements.probability.textContent = "--";
    elements.priority.textContent = "--";
    elements.scoreBand.textContent = "未知";
    elements.probabilityBand.textContent = "未知";
    elements.modelVersion.textContent = "未提供";
    elements.reviewStatus.textContent = "必须复核";
    renderTextList(elements.factors, [], "尚未运行历史回放。");
    renderTextList(elements.survivalLimitations, [], "尚未读取案例或评估限制。");
  }

  function replaceOptions(values) {
    const options = values.map(function (value) {
      const option = document.createElement("option");
      option.value = value.value;
      option.textContent = value.label;
      return option;
    });
    elements.caseSelect.replaceChildren.apply(elements.caseSelect, options);
  }

  function rememberReference(kind, id, label) {
    state.references.set(kind, { kind: kind, id: id, label: label });
    refreshReferences(kind);
  }

  function removeReference(kind) {
    state.references.delete(kind);
    refreshReferences();
  }

  function refreshReferences(selectedKind) {
    const current = elements.aiReference.value;
    const options = [];
    if (state.references.size === 0) options.push({ value: "", label: "先完成损失评估或历史回放" });
    state.references.forEach(function (value) { options.push({ value: value.kind + ":" + value.id, label: value.label }); });
    const nodes = options.map(function (value) {
      const option = document.createElement("option");
      option.value = value.value;
      option.textContent = value.label;
      return option;
    });
    elements.aiReference.replaceChildren.apply(elements.aiReference, nodes);
    const preferred = selectedKind && state.references.get(selectedKind);
    const preferredValue = preferred ? preferred.kind + ":" + preferred.id : current;
    if (preferredValue && Array.from(elements.aiReference.options).some(function (option) { return option.value === preferredValue; })) {
      elements.aiReference.value = preferredValue;
    }
    elements.aiButton.disabled = state.references.size === 0;
    clearAIResult("权威引用列表已更新，请重新生成解释。");
  }

  function renderTextList(container, values, emptyText) {
    const safe = Array.isArray(values) ? values.slice(0, 32) : [];
    const nodes = safe.map(function (value) {
      const item = document.createElement("li");
      item.textContent = String(value);
      return item;
    });
    if (nodes.length === 0) {
      const item = document.createElement("li");
      item.textContent = emptyText;
      nodes.push(item);
    }
    container.replaceChildren.apply(container, nodes);
  }

  function setAssessmentState(element, name, message) {
    element.className = "assessment-state assessment-state-" + name;
    element.textContent = message;
  }

  function validReplayUsage(value) {
    const fields = ["disclaimer", "liveUseAllowed", "mode", "syntheticInput"];
    return hasExactKeys(value, fields) && value.mode === "historical_replay" && value.syntheticInput === true &&
      value.liveUseAllowed === false && value.disclaimer ===
      "仅用于合成输入的历史案例回放和人工辅助，不得用于实时人员评估或自动放弃搜救";
  }

  function expectedSurvivalBands(score, elapsedMinutes) {
    const scoreBand = score >= 75 ? "high" : score >= 50 ? "moderate" : score >= 25 ? "low" : "very_low";
    let low = 0.05;
    let high = 0.14;
    let probabilityBand = "very_low";
    if (score >= 75) { low = 0.60; high = 0.85; probabilityBand = "high"; }
    else if (score >= 50) { low = 0.35; high = 0.59; probabilityBand = "moderate"; }
    else if (score >= 25) { low = 0.15; high = 0.34; probabilityBand = "low"; }
    const priority = score >= 75 || (elapsedMinutes <= 60 && score >= 55) ? "immediate" :
      score >= 50 ? "urgent" : score >= 25 ? "elevated" : "routine";
    return { scoreBand: scoreBand, low: low, high: high, probabilityBand: probabilityBand, priority: priority };
  }

  function sameUsage(left, right) {
    return validReplayUsage(left) && left.mode === right.mode && left.syntheticInput === right.syntheticInput &&
      left.liveUseAllowed === right.liveUseAllowed && left.disclaimer === right.disclaimer;
  }

  function validateTextArray(values, maximum, maxRunes, label) {
    if (!Array.isArray(values) || values.length > maximum || values.some(function (value) {
      return !validText(value, maxRunes);
    })) throw new Error(label + "契约无效");
    return values;
  }

  function uniqueTextValues(values) {
    const seen = new Set();
    return values.filter(function (value) {
      if (seen.has(value)) return false;
      seen.add(value);
      return true;
    });
  }

  function validID(value) { return typeof value === "string" && IDENTIFIER.test(value); }
  function validText(value, maxRunes) { return typeof value === "string" && value.trim() !== "" && Array.from(value).length <= maxRunes; }
  function optionalText(value, maxRunes) { return value === undefined || value === "" || validText(value, maxRunes); }
  function optionalSHA256(value) { return value === undefined || value === "" || (typeof value === "string" && SHA256.test(value)); }
  function hasOnlyKeys(value, allowed) {
    return value && typeof value === "object" && !Array.isArray(value) &&
      Object.keys(value).every(function (key) { return allowed.includes(key); });
  }
  function hasRequiredKeys(value, allowed, required) {
    return hasOnlyKeys(value, allowed) && required.every(function (key) {
      return Object.prototype.hasOwnProperty.call(value, key);
    });
  }
  function hasExactKeys(value, expected) {
    if (!hasOnlyKeys(value, expected)) return false;
    const actual = Object.keys(value).slice().sort();
    const wanted = expected.slice().sort();
    return sameStringArray(actual, wanted);
  }
  function sameStringArray(values, expected) {
    return Array.isArray(values) && values.length === expected.length &&
      values.every(function (value, index) { return value === expected[index]; });
  }
  function canonicalDecimal(value) { return typeof value === "string" && /^(?:0|[1-9][0-9]*)$/.test(value); }
  function validWGS84Point(value) {
    return hasOnlyKeys(value, ["longitude", "latitude"]) && finiteRange(value.longitude, -180, 180) &&
      finiteRange(value.latitude, -90, 90);
  }
  function validBBox(value) {
    return value === undefined || (Array.isArray(value) && value.length === 4 &&
      finiteRange(value[0], -180, 180) && finiteRange(value[1], -90, 90) &&
      finiteRange(value[2], -180, 180) && finiteRange(value[3], -90, 90) &&
      value[0] <= value[2] && value[1] <= value[3]);
  }
  function validHistoricalURL(value) {
    return Boolean(publicHTTPSURL(value, function (host) { return host === "usgs.gov" || host.endsWith(".usgs.gov"); }));
  }
  function publicHTTPSURL(value, hostAllowed) {
    if (typeof value !== "string" || value.length > 2048) return "";
    try {
      const parsed = new URL(value);
      const host = parsed.hostname.toLowerCase();
      if (parsed.protocol !== "https:" || (parsed.port && parsed.port !== "443") || parsed.username || parsed.password ||
        parsed.hash || !publicNetworkHost(host) || (hostAllowed && !hostAllowed(host))) return "";
      if (!Array.from(parsed.searchParams.keys()).every(function (key) { return !SENSITIVE_QUERY.test(key); })) return "";
      return parsed.href;
    } catch (_) {
      return "";
    }
  }

  function publicNetworkHost(host) {
    const value = host.replace(/^\[|\]$/g, "").replace(/\.$/, "").toLowerCase();
    if (!value || value === "localhost" || value.endsWith(".localhost") || value.endsWith(".local") ||
      value.endsWith(".internal")) return false;
    if (/^\d+(?:\.\d+){3}$/.test(value)) return publicIPv4(value);
    if (value.includes(":")) return publicIPv6(value);
    return true;
  }

  function publicIPv4(value) {
    const octets = value.split(".").map(Number);
    if (octets.length !== 4 || octets.some(function (part) { return !Number.isInteger(part) || part < 0 || part > 255; })) {
      return false;
    }
    const first = octets[0];
    const second = octets[1];
    if (first === 0 || first === 10 || first === 127 || first >= 224 ||
      (first === 100 && second >= 64 && second <= 127) || (first === 169 && second === 254) ||
      (first === 172 && second >= 16 && second <= 31) || (first === 192 && second === 168) ||
      (first === 198 && (second === 18 || second === 19))) return false;
    return !(first === 192 && second === 0 && octets[2] === 2) &&
      !(first === 198 && second === 51 && octets[2] === 100) &&
      !(first === 203 && second === 0 && octets[2] === 113);
  }

  function publicIPv6(value) {
    if (value.startsWith("::ffff:")) return publicMappedIPv4(value.slice(7));
    return value !== "::" && value !== "::1" && !/^f[cd]/.test(value) && !/^fe[89ab]/.test(value) &&
      !/^fe[c-f]/.test(value) && !value.startsWith("2001:db8:");
  }

  function publicMappedIPv4(value) {
    if (value.includes(".")) return publicIPv4(value.slice(value.lastIndexOf(":") + 1));
    const words = value.split(":");
    if (words.length !== 2 || words.some(function (part) { return !/^[0-9a-f]{1,4}$/.test(part); })) return false;
    const high = Number.parseInt(words[0], 16);
    const low = Number.parseInt(words[1], 16);
    return publicIPv4([(high >>> 8) & 255, high & 255, (low >>> 8) & 255, low & 255].join("."));
  }
  function parseReference(value) {
    if (typeof value !== "string") return null;
    const separator = value.indexOf(":");
    if (separator <= 0) return null;
    const reference = { kind: value.slice(0, separator), id: value.slice(separator + 1) };
    return AUTHORITY_SCHEMAS[reference.kind] && validID(reference.id) ? reference : null;
  }
  function sha256Fallback(bytes) {
    const constants = [
      0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
      0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
      0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
      0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
      0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
      0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
    ];
    const length = Math.ceil((bytes.length + 9) / 64) * 64;
    const padded = new Uint8Array(length);
    padded.set(bytes);
    padded[bytes.length] = 0x80;
    const view = new DataView(padded.buffer);
    const bitLength = bytes.length * 8;
    view.setUint32(length - 8, Math.floor(bitLength / 0x100000000));
    view.setUint32(length - 4, bitLength >>> 0);
    const hash = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    const words = new Uint32Array(64);
    for (let offset = 0; offset < length; offset += 64) sha256Block(view, offset, words, hash, constants);
    const result = new Uint8Array(32);
    const resultView = new DataView(result.buffer);
    hash.forEach(function (value, index) { resultView.setUint32(index * 4, value); });
    return result;
  }

  function sha256Block(view, offset, words, hash, constants) {
    for (let index = 0; index < 16; index++) words[index] = view.getUint32(offset + index * 4);
    for (let index = 16; index < 64; index++) {
      const left = rotateRight(words[index - 15], 7) ^ rotateRight(words[index - 15], 18) ^ (words[index - 15] >>> 3);
      const right = rotateRight(words[index - 2], 17) ^ rotateRight(words[index - 2], 19) ^ (words[index - 2] >>> 10);
      words[index] = (words[index - 16] + left + words[index - 7] + right) >>> 0;
    }
    let a = hash[0]; let b = hash[1]; let c = hash[2]; let d = hash[3];
    let e = hash[4]; let f = hash[5]; let g = hash[6]; let h = hash[7];
    for (let index = 0; index < 64; index++) {
      const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
      const choice = (e & f) ^ (~e & g);
      const temporary1 = (h + sum1 + choice + constants[index] + words[index]) >>> 0;
      const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
      const majority = (a & b) ^ (a & c) ^ (b & c);
      const temporary2 = (sum0 + majority) >>> 0;
      h = g; g = f; f = e; e = (d + temporary1) >>> 0;
      d = c; c = b; b = a; a = (temporary1 + temporary2) >>> 0;
    }
    hash[0] = (hash[0] + a) >>> 0; hash[1] = (hash[1] + b) >>> 0;
    hash[2] = (hash[2] + c) >>> 0; hash[3] = (hash[3] + d) >>> 0;
    hash[4] = (hash[4] + e) >>> 0; hash[5] = (hash[5] + f) >>> 0;
    hash[6] = (hash[6] + g) >>> 0; hash[7] = (hash[7] + h) >>> 0;
  }

  function rotateRight(value, bits) { return (value >>> bits) | (value << (32 - bits)); }
  function bytesToHex(values) { return Array.from(values, function (value) { return value.toString(16).padStart(2, "0"); }).join(""); }
  function strictUTC(value, rejectFuture) {
    if (typeof value !== "string") return false;
    const match = STRICT_UTC_RFC3339.exec(value);
    const timestamp = match ? Date.parse(value) : Number.NaN;
    if (!match || !Number.isFinite(timestamp) || !calendarMatches(timestamp, match)) return false;
    return !rejectFuture || timestamp <= Date.now();
  }

  function calendarMatches(timestamp, match) {
    const value = new Date(timestamp);
    return Number(match[1]) > 0 && value.getUTCFullYear() === Number(match[1]) &&
      value.getUTCMonth() + 1 === Number(match[2]) && value.getUTCDate() === Number(match[3]) &&
      value.getUTCHours() === Number(match[4]) && value.getUTCMinutes() === Number(match[5]) &&
      value.getUTCSeconds() === Number(match[6]);
  }
  function safeInteger(value, minimum, maximum) { return Number.isSafeInteger(value) && value >= minimum && value <= maximum; }
  function validReportedCount(value) { return value === null || safeInteger(value, 0, MAX_EVENT_COUNT); }
  function finiteRange(value, minimum, maximum) { return typeof value === "number" && Number.isFinite(value) && value >= minimum && value <= maximum; }

  function appendText(parent, tag, value) {
    const child = document.createElement(tag);
    child.textContent = value;
    parent.appendChild(child);
  }

  function requestJSON(endpoint, options) {
    if (!window.AIGDM || !window.AIGDM.requestJSON) return Promise.reject(new Error("浏览器 API 客户端未加载"));
    const settings = Object.assign({ timeoutMs: requestTimeout(), maxResponseBytes: responseLimit() }, options || {});
    return window.AIGDM.requestJSON(endpoint, settings);
  }

  function requestTimeout() {
    const value = Number(root.dataset.requestTimeoutMs);
    return Number.isSafeInteger(value) && value >= 25 && value <= 60000 ? value : 30000;
  }

  function aiRequestTimeout() {
    const value = Number(root.dataset.aiRequestTimeoutMs);
    return Number.isSafeInteger(value) && value >= 25 && value <= 60000 ? value : 45000;
  }

  function responseLimit() {
    const value = Number(root.dataset.maxResponseBytes);
    return Number.isSafeInteger(value) && value > 0 && value <= MAX_RESPONSE_BYTES ? value : MAX_RESPONSE_BYTES;
  }

  function caseLabel(event) { return formatDate(event.eventDate) + " / " + event.adminArea + " / " + event.category; }
  function formatDate(value) { return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date(value)); }
  function formatTime(value) { return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", dateStyle: "short", timeStyle: "medium" }).format(new Date(value)); }
  function percent(value) { return (value * 100).toFixed(0) + "%"; }
  function signalText(value) { return ({ yes: "是", no: "否", unknown: "未知" })[value] || "未知"; }
  function injuryText(value) { return ({ none: "无", minor: "轻微", severe: "严重", critical: "危重", unknown: "未知" })[value] || "未知"; }
  function reportedCountText(value) { return value === null ? "未知 / 未按统一口径披露" : String(value); }
  function priorityText(value) { return ({ routine: "常规", elevated: "提高", urgent: "紧急", immediate: "立即" })[value] || value; }
  function bandText(value) { return ({ very_low: "很低", low: "低", moderate: "中", high: "高", very_high: "很高" })[value] || value; }
  function authorityKindText(value) { return ({ hazard_snapshot: "风险快照", evacuation_route: "疏散路线", loss_assessment: "损失评估", survival_assessment: "历史生还回放" })[value] || "未知"; }
  function errorMessage(error) { return error && error.message ? error.message : "接口暂时不可用"; }
})();
