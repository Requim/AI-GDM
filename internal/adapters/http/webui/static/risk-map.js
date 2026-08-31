(function () {
  "use strict";

  const MAX_VISIBLE_ZONES = 3000;
  const MAX_SOURCE_ZONES = 100000;
  const MAX_ZONE_VERTICES = 5000;
  const MAX_TOTAL_VERTICES = 200000;
  const MAX_GEOMETRY_BYTES = 512 * 1024;
  const MAX_RESPONSE_BYTES = 8 * 1024 * 1024;
  const MAX_EXPIRY_TIMER_MS = 60 * 60 * 1000;
  const MAX_LOSS_REFERENCE_AGE_MS = 72 * 60 * 60 * 1000;
  const RISK_SNAPSHOT_EVENT = "ai-gdm:risk-snapshot";
  const IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
  const STRICT_UTC_RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?Z$/;
  const LEVEL_TEXT = { low: "低", moderate: "中", high: "高", very_high: "很高" };
  const LEVEL_COLOR = { low: "#4ecb71", moderate: "#f0b85a", high: "#ef6a5b", very_high: "#d64882" };
  const root = document.getElementById("risk-map");
  if (!root) return;

  const elements = collectElements();
  const map = createMap(elements.canvas);
  let riskLayer = window.L.layerGroup().addTo(map);
  let expiryTimer = 0;
  let activeData = null;

  elements.refresh.addEventListener("click", loadRisk);
  loadRisk();

  function collectElements() {
    return {
      canvas: document.getElementById("risk-map-canvas"),
      refresh: document.getElementById("risk-map-refresh"),
      message: document.getElementById("risk-map-message"),
      visibleCount: document.getElementById("risk-visible-count"),
      totalCount: document.getElementById("risk-total-count"),
      coverageScope: document.getElementById("risk-coverage-scope"),
      decision: document.getElementById("risk-decision-level"),
      assessment: document.getElementById("risk-assessment-status"),
      dataStatus: document.getElementById("risk-data-status"),
      confidence: document.getElementById("risk-confidence"),
      model: document.getElementById("risk-model"),
      source: document.getElementById("risk-source"),
      fetchedAt: document.getElementById("risk-fetched-at"),
      validTo: document.getElementById("risk-valid-to"),
      crs: document.getElementById("risk-crs"),
      ruleVersion: document.getElementById("risk-rule-version"),
      limitations: document.getElementById("risk-limitations-list")
    };
  }

  function createMap(container) {
    if (!window.L) {
      setState("unavailable", "地图组件未加载，风险详情仍可通过 API 查询。" );
      throw new Error("Leaflet unavailable");
    }
    const value = window.L.map(container, { preferCanvas: true, zoomControl: true }).setView([35.5, 104.5], 4);
    window.L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
      maxZoom: 18, attribution: "&copy; OpenStreetMap contributors"
    }).addTo(value);
    return value;
  }

  async function loadRisk() {
    elements.refresh.disabled = true;
    setState("loading", "正在读取最后成功的风险分析...");
    try {
      const data = await requestRisk(root.dataset.riskEndpoint);
      renderRisk(data);
    } catch (error) {
      renderUnavailable(error instanceof Error ? error.message : "风险数据暂时不可用");
    } finally {
      elements.refresh.disabled = false;
    }
  }

  async function requestRisk(endpoint) {
    if (!window.AIGDM || !window.AIGDM.requestJSON) throw new Error("浏览器 API 客户端未加载");
    const payload = await window.AIGDM.requestJSON(endpoint, {
      timeoutMs: requestTimeout(), maxResponseBytes: MAX_RESPONSE_BYTES
    });
    return validateRiskPayload(payload);
  }

  function validateRiskPayload(payload) {
    const data = payload && payload.data;
    if (!data || !data.snapshot || !validSnapshotID(data.snapshot.id) ||
      !Array.isArray(data.zones) || !validLimits(data.limits) || !validCoverage(data.coverage)) {
      throw new Error("风险地图接口契约不完整，已按不可用处理");
    }
    validateCounts(data);
    requireValidTo(data);
    validateGeometryLimits(data.zones, data.limits);
    return data;
  }

  function validCoverage(value) {
    if (!value || value.viewportIndependent !== true || typeof value.label !== "string" ||
      value.label.trim() === "" || value.label.length > 256) return false;
    if (value.mode === "bounding_box") return true;
    return value.mode === "administrative_boundary" && value.regionCode === "CN" &&
      value.boundaryType === "ADM0" && validSnapshotID(value.boundaryId) &&
      typeof value.boundaryVersion === "string" && value.boundaryVersion.trim() !== "" &&
      validCoverageText(value.source) && validCoverageText(value.license) &&
      value.label.includes("（" + value.boundaryVersion + "）");
  }

  function validCoverageText(value) {
    return typeof value === "string" && value.trim() === value && value !== "" && value.length <= 2048;
  }

  function validateCounts(data) {
    const total = safeCount(data.totalZoneCount, "风险区总数");
    const visible = safeCount(data.visibleZoneCount, "可见风险区数");
    const omitted = safeCount(data.omittedZoneCount, "省略风险区数");
    const complex = safeCount(data.omittedComplexZoneCount, "复杂几何省略数");
    const payload = safeCount(data.omittedPayloadZoneCount, "负载省略数");
    if (visible !== data.zones.length || total !== visible + omitted || omitted !== complex + payload ||
      total > data.limits.maxSourceZones || visible > data.limits.maxZones) {
      throw new Error("风险区计数契约不一致，已按不可用处理");
    }
  }

  function safeCount(value, label) {
    if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
      throw new Error(label + "无效");
    }
    return value;
  }

  function validLimits(limits) {
    return limits && positiveLimit(limits.maxZones, MAX_VISIBLE_ZONES) &&
      positiveLimit(limits.maxSourceZones, MAX_SOURCE_ZONES) &&
      positiveLimit(limits.maxZoneVertices, MAX_ZONE_VERTICES) &&
      positiveLimit(limits.maxTotalVertices, MAX_TOTAL_VERTICES) &&
      positiveLimit(limits.maxGeometryBytes, MAX_GEOMETRY_BYTES) &&
      positiveLimit(limits.maxResponseBytes, MAX_RESPONSE_BYTES);
  }

  function positiveLimit(value, maximum) {
    return typeof value === "number" && Number.isSafeInteger(value) && value > 0 && value <= maximum;
  }

  function requireValidTo(data) {
    const snapshot = data.snapshot || {};
    const source = snapshot.source || {};
    const snapshotTimestamp = parseStrictUTC(snapshot.validTo);
    const sourceTimestamp = parseStrictUTC(source.validTo);
    if (snapshot.validTo !== source.validTo || snapshotTimestamp !== sourceTimestamp) {
      throw new Error("风险快照与来源有效期不一致，已按不可用处理");
    }
    return snapshotTimestamp;
  }

  function parseStrictUTC(value) {
    if (typeof value !== "string") throw new Error("风险数据有效期缺失或无效，已按不可用处理");
    const match = STRICT_UTC_RFC3339.exec(value);
    const timestamp = match ? Date.parse(value) : Number.NaN;
    if (!match || !Number.isFinite(timestamp) || !calendarMatches(timestamp, match)) {
      throw new Error("风险数据有效期缺失或无效，已按不可用处理");
    }
    return timestamp;
  }

  function calendarMatches(timestamp, match) {
    const value = new Date(timestamp);
    return Number(match[1]) > 0 && value.getUTCFullYear() === Number(match[1]) &&
      value.getUTCMonth() + 1 === Number(match[2]) && value.getUTCDate() === Number(match[3]) &&
      value.getUTCHours() === Number(match[4]) && value.getUTCMinutes() === Number(match[5]) &&
      value.getUTCSeconds() === Number(match[6]);
  }

  function validateGeometryLimits(zones, limits) {
    let totalVertices = 0;
    zones.forEach(function (zone) {
      if (!validZone(zone)) throw new Error("风险区几何结构无效");
      const bytes = new TextEncoder().encode(JSON.stringify(zone.geometry.coordinates)).byteLength;
      if (bytes > limits.maxGeometryBytes) throw new Error("单个风险区几何超过浏览器字节上限");
      const vertices = validateGeometry(zone.geometry, limits.maxZoneVertices);
      totalVertices += vertices;
      if (totalVertices > limits.maxTotalVertices) {
        throw new Error("风险区几何总量超过浏览器安全上限");
      }
    });
  }

  function validateGeometry(geometry, maximum) {
    const polygons = geometry.type === "Polygon" ? [geometry.coordinates] : geometry.coordinates;
    if (!Array.isArray(polygons) || polygons.length === 0) throw new Error("风险区不含有效多边形");
    let count = 0;
    for (const polygon of polygons) {
      count += validatePolygon(polygon, maximum - count);
      if (count > maximum) throw new Error("单个风险区几何超过浏览器顶点上限");
    }
    return count;
  }

  function validatePolygon(polygon, maximum) {
    if (!Array.isArray(polygon) || polygon.length === 0) throw new Error("风险区 Polygon 不含环");
    let count = 0;
    for (const ring of polygon) {
      count += validateRing(ring);
      if (count > maximum) throw new Error("单个风险区几何超过浏览器顶点上限");
    }
    return count;
  }

  function validateRing(ring) {
    if (!Array.isArray(ring) || ring.length < 4) throw new Error("风险区 Polygon 环点数不足");
    let area = 0;
    for (let index = 0; index < ring.length; index++) {
      validateCoordinate(ring[index]);
      if (index < ring.length - 1) {
        area += ring[index][0] * ring[index + 1][1] - ring[index + 1][0] * ring[index][1];
      }
    }
    if (!sameCoordinate(ring[0], ring[ring.length - 1]) || Math.abs(area) < 1e-12) {
      throw new Error("风险区 Polygon 环未闭合或面积为零");
    }
    return ring.length;
  }

  function validateCoordinate(value) {
    if (!Array.isArray(value) || value.length < 2 || !Number.isFinite(value[0]) || !Number.isFinite(value[1]) ||
      value[0] < -180 || value[0] > 180 || value[1] < -90 || value[1] > 90) {
      throw new Error("风险区坐标超出 WGS84 范围");
    }
  }

  function sameCoordinate(left, right) {
    return left[0] === right[0] && left[1] === right[1];
  }

  function renderRisk(data) {
    activeData = data;
    const drawn = renderLayer(data.zones);
    const state = snapshotState(data);
    renderMetadata(data, state);
    publishRiskSnapshot(data, state);
    renderCountSummary(data, drawn);
    scheduleExpiry(data);
    if (data.zones.length === 0) {
      renderEmptyRiskMap(data, state);
      return;
    }
    if (drawn === 0) {
      renderUnavailable("风险区几何无效，未绘制任何图层");
      return;
    }
    const truncated = data.totalZoneCount > drawn ? "，风险较高区域优先显示；未绘制不代表无风险" : "";
    const messages = { expired: "当前展示已过期的最后成功数据", stale: "当前展示陈旧的最后成功数据",
      fallback: "当前使用未过期的最后成功回退数据", current: "风险图层已更新" };
    setState(state === "expired" ? "stale" : state, messages[state] + truncated + "。" );
  }

  function renderCountSummary(data, drawn) {
    const omitted = Math.max(0, data.totalZoneCount - drawn);
    const reasons = omitted > 0 ? "（几何复杂 " + data.omittedComplexZoneCount.toLocaleString("zh-CN") +
      "；数量、顶点或响应大小上限 " + data.omittedPayloadZoneCount.toLocaleString("zh-CN") + "）" : "";
    elements.visibleCount.textContent = "本次地图绘制 " + drawn.toLocaleString("zh-CN") + " 个风险区";
    elements.totalCount.textContent = "本快照范围内共 " + data.totalZoneCount.toLocaleString("zh-CN") +
      " · 未绘制 " + omitted.toLocaleString("zh-CN") + reasons;
    const provenance = data.coverage.mode === "administrative_boundary" ?
      "；来源：" + data.coverage.source + "；许可：" + data.coverage.license : "";
    elements.coverageScope.textContent = "处理范围：" + data.coverage.label + provenance +
      "；与地图缩放和拖动无关，非官方国界依据";
  }

  function renderEmptyRiskMap(data, state) {
    if (data.totalZoneCount > 0 && data.omittedZoneCount === data.totalZoneCount) {
      const expired = state === "expired" ? "风险数据已过期，且" : "";
      setState("unavailable", expired + "风险快照包含风险区，但全部因地图安全上限被省略，地图当前不可用，不得据此降低风险判断。" );
      return;
    }
    const messages = {
      expired: "风险数据已过期，原快照未生成达到阈值的风险区。",
      stale: "陈旧快照未生成达到阈值的风险区。",
      fallback: "回退快照未生成达到阈值的风险区。",
      current: "当前快照未生成达到阈值的风险区。"
    };
    setState(state === "expired" ? "stale" : state, messages[state]);
  }

  function renderLayer(zones) {
    clearRiskLayer();
    const features = zones.map(function (zone) {
      return { type: "Feature", geometry: zone.geometry, properties: zone };
    });
    if (features.length === 0) return 0;
    riskLayer = window.L.geoJSON({ type: "FeatureCollection", features: features }, {
      style: function (feature) { return zoneStyle(feature.properties.riskLevel); },
      onEachFeature: function (feature, layer) { layer.bindPopup(popupNode(feature.properties)); }
    }).addTo(map);
    const bounds = riskLayer.getBounds();
    if (bounds.isValid()) map.fitBounds(bounds, { padding: [20, 20], maxZoom: 9 });
    return riskLayer.getLayers().length;
  }

  function validZone(zone) {
    return zone && zone.geometry && (zone.geometry.type === "Polygon" || zone.geometry.type === "MultiPolygon") &&
      Array.isArray(zone.geometry.coordinates);
  }

  function zoneStyle(level) {
    const color = LEVEL_COLOR[level] || "#7b8984";
    return { color: color, weight: 1, fillColor: color, fillOpacity: 0.46 };
  }

  function popupNode(zone) {
    const node = document.createElement("div");
    node.className = "risk-popup";
    appendText(node, "strong", "风险等级：" + levelText(zone.riskLevel));
    appendText(node, "span", "区域标识：" + textValue(zone.id));
    appendText(node, "span", "概率：" + probabilityRange(zone));
    appendText(node, "span", "面积：" + areaText(zone));
    return node;
  }

  function appendText(parent, tag, value) {
    const child = document.createElement(tag);
    child.textContent = value;
    parent.appendChild(child);
  }

  function probabilityRange(zone) {
    return percent(zone.probabilityMinimum) + " / " + percent(zone.probabilityMean) + " / " + percent(zone.probabilityMaximum);
  }

  function percent(value) {
    const number = Number(value);
    return Number.isFinite(number) ? (number * 100).toFixed(1) + "%" : "未提供";
  }

  function areaText(zone) {
    if (!zone.areaCalculated) return "未计算";
    const value = Number(zone.areaSquareMeters);
    return Number.isFinite(value) ? (value / 1000000).toFixed(2) + " km²" : "未提供";
  }

  function renderMetadata(data, state) {
    const snapshot = data.snapshot || {};
    const source = snapshot.source || {};
    const assessment = data.assessment || {};
    const decision = assessment.decision || null;
    const level = decision ? levelText(decision.level) : "不可判定";
    const expired = state === "expired";
    const stale = state === "stale";
    elements.decision.textContent = expired ? "已过期 / " + level : stale ? "陈旧 / " + level : level;
    elements.assessment.textContent = expired ? "数据已过期，仅保留图层供人工复核" :
      stale ? "数据陈旧，仅保留图层供人工复核" :
      state === "fallback" ? "正在使用最后成功回退数据，需人工复核" : assessmentText(assessment.status);
    elements.dataStatus.textContent = dataStatusText(assessment.dataStatus, state);
    elements.confidence.textContent = expired ? "已过期，原输入质量不代表当前状态" :
      stale ? "数据陈旧，原输入质量需人工复核" : confidenceText(assessment.confidence);
    elements.model.textContent = joinNonEmpty([snapshot.modelName, snapshot.modelVersion]);
    elements.source.textContent = joinNonEmpty([source.provider, source.dataset, source.datasetVersion]);
    elements.fetchedAt.textContent = formatTime(source.fetchedAt);
    elements.validTo.textContent = formatTime(snapshot.validTo);
    elements.crs.textContent = textValue(source.crs || "WGS84");
    elements.ruleVersion.textContent = textValue(assessment.ruleVersion);
    renderLimitations(snapshot.limitations, source.limitations, assessment.limitations, data.mapLimitations,
      expired ? ["风险数据已跨过有效期，禁止作为当前预警结论使用"] :
        stale ? ["风险数据来源标记为陈旧，需人工复核后使用"] : []);
  }

  function snapshotState(data) {
    const snapshot = data.snapshot || {};
    const source = snapshot.source || {};
    const assessment = data.assessment || {};
    if (assessment.dataStatus === "expired" || Date.now() >= requireValidTo(data)) return "expired";
    if (assessment.dataStatus === "fallback") return "fallback";
    return snapshot.status === "stale" || source.stale ? "stale" : "current";
  }

  function scheduleExpiry(data) {
    clearExpiryTimer();
    const validTo = requireValidTo(data);
    const check = function () {
      if (activeData !== data) return;
      const remaining = validTo - Date.now();
      if (remaining <= 0) {
        renderExpiredRisk(data);
        scheduleReferenceExpiry(data, validTo);
        return;
      }
      expiryTimer = window.setTimeout(check, Math.min(remaining + 25, MAX_EXPIRY_TIMER_MS));
    };
    check();
  }

  function scheduleReferenceExpiry(data, validTo) {
    const check = function () {
      if (activeData !== data) return;
      const remaining = validTo + MAX_LOSS_REFERENCE_AGE_MS - Date.now();
      if (remaining <= 0) {
        publishRiskSnapshot(data, "expired");
        return;
      }
      expiryTimer = window.setTimeout(check, Math.min(remaining + 25, MAX_EXPIRY_TIMER_MS));
    };
    check();
  }

  function renderExpiredRisk(data) {
    renderMetadata(data, "expired");
    publishRiskSnapshot(data, "expired");
    if (data.zones.length === 0) {
      renderEmptyRiskMap(data, "expired");
      return;
    }
    setState("stale", "风险数据已跨过有效期，当前仅展示最后成功图层供人工复核。" );
  }

  function clearExpiryTimer() {
    if (expiryTimer) window.clearTimeout(expiryTimer);
    expiryTimer = 0;
  }

  function renderLimitations() {
    const values = Array.prototype.slice.call(arguments).flat().filter(function (value) {
      return typeof value === "string" && value.trim() !== "";
    });
    values.unshift("风险图层仅用于辅助研判，不构成官方预警。");
    const unique = Array.from(new Set(values)).slice(0, 10);
    elements.limitations.replaceChildren();
    unique.forEach(function (value) { appendText(elements.limitations, "li", value); });
  }

  function renderUnavailable(message) {
    activeData = null;
    publishRiskSnapshot(null, "unavailable");
    clearExpiryTimer();
    clearRiskLayer();
    map.setView([35.5, 104.5], 4);
    setState("unavailable", sentence(message) + " 页面不会使用模拟风险区替代。" );
    elements.visibleCount.textContent = "未显示风险区";
    elements.totalCount.textContent = "总数不可用";
    elements.coverageScope.textContent = "统计范围不可用";
    elements.decision.textContent = "不可用";
    elements.assessment.textContent = "没有可用的当前风险结果";
    elements.dataStatus.textContent = "不可用";
    elements.confidence.textContent = "未提供";
    elements.model.textContent = "未提供";
    elements.source.textContent = "未提供";
    elements.fetchedAt.textContent = "未提供";
    elements.validTo.textContent = "未提供";
    elements.crs.textContent = "WGS84";
    elements.ruleVersion.textContent = "未提供";
    renderLimitations(["风险数据不可用，禁止沿用页面上一次成功结果"]);
  }

  function clearRiskLayer() {
    if (riskLayer) map.removeLayer(riskLayer);
    riskLayer = window.L.layerGroup().addTo(map);
  }

  function publishRiskSnapshot(data, state) {
    const snapshot = data && data.snapshot;
    const available = Boolean(snapshot && validSnapshotID(snapshot.id) &&
      (state === "current" || state === "fallback"));
    const referenceEligible = Boolean(snapshot && validSnapshotID(snapshot.id) && state === "expired" &&
      Date.now() - requireValidTo(data) <= MAX_LOSS_REFERENCE_AGE_MS);
    const snapshotID = available || referenceEligible ? snapshot.id : "";
    root.dataset.currentSnapshotId = available ? snapshot.id : "";
    root.dataset.currentSnapshotState = state;
    root.dataset.referenceSnapshotId = referenceEligible ? snapshot.id : "";
    document.dispatchEvent(new CustomEvent(RISK_SNAPSHOT_EVENT, {
      detail: { available: available, referenceEligible: referenceEligible, snapshotId: snapshotID, state: state }
    }));
  }

  function setState(state, message) {
    elements.message.className = "map-state map-state-" + state;
    elements.message.textContent = message;
  }

  function requestTimeout() {
    const value = Number(root.dataset.requestTimeoutMs);
    return Number.isFinite(value) && value >= 25 && value <= 60000 ? value : 20000;
  }

  function sentence(value) {
    const text = String(value || "风险数据暂时不可用").trim();
    return /[。！？]$/.test(text) ? text : text + "。";
  }

  function levelText(value) { return LEVEL_TEXT[value] || textValue(value); }

  function assessmentText(value) {
    return ({ available: "研判可用", degraded: "降级研判，需重点复核", insufficient_data: "数据不足，不提供可操作等级" })[value] || "状态未提供";
  }

  function dataStatusText(value, state) {
    if (state === "expired") return "数据已过期";
    if (state === "stale") return "数据陈旧";
    return ({ current: "当前数据", fallback: "最后成功数据回退", expired: "数据已过期" })[value] || "未知";
  }

  function confidenceText(value) {
    const level = value && value.level;
    return ({ high: "高（输入质量）", medium: "中（输入质量）", low: "低（输入质量）", unavailable: "不可用" })[level] || "未提供";
  }

  function joinNonEmpty(values) {
    const result = values.filter(function (value) { return typeof value === "string" && value.trim() !== ""; });
    return result.length ? result.join(" / ") : "未提供";
  }

  function formatTime(value) {
    const parsed = new Date(value);
    if (!value || Number.isNaN(parsed.getTime())) return "未提供";
    return new Intl.DateTimeFormat("zh-CN", {
      timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false
    }).format(parsed) + " UTC+8";
  }

  function textValue(value) {
    return value === undefined || value === null || String(value).trim() === "" ? "未提供" : String(value);
  }

  function validSnapshotID(value) { return typeof value === "string" && IDENTIFIER.test(value); }
})();
