(function () {
  "use strict";

  const MAX_VISIBLE_ZONES = 3000;
  const LEVEL_ORDER = { low: 1, moderate: 2, high: 3, very_high: 4 };
  const LEVEL_TEXT = { low: "低", moderate: "中", high: "高", very_high: "很高" };
  const LEVEL_COLOR = { low: "#4ecb71", moderate: "#f0b85a", high: "#ef6a5b", very_high: "#d64882" };
  const root = document.getElementById("risk-map");
  if (!root) return;

  const elements = collectElements();
  const map = createMap(elements.canvas);
  let riskLayer = window.L.layerGroup().addTo(map);

  elements.refresh.addEventListener("click", loadRisk);
  loadRisk();

  function collectElements() {
    return {
      canvas: document.getElementById("risk-map-canvas"),
      refresh: document.getElementById("risk-map-refresh"),
      message: document.getElementById("risk-map-message"),
      visibleCount: document.getElementById("risk-visible-count"),
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
      maxZoom: 18,
      attribution: "&copy; OpenStreetMap contributors"
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
      clearRiskLayer();
      renderUnavailable(error instanceof Error ? error.message : "风险数据暂时不可用");
    } finally {
      elements.refresh.disabled = false;
    }
  }

  async function requestRisk(endpoint) {
    const controller = new AbortController();
    const timer = window.setTimeout(function () { controller.abort(); }, 20000);
    try {
      const response = await fetch(endpoint, {
        headers: { Accept: "application/json" }, cache: "no-store", signal: controller.signal
      });
      const payload = await parsePayload(response);
      if (!response.ok) throw new Error(apiMessage(response.status, payload));
      if (!payload || !payload.data || !payload.data.snapshot || !Array.isArray(payload.data.zones)) {
        throw new Error("风险接口返回的数据结构无效");
      }
      return payload.data;
    } catch (error) {
      if (error && error.name === "AbortError") throw new Error("读取风险数据超时");
      throw error;
    } finally {
      window.clearTimeout(timer);
    }
  }

  async function parsePayload(response) {
    const text = await response.text();
    if (!text) return null;
    try { return JSON.parse(text); } catch (_) { return null; }
  }

  function apiMessage(status, payload) {
    if (payload && payload.error && payload.error.message) return payload.error.message;
    if (status === 404) return "尚未生成实时风险数据";
    if (status === 503) return "实时风险数据不足或供应商暂时不可用";
    return "风险接口请求失败（HTTP " + status + "）";
  }

  function renderRisk(data) {
    const zones = data.zones.slice().sort(compareZones);
    const visible = zones.slice(0, MAX_VISIBLE_ZONES);
    const drawn = renderLayer(visible);
    renderMetadata(data);
    elements.visibleCount.textContent = "已显示 " + drawn + " / " + zones.length + " 个风险区";
    if (zones.length === 0) {
      setState(snapshotState(data) === "stale" ? "stale" : "current", "当前快照未生成达到阈值的风险区。" );
      return;
    }
    if (drawn === 0) {
      setState("unavailable", "风险区几何无效，未绘制任何图层。" );
      return;
    }
    const truncated = zones.length > visible.length ? "，为保障浏览器性能已按风险等级截断" : "";
    const stale = snapshotState(data) === "stale";
    setState(stale ? "stale" : "current", (stale ? "当前展示最后成功但已过期的数据" : "风险图层已更新") + truncated + "。" );
  }

  function compareZones(left, right) {
    const level = (LEVEL_ORDER[right.riskLevel] || 0) - (LEVEL_ORDER[left.riskLevel] || 0);
    if (level !== 0) return level;
    return Number(right.probabilityMaximum || 0) - Number(left.probabilityMaximum || 0);
  }

  function renderLayer(zones) {
    clearRiskLayer();
    const features = zones.filter(validZone).map(function (zone) {
      return { type: "Feature", geometry: zone.geometry, properties: zone };
    });
    if (features.length === 0) return 0;
    riskLayer = window.L.geoJSON({ type: "FeatureCollection", features: features }, {
      style: function (feature) { return zoneStyle(feature.properties.riskLevel); },
      onEachFeature: function (feature, layer) { layer.bindPopup(popupNode(feature.properties)); }
    }).addTo(map);
    const bounds = riskLayer.getBounds();
    if (bounds.isValid()) map.fitBounds(bounds, { padding: [20, 20], maxZoom: 9 });
    return features.length;
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

  function renderMetadata(data) {
    const snapshot = data.snapshot || {};
    const source = snapshot.source || {};
    const assessment = data.assessment || {};
    const decision = assessment.decision || null;
    elements.decision.textContent = decision ? levelText(decision.level) : "不可判定";
    elements.assessment.textContent = assessmentText(assessment.status);
    elements.dataStatus.textContent = dataStatusText(assessment.dataStatus, snapshotState(data));
    elements.confidence.textContent = confidenceText(assessment.confidence);
    elements.model.textContent = joinNonEmpty([snapshot.modelName, snapshot.modelVersion]);
    elements.source.textContent = joinNonEmpty([source.provider, source.dataset, source.datasetVersion]);
    elements.fetchedAt.textContent = formatTime(source.fetchedAt);
    elements.validTo.textContent = formatTime(snapshot.validTo || source.validTo);
    elements.crs.textContent = textValue(source.crs || "WGS84");
    elements.ruleVersion.textContent = textValue(assessment.ruleVersion);
    renderLimitations(snapshot.limitations, source.limitations, assessment.limitations);
  }

  function snapshotState(data) {
    const snapshot = data.snapshot || {};
    const source = snapshot.source || {};
    const assessment = data.assessment || {};
    const validTo = Date.parse(snapshot.validTo || source.validTo || "");
    return snapshot.status === "stale" || source.stale || assessment.dataStatus === "expired" ||
      (Number.isFinite(validTo) && Date.now() > validTo) ? "stale" : "current";
  }

  function renderLimitations() {
    const values = Array.prototype.slice.call(arguments).flat().filter(function (value) {
      return typeof value === "string" && value.trim() !== "";
    });
    values.unshift("风险图层仅用于辅助研判，不构成官方预警。");
    const unique = Array.from(new Set(values)).slice(0, 8);
    elements.limitations.replaceChildren();
    unique.forEach(function (value) { appendText(elements.limitations, "li", value); });
  }

  function renderUnavailable(message) {
    setState("unavailable", message + "。页面不会使用模拟风险区替代。" );
    elements.visibleCount.textContent = "未显示风险区";
    elements.decision.textContent = "不可用";
    elements.assessment.textContent = "等待最后成功数据";
    elements.dataStatus.textContent = "不可用";
  }

  function clearRiskLayer() {
    if (riskLayer) map.removeLayer(riskLayer);
    riskLayer = window.L.layerGroup().addTo(map);
  }

  function setState(state, message) {
    elements.message.className = "map-state map-state-" + state;
    elements.message.textContent = message;
  }

  function levelText(value) { return LEVEL_TEXT[value] || textValue(value); }

  function assessmentText(value) {
    return ({ available: "研判可用", degraded: "降级研判，需重点复核", insufficient_data: "数据不足，不提供可操作等级" })[value] || "状态未提供";
  }

  function dataStatusText(value, fallback) {
    return ({ current: "当前数据", fallback: "最后成功数据回退", expired: "数据已过期" })[value] || (fallback === "stale" ? "数据已过期" : "未知");
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
})();
