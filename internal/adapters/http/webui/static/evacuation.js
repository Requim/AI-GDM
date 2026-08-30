(function () {
  "use strict";

  const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
  const MAX_FACILITIES = 50;
  const MAX_EXCLUDED_FACILITIES = 50;
  const MAX_ROUTES = 10;
  const MAX_EXCLUDED_ROUTES = 10;
  const MAX_ROUTE_VERTICES = 5000;
  const MAX_ROUTE_GEOMETRY_BYTES = 512 * 1024;
  const MAX_TOTAL_VERTICES = 20000;
  const MAX_ROUTE_STEPS = 50;
  const MAX_RISK_ZONE_IDS = 50;
  const PROVIDER_PAGE_NUMBER = 1;
  const PROVIDER_PAGE_SIZE_LIMIT = 25;
  const FALLBACK_QUALITY_FLAG = "fallback_last_success";
  const MAX_EXPIRY_TIMER_MS = 60 * 60 * 1000;
  const STRICT_UTC_RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?Z$/;
  const root = document.getElementById("evacuation");
  if (!root) return;

  const elements = collectElements();
  if (!window.L || !window.AIGDM || !window.AIGDM.requestJSON) {
    setOperationState("error", "地图组件或浏览器 API 客户端未加载，疏散工作台不可用。");
    return;
  }

  const map = createMap();
  const markers = { origin: null, destination: null };
  const facilityLayer = window.L.layerGroup().addTo(map);
  const routeLayer = window.L.layerGroup().addTo(map);
  const excludedLayer = window.L.layerGroup().addTo(map);
  const routeLayers = new Map();
  const requestSequence = { facilities: 0, routes: 0 };
  const expiryTimers = { facilities: 0, routes: 0 };
  const activeResults = { facilities: null, routes: null };
  const exclusions = { facilities: [], routes: [], facilityLimitations: [], routeLimitations: [] };
  let coordinateTarget = "origin";

  bindEvents();

  function collectElements() {
    return {
      form: document.getElementById("evacuation-form"), map: document.getElementById("evacuation-map-canvas"),
      message: document.getElementById("evacuation-message"),
      targetButtons: Array.from(root.querySelectorAll("[data-coordinate-target]")),
      modeInputs: Array.from(root.querySelectorAll('input[name="travelMode"]')),
      transitFields: document.getElementById("transit-city-fields"),
      originLongitude: document.getElementById("origin-longitude"),
      originLatitude: document.getElementById("origin-latitude"),
      destinationLongitude: document.getElementById("destination-longitude"),
      destinationLatitude: document.getElementById("destination-latitude"),
      originCity: document.getElementById("origin-city"), destinationCity: document.getElementById("destination-city"),
      facilityKind: document.getElementById("facility-kind"), facilityRadius: document.getElementById("facility-radius"),
      facilitySearch: document.getElementById("facility-search"), routePlan: document.getElementById("route-plan"),
      facilityStatus: document.getElementById("facility-status"), routeStatus: document.getElementById("route-status"),
      facilityCount: document.getElementById("facility-count"), routeCount: document.getElementById("route-count"),
      excludedCount: document.getElementById("excluded-count"),
      facilityResults: document.getElementById("facility-results"), routeResults: document.getElementById("route-results"),
      excludedResults: document.getElementById("excluded-results")
    };
  }

  function createMap() {
    const value = window.L.map(elements.map, { preferCanvas: true }).setView([35.5, 104.5], 4);
    window.L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
      maxZoom: 18, attribution: "&copy; OpenStreetMap contributors"
    }).addTo(value);
    return value;
  }

  function bindEvents() {
    elements.targetButtons.forEach(function (button) {
      button.addEventListener("click", function () { selectCoordinateTarget(button.dataset.coordinateTarget); });
    });
    elements.modeInputs.forEach(function (input) {
      input.addEventListener("change", function () { syncTransitFields(); invalidateRoutes("交通方式已改变"); });
    });
    bindPointInputs("origin", function () {
      syncMarkersFromInputs(); invalidateFacilities("起点已改变"); invalidateRoutes("起点已改变");
    });
    bindPointInputs("destination", function () { syncMarkersFromInputs(); invalidateRoutes("终点已改变"); });
    [elements.originCity, elements.destinationCity].forEach(function (input) {
      input.addEventListener("input", function () { invalidateRoutes("公交 citycode 已改变"); });
    });
    elements.facilityKind.addEventListener("change", function () { invalidateFacilities("设施类型已改变"); });
    elements.facilityRadius.addEventListener("input", function () { invalidateFacilities("搜索半径已改变"); });
    elements.facilitySearch.addEventListener("click", searchFacilities);
    elements.routePlan.addEventListener("click", planRoutes);
    map.on("click", function (event) { writePoint(coordinateTarget, event.latlng.lng, event.latlng.lat, true, true); });
    syncTransitFields();
  }

  function bindPointInputs(target, listener) {
    const fields = pointFields(target);
    fields.longitude.addEventListener("input", listener);
    fields.latitude.addEventListener("input", listener);
  }

  function selectCoordinateTarget(target) {
    coordinateTarget = target === "destination" ? "destination" : "origin";
    elements.targetButtons.forEach(function (button) {
      button.setAttribute("aria-pressed", String(button.dataset.coordinateTarget === coordinateTarget));
    });
    setOperationState("", "地图点击将写入" + (coordinateTarget === "origin" ? "起点" : "终点") + "坐标。");
  }

  function syncTransitFields() {
    const enabled = selectedMode() === "transit";
    elements.transitFields.hidden = !enabled;
    [elements.originCity, elements.destinationCity].forEach(function (input) {
      input.disabled = !enabled;
      input.required = enabled;
    });
  }

  function selectedMode() {
    const selected = elements.modeInputs.find(function (input) { return input.checked; });
    return selected ? selected.value : "driving";
  }

  function syncMarkersFromInputs() {
    ["origin", "destination"].forEach(function (target) {
      const point = optionalPoint(target);
      if (point) setMarker(target, point);
      else removeMarker(target);
    });
    fitSelectedPoints();
  }

  function writePoint(target, longitude, latitude, focus, invalidate) {
    const point = { longitude: Number(longitude), latitude: Number(latitude) };
    if (!validPoint(point)) return;
    const fields = pointFields(target);
    fields.longitude.value = point.longitude.toFixed(6);
    fields.latitude.value = point.latitude.toFixed(6);
    setMarker(target, point);
    if (focus) map.setView([point.latitude, point.longitude], Math.max(map.getZoom(), 12));
    if (invalidate) invalidateForPoint(target);
  }

  function invalidateForPoint(target) {
    if (target === "origin") invalidateFacilities("起点已改变");
    invalidateRoutes((target === "origin" ? "起点" : "终点") + "已改变");
  }

  function setMarker(target, point) {
    removeMarker(target);
    const color = target === "origin" ? "#5ebdd1" : "#53d694";
    markers[target] = window.L.circleMarker([point.latitude, point.longitude], {
      radius: 7, color: "#09100d", weight: 2, fillColor: color, fillOpacity: 1
    }).bindTooltip(target === "origin" ? "疏散起点" : "候选终点").addTo(map);
  }

  function removeMarker(target) {
    if (!markers[target]) return;
    map.removeLayer(markers[target]);
    markers[target] = null;
  }

  function fitSelectedPoints() {
    const points = [optionalPoint("origin"), optionalPoint("destination")].filter(Boolean);
    if (points.length !== 2) return;
    const bounds = window.L.latLngBounds(points.map(function (point) { return [point.latitude, point.longitude]; }));
    map.fitBounds(bounds, { padding: [40, 40], maxZoom: 14 });
  }

  async function searchFacilities() {
    let token = null;
    try {
      const request = facilityRequest();
      token = beginRequest("facilities", request);
      const payload = await window.AIGDM.requestJSON(root.dataset.facilitiesEndpoint, requestOptions(request));
      if (!requestIsCurrent(token, facilityFingerprint)) return;
      renderFacilities(validateFacilityResponse(payload));
    } catch (error) {
      if (!token || requestIsCurrent(token, facilityFingerprint)) failFacilities(error);
    } finally {
      finishRequest(token, "facilities", elements.facilitySearch);
    }
  }

  async function planRoutes() {
    let token = null;
    try {
      const request = routeRequest();
      token = beginRequest("routes", request);
      const payload = await window.AIGDM.requestJSON(root.dataset.routesEndpoint, requestOptions(request));
      if (!requestIsCurrent(token, routeFingerprint)) return;
      renderRoutes(validateRouteResponse(payload));
    } catch (error) {
      if (!token || requestIsCurrent(token, routeFingerprint)) failRoutes(error);
    } finally {
      finishRequest(token, "routes", elements.routePlan);
    }
  }

  function beginRequest(kind, request) {
    requestSequence[kind]++;
    clearExpiry(kind);
    if (kind === "facilities") {
      clearFacilityResult(); setBusy(elements.facilitySearch, true);
      setResultState("facilities", "loading", "正在通过服务端代理搜索并执行风险区筛选...");
    } else {
      clearRouteResult(); setBusy(elements.routePlan, true);
      setResultState("routes", "loading", "正在规划并执行风险区相交闸门...");
    }
    return { kind: kind, sequence: requestSequence[kind], fingerprint: JSON.stringify(request) };
  }

  function requestIsCurrent(token, fingerprint) {
    return token && requestSequence[token.kind] === token.sequence && token.fingerprint === fingerprint();
  }

  function finishRequest(token, kind, button) {
    if (!token || requestSequence[kind] === token.sequence) setBusy(button, false);
  }

  function requestOptions(body) {
    return { method: "POST", body: body, timeoutMs: requestTimeout(), maxResponseBytes: responseLimit() };
  }

  function facilityRequest() {
    const radius = Number(elements.facilityRadius.value);
    if (!Number.isInteger(radius) || radius < 1 || radius > 50000) {
      throw new Error("设施搜索半径必须是 1 至 50000 米的整数");
    }
    return { hazardType: "landslide", center: readPoint("origin"),
      kind: elements.facilityKind.value, radiusMeters: radius };
  }

  function routeRequest() {
    const origin = readPoint("origin");
    const destination = readPoint("destination");
    if (origin.longitude === destination.longitude && origin.latitude === destination.latitude) {
      throw new Error("起点和终点不能完全相同");
    }
    const result = { hazardType: "landslide", origin: origin, destination: destination, mode: selectedMode() };
    if (result.mode === "transit") {
      result.originCity = cityCode(elements.originCity.value, "起点");
      result.destinationCity = cityCode(elements.destinationCity.value, "终点");
    }
    return result;
  }

  function facilityFingerprint() { return safeFingerprint(facilityRequest); }
  function routeFingerprint() { return safeFingerprint(routeRequest); }

  function safeFingerprint(factory) {
    try { return JSON.stringify(factory()); } catch (_) { return "__invalid__"; }
  }

  function validateFacilityResponse(payload) {
    const data = requireData(payload, "设施搜索");
    const limits = validateFacilityLimits(data.limits);
    const snapshot = validateSnapshot(data.snapshot);
    if (!Array.isArray(data.facilities) || !Array.isArray(data.excluded) || !data.filter) {
      throw new Error("设施搜索接口契约不完整");
    }
    validateFacilityCounts(data, limits);
    data.facilities.forEach(validateFacility);
    data.excluded.forEach(function (item) { validateExcludedFacility(item, limits.maxRiskZoneIds); });
    validateStringList(data.limitations, 50, "设施限制");
    data._freshness = snapshot.freshness; data._validTo = snapshot.validTo;
    return data;
  }

  function validateFacilityLimits(limits) {
    if (!limits || !positiveLimit(limits.maxFacilities, MAX_FACILITIES) ||
      !positiveLimit(limits.maxExcludedFacilities, MAX_EXCLUDED_FACILITIES) ||
      !positiveLimit(limits.maxRiskZoneIds, MAX_RISK_ZONE_IDS) ||
      !positiveLimit(limits.maxResponseBytes, MAX_RESPONSE_BYTES) ||
      limits.providerPageNumber !== PROVIDER_PAGE_NUMBER ||
      !positiveLimit(limits.providerPageSizeLimit, PROVIDER_PAGE_SIZE_LIMIT)) {
      throw new Error("设施搜索接口负载上限无效");
    }
    return limits;
  }

  function validateFacilityCounts(data, limits) {
    const filter = data.filter;
    const candidate = safeCount(filter.candidateCount, "设施候选总数");
    const allowed = safeCount(filter.allowedCount, "可选设施总数");
    const excluded = safeCount(filter.excludedCount, "排除设施总数");
    const visibleAllowed = safeCount(filter.visibleAllowedCount, "可见可选设施数");
    const visibleExcluded = safeCount(filter.visibleExcludedCount, "可见排除设施数");
    const omittedAllowed = safeCount(filter.omittedAllowedCount, "省略可选设施数");
    const omittedExcluded = safeCount(filter.omittedExcludedCount, "省略排除设施数");
    if (candidate !== allowed + excluded || allowed !== visibleAllowed + omittedAllowed ||
      excluded !== visibleExcluded + omittedExcluded || visibleAllowed !== data.facilities.length ||
      visibleExcluded !== data.excluded.length || visibleAllowed > limits.maxFacilities ||
      visibleExcluded > limits.maxExcludedFacilities ||
      candidate > limits.providerPageNumber * limits.providerPageSizeLimit) {
      throw new Error("设施搜索计数契约不一致");
    }
  }

  function validateRouteResponse(payload) {
    const data = requireData(payload, "路线规划");
    const limits = validateRouteLimits(data.limits);
    const snapshot = validateSnapshot(data.snapshot);
    if (!Array.isArray(data.routes) || !Array.isArray(data.excluded) ||
      typeof data.riskScoreAvailable !== "boolean") throw new Error("路线规划接口契约不完整");
    validateRouteCounts(data, limits);
    let vertices = 0;
    data.routes.forEach(function (route) { vertices += validateCandidateRoute(route, limits, data.totalRouteCount); });
    validateCandidateRanks(data.routes);
    data.excluded.forEach(function (item) { vertices += validateExcludedRoute(item, limits); });
    if (vertices > limits.maxTotalVertices) throw new Error("路线几何总量超过浏览器安全上限");
    validateStringList(data.limitations, 50, "路线限制");
    data._freshness = snapshot.freshness; data._validTo = snapshot.validTo;
    return data;
  }

  function validateRouteLimits(limits) {
    if (!limits || !positiveLimit(limits.maxRoutes, MAX_ROUTES) ||
      !positiveLimit(limits.maxExcludedRoutes, MAX_EXCLUDED_ROUTES) ||
      !positiveLimit(limits.maxRouteVertices, MAX_ROUTE_VERTICES) ||
      !positiveLimit(limits.maxRouteGeometryBytes, MAX_ROUTE_GEOMETRY_BYTES) ||
      !positiveLimit(limits.maxTotalVertices, MAX_TOTAL_VERTICES) ||
      !positiveLimit(limits.maxRouteSteps, MAX_ROUTE_STEPS) ||
      !positiveLimit(limits.maxRiskZoneIds, MAX_RISK_ZONE_IDS) ||
      !positiveLimit(limits.maxResponseBytes, MAX_RESPONSE_BYTES)) throw new Error("路线规划接口负载上限无效");
    return limits;
  }

  function validateRouteCounts(data, limits) {
    const total = safeCount(data.totalRouteCount, "候选路线总数");
    const visible = safeCount(data.visibleRouteCount, "可见候选路线数");
    const omitted = safeCount(data.omittedRouteCount, "省略候选路线数");
    const totalExcluded = safeCount(data.totalExcludedRouteCount, "排除路线总数");
    const visibleExcluded = safeCount(data.visibleExcludedRouteCount, "可见排除路线数");
    const omittedExcluded = safeCount(data.omittedExcludedRouteCount, "省略排除路线数");
    if (total !== visible + omitted || totalExcluded !== visibleExcluded + omittedExcluded ||
      visible !== data.routes.length || visibleExcluded !== data.excluded.length ||
      visible > limits.maxRoutes || visibleExcluded > limits.maxExcludedRoutes) {
      throw new Error("路线规划计数契约不一致");
    }
  }

  function validateSnapshot(snapshot) {
    if (!snapshot || typeof snapshot.id !== "string" || !snapshot.id.trim() ||
      (snapshot.status !== "available" && snapshot.status !== "stale")) {
      throw new Error("风险快照状态缺失或无效");
    }
    const source = snapshot.source;
    if (!source || typeof source !== "object" || typeof source.stale !== "boolean") {
      throw new Error("风险来源状态缺失或无效");
    }
    const validTo = parseRequiredTime(snapshot.validTo, "风险快照有效期");
    const sourceValidTo = parseRequiredTime(source.validTo, "风险来源有效期");
    if (snapshot.validTo !== source.validTo || validTo !== sourceValidTo) {
      throw new Error("风险快照与来源有效期不一致");
    }
    const qualityFlags = source.qualityFlags === undefined ? [] : source.qualityFlags;
    validateStringList(qualityFlags, 50, "风险来源质量标记");
    const stale = snapshot.status === "stale" || source.stale;
    const freshness = Date.now() >= validTo ? "expired" :
      qualityFlags.includes(FALLBACK_QUALITY_FLAG) ? "fallback" : stale ? "stale" : "current";
    return { validTo: validTo, freshness: freshness };
  }

  function validateFacility(facility) {
    if (!facility || typeof facility.id !== "string" || !facility.id.trim() ||
      typeof facility.name !== "string" || !facility.name.trim() || !validPoint(facility.location) ||
      !["shelter", "hospital", "transport"].includes(facility.type)) throw new Error("设施候选结构无效");
    requireNonNegativeNumber(facility.distanceMeters, "设施距离");
  }

  function validateExcludedFacility(item, maximumZoneIDs) {
    if (!item || typeof item.reason !== "string" || !item.reason.trim()) throw new Error("设施排除原因无效");
    validateFacility(item.facility);
    validateZoneIDs(item.riskZoneIds, item.omittedRiskZoneIdCount, maximumZoneIDs);
  }

  function validateRoute(route, limits) {
    if (!route || typeof route.id !== "string" || !route.id.trim() ||
      !["driving", "walking", "transit"].includes(route.mode)) throw new Error("候选路线结构无效");
    requirePositiveNumber(route.distanceMeters, "路线距离");
    if (!Number.isSafeInteger(route.durationSeconds) || route.durationSeconds <= 0) throw new Error("路线时长无效");
    if (typeof route.intersectsRiskZone !== "boolean") throw new Error("路线风险区相交标记无效");
    validateRiskScore(route);
    const geometryBytes = safeCount(route.geometryByteCount, "路线几何字节数");
    if (geometryBytes === 0 || geometryBytes > limits.maxRouteGeometryBytes) throw new Error("路线几何超过字节上限");
    const vertices = validateLineString(route.geometry, limits.maxRouteVertices);
    if (!Array.isArray(route.steps) || route.steps.length > limits.maxRouteSteps) throw new Error("路线步骤超过安全上限");
    safeCount(route.omittedStepCount, "省略路线步骤数");
    validateStringList(route.limitations, 30, "路线专属限制");
    return vertices;
  }

  function validateCandidateRoute(route, limits, totalRouteCount) {
    const vertices = validateRoute(route, limits);
    if (route.intersectsRiskZone || !Number.isSafeInteger(route.rank) ||
      route.rank <= 0 || route.rank > totalRouteCount) throw new Error("候选路线安全状态或排名无效");
    return vertices;
  }

  function validateCandidateRanks(routes) {
    const ranks = new Set();
    routes.forEach(function (route) {
      if (ranks.has(route.rank)) throw new Error("候选路线排名重复");
      ranks.add(route.rank);
    });
  }

  function validateExcludedRoute(item, limits) {
    if (!item || typeof item.reason !== "string" || !item.reason.trim()) throw new Error("路线排除原因无效");
    const vertices = validateRoute(item.route, limits);
    validateZoneIDs(item.riskZoneIds, item.omittedRiskZoneIdCount, limits.maxRiskZoneIds);
    return vertices;
  }

  function validateRiskScore(route) {
    if (typeof route.riskScoreProvided !== "boolean") throw new Error("路线风险分数来源标记无效");
    if (route.riskScoreProvided) {
      requireRangeNumber(route.riskScore, 0, 100, "路线风险分数");
      return;
    }
    if (route.riskScore !== null) throw new Error("缺失路线风险分数必须返回 null");
  }

  function validateLineString(geometry, maximumVertices) {
    if (!geometry || geometry.type !== "LineString" || !Array.isArray(geometry.coordinates) ||
      geometry.coordinates.length < 2 || geometry.coordinates.length > maximumVertices) {
      throw new Error("路线几何结构无效或超过顶点上限");
    }
    geometry.coordinates.forEach(validateCoordinate);
    return geometry.coordinates.length;
  }

  function validateCoordinate(value) {
    if (!Array.isArray(value) || value.length < 2 || typeof value[0] !== "number" || typeof value[1] !== "number" ||
      !Number.isFinite(value[0]) || !Number.isFinite(value[1]) || value[0] < -180 || value[0] > 180 ||
      value[1] < -90 || value[1] > 90) throw new Error("路线坐标超出 WGS84 范围");
  }

  function validateZoneIDs(values, omitted, maximum) {
    if (!Array.isArray(values) || values.length > maximum || values.some(function (value) {
      return typeof value !== "string" || !value.trim();
    })) throw new Error("风险区标识列表无效");
    safeCount(omitted, "省略风险区标识数");
  }

  function validateStringList(values, maximum, label) {
    if (!Array.isArray(values) || values.length > maximum || values.some(function (value) {
      return typeof value !== "string" || !value.trim();
    })) throw new Error(label + "无效");
  }

  function renderFacilities(data) {
    facilityLayer.clearLayers(); elements.facilityResults.replaceChildren();
    data.facilities.forEach(function (facility) {
      addFacilityMarker(facility, false); elements.facilityResults.appendChild(facilityItem(facility));
    });
    data.excluded.forEach(function (item) { addFacilityMarker(item.facility, true); });
    renderFacilityEmpty(data);
    elements.facilityCount.textContent = data.filter.visibleAllowedCount + " / " + data.filter.allowedCount +
      " 个可选，" + data.filter.visibleExcludedCount + " / " + data.filter.excludedCount + " 个排除";
    exclusions.facilities = data.excluded; exclusions.facilityLimitations = data.limitations.slice();
    activeResults.facilities = data; applyFacilityFreshness(data._freshness); renderExclusions();
    scheduleExpiry("facilities", data, applyFacilityFreshness);
  }

  function renderFacilityEmpty(data) {
    if (data.facilities.length > 0) return;
    const message = facilityResultsUnavailable(data) ?
      "风险区外候选设施全部因响应安全上限省略，页面无法展示可调度设施，不得据此判断无风险或无设施。" :
      facilityCandidatesAllExcluded(data) ?
        "本次供应商候选设施全部落入当前已知风险区，当前没有通过门禁的可调度设施。" :
      data.filter.omittedAllowedCount + data.filter.omittedExcludedCount > 0 ?
        "响应已省略部分设施，不能据此判断没有安全候选。" :
      "本次有界供应商结果中没有风险区外候选设施，不能据此断言周边不存在其他设施。";
    appendEmpty(elements.facilityResults, message);
  }

  function addFacilityMarker(facility, excluded) {
    const color = excluded ? "#f06b61" : "#53d694";
    const tooltip = document.createElement("span");
    tooltip.textContent = textValue(facility.name);
    window.L.circleMarker([facility.location.latitude, facility.location.longitude], {
      radius: excluded ? 5 : 6, color: color, weight: 2, fillColor: color, fillOpacity: excluded ? .35 : .85
    }).bindTooltip(tooltip).addTo(facilityLayer);
  }

  function facilityItem(facility) {
    const item = resultItem(textValue(facility.name));
    appendText(item, "p", textValue(facility.address));
    item.appendChild(metrics([["距离", distanceText(facility.distanceMeters)], ["类型", facilityTypeText(facility.type)],
      ["来源", sourceText(facility.source)], ["坐标", pointText(facility.location)]]));
    const button = commandButton("设为候选终点", "secondary-command");
    button.addEventListener("click", function () {
      writePoint("destination", facility.location.longitude, facility.location.latitude, true, true);
      selectCoordinateTarget("destination");
      setOperationState("", "已将“" + textValue(facility.name) + "”写入候选终点；设施结果时效状态保持不变。");
    });
    item.appendChild(button);
    return item;
  }

  function applyFacilityFreshness(freshness) {
    const data = activeResults.facilities;
    if (!data) return;
    elements.facilityResults.dataset.freshness = freshness;
    if (facilityResultsUnavailable(data)) {
      const prefix = freshnessPrefix("设施结果", freshness);
      setResultState("facilities", "unavailable", prefix + "风险区外候选设施全部因响应安全上限省略；当前不可用于调度。");
      return;
    }
    if (facilityCandidatesAllExcluded(data)) {
      const prefix = freshnessPrefix("设施结果", freshness);
      setResultState("facilities", "unavailable", prefix + "本次供应商候选设施全部被当前风险区门禁排除；当前无可调度设施。");
      return;
    }
    if (freshness === "expired") {
      setResultState("facilities", "warning", "设施结果使用的风险快照已过期，仅保留供人工复核。");
      return;
    }
    if (freshness === "fallback") {
      setResultState("facilities", "warning", "设施结果使用有效期内的最后成功风险图层；实时采集失败，需复核现场变化。");
      return;
    }
    if (freshness === "stale") {
      setResultState("facilities", "warning", "设施结果使用的风险快照标记为需复核，请核对数据时效。");
      return;
    }
    setResultState("facilities", "current", "设施已按当前有效的已知风险区完成筛选。");
  }

  function renderRoutes(data) {
    routeLayer.clearLayers(); excludedLayer.clearLayers(); routeLayers.clear(); elements.routeResults.replaceChildren();
    data.routes.forEach(function (route, index) { renderCandidateRoute(route, index); });
    data.excluded.forEach(renderExcludedRouteGeometry); renderRouteEmpty(data);
    elements.routeCount.textContent = data.visibleRouteCount + " / " + data.totalRouteCount +
      " 条可选，" + data.visibleExcludedRouteCount + " / " + data.totalExcludedRouteCount + " 条排除";
    exclusions.routes = data.excluded; exclusions.routeLimitations = data.limitations.slice();
    activeResults.routes = data; applyRouteFreshness(data._freshness); renderExclusions(); fitRouteLayers();
    scheduleExpiry("routes", data, applyRouteFreshness);
  }

  function renderRouteEmpty(data) {
    if (data.routes.length > 0) return;
    const message = routeResultsUnavailable(data) ?
      "候选路线全部因响应安全上限省略，页面无法展示可调度路线，不得降低风险判断。" :
      data.omittedRouteCount > 0 ? "响应已省略部分候选路线，请通过审计接口复核。" :
      "本次没有通过当前风险区相交闸门的候选路线。";
    appendEmpty(elements.routeResults, message);
  }

  function renderCandidateRoute(route, index) {
    const layer = addRouteGeometry(route, routeColor(index), routeLayer, false);
    if (layer) routeLayers.set(route.id, layer);
    const item = resultItem("候选路线 #" + route.rank);
    item.appendChild(metrics([["风险分数", riskScoreText(route)],
      ["预计时长", durationText(route.durationSeconds)], ["距离", distanceText(route.distanceMeters)],
      ["来源", sourceText(route.source)]]));
    appendText(item, "p", "未穿越当前已知风险区；不代表道路已开放。");
    appendText(item, "p", stepText(route.steps, route.omittedStepCount));
    appendLimitations(item, route.limitations);
    const button = commandButton("在地图中查看", "secondary-command");
    button.addEventListener("click", function () { focusRoute(route.id); });
    item.appendChild(button); elements.routeResults.appendChild(item);
  }

  function applyRouteFreshness(freshness) {
    const data = activeResults.routes;
    if (!data) return;
    elements.routeResults.dataset.freshness = freshness;
    if (routeResultsUnavailable(data)) {
      const prefix = freshnessPrefix("路线结果", freshness);
      setResultState("routes", "unavailable", prefix + "候选路线全部因响应安全上限省略；当前不可用于调度。");
      return;
    }
    if (routeCandidatesAllExcluded(data)) {
      const prefix = freshnessPrefix("路线结果", freshness);
      setResultState("routes", "unavailable", prefix + "本次供应商候选路线全部被当前风险区门禁排除；当前无可调度路线。");
      return;
    }
    if (freshness === "expired") {
      setResultState("routes", "warning", "路线结果使用的风险快照已过期，禁止作为当前道路安全结论。");
      return;
    }
    if (freshness === "fallback") {
      setResultState("routes", "warning", "路线结果使用有效期内的最后成功风险图层；实时采集失败，需复核道路变化。");
      return;
    }
    if (freshness === "stale") {
      setResultState("routes", "warning", "路线结果使用的风险快照标记为需复核，禁止直接作为道路安全结论。");
      return;
    }
    const partial = data.routes.some(function (route) { return !route.riskScoreProvided; });
    const suffix = !data.riskScoreAvailable ? "；供应商未提供风险分数，仅执行相交闸门后按时间和距离排序" :
      partial ? "；部分路线缺少风险分数，缺失项未按 0 分处理" : "";
    setResultState("routes", "current", "路线已按当前有效风险区完成安全排序" + suffix + "。");
  }

  function facilityResultsUnavailable(data) {
    return data.filter.allowedCount > 0 && data.filter.visibleAllowedCount === 0 &&
      data.filter.omittedAllowedCount === data.filter.allowedCount;
  }

  function facilityCandidatesAllExcluded(data) {
    return data.filter.candidateCount > 0 && data.filter.allowedCount === 0 &&
      data.filter.excludedCount === data.filter.candidateCount;
  }

  function routeResultsUnavailable(data) {
    return data.totalRouteCount > 0 && data.visibleRouteCount === 0 &&
      data.omittedRouteCount === data.totalRouteCount;
  }

  function routeCandidatesAllExcluded(data) {
    return data.totalRouteCount === 0 && data.totalExcludedRouteCount > 0;
  }

  function freshnessPrefix(subject, freshness) {
    if (freshness === "expired") return subject + "已过期，且";
    if (freshness === "fallback") return subject + "来自最后成功回退风险图层，且";
    if (freshness === "stale") return subject + "时效需复核，且";
    return "";
  }

  function renderExcludedRouteGeometry(item) {
    if (item && item.route) addRouteGeometry(item.route, "#f06b61", excludedLayer, true);
  }

  function addRouteGeometry(route, color, group, dashed) {
    const layer = window.L.geoJSON(route.geometry, {
      style: { color: color, weight: dashed ? 3 : 5, opacity: dashed ? .65 : .85,
        dashArray: dashed ? "7 7" : null }
    }).addTo(group);
    return layer.getLayers().length ? layer : null;
  }

  function focusRoute(id) {
    const layer = routeLayers.get(id);
    if (!layer) return;
    const bounds = layer.getBounds();
    if (bounds.isValid()) map.fitBounds(bounds, { padding: [32, 32], maxZoom: 15 });
  }

  function fitRouteLayers() {
    const layers = routeLayer.getLayers().concat(excludedLayer.getLayers());
    if (layers.length === 0) return fitSelectedPoints();
    const bounds = window.L.featureGroup(layers).getBounds();
    if (bounds.isValid()) map.fitBounds(bounds, { padding: [36, 36], maxZoom: 14 });
  }

  function scheduleExpiry(kind, data, renderFreshness) {
    clearExpiry(kind);
    const check = function () {
      if (activeResults[kind] !== data) return;
      const remaining = data._validTo - Date.now();
      if (remaining <= 0) {
        data._freshness = "expired"; appendExpiryLimitation(kind); renderFreshness("expired"); return;
      }
      expiryTimers[kind] = window.setTimeout(check, Math.min(remaining + 25, MAX_EXPIRY_TIMER_MS));
    };
    check();
  }

  function appendExpiryLimitation(kind) {
    const field = kind === "facilities" ? "facilityLimitations" : "routeLimitations";
    const message = "结果已跨过风险快照有效期，禁止作为当前调度结论";
    if (!exclusions[field].includes(message)) exclusions[field].push(message);
    renderExclusions();
  }

  function clearExpiry(kind) {
    if (expiryTimers[kind]) window.clearTimeout(expiryTimers[kind]);
    expiryTimers[kind] = 0;
  }

  function invalidateFacilities(reason) {
    requestSequence.facilities++; clearExpiry("facilities"); clearFacilityResult();
    setBusy(elements.facilitySearch, false);
    setResultState("facilities", "idle", reason + "，旧设施结果已清除。");
  }

  function invalidateRoutes(reason) {
    requestSequence.routes++; clearExpiry("routes"); clearRouteResult();
    setBusy(elements.routePlan, false);
    setResultState("routes", "idle", reason + "，旧路线结果已清除。");
  }

  function failFacilities(error) {
    clearFacilityResult();
    setResultState("facilities", "error", errorText(error) + "；未沿用上一次设施结果。");
  }

  function failRoutes(error) {
    clearRouteResult();
    setResultState("routes", "error", errorText(error) + "；未沿用上一次路线结果。");
  }

  function clearFacilityResult() {
    activeResults.facilities = null; facilityLayer.clearLayers();
    elements.facilityResults.removeAttribute("data-freshness"); elements.facilityResults.replaceChildren();
    appendEmpty(elements.facilityResults, "尚无与当前输入绑定的设施结果。");
    elements.facilityCount.textContent = "尚未搜索";
    exclusions.facilities = []; exclusions.facilityLimitations = []; renderExclusions();
  }

  function clearRouteResult() {
    activeResults.routes = null; routeLayer.clearLayers(); excludedLayer.clearLayers(); routeLayers.clear();
    elements.routeResults.removeAttribute("data-freshness"); elements.routeResults.replaceChildren();
    appendEmpty(elements.routeResults, "尚无与当前输入绑定的路线结果。");
    elements.routeCount.textContent = "尚未规划";
    exclusions.routes = []; exclusions.routeLimitations = []; renderExclusions(); fitSelectedPoints();
  }

  function renderExclusions() {
    elements.excludedResults.replaceChildren();
    exclusions.facilities.forEach(function (item) {
      appendExcluded("设施：" + textValue(item.facility && item.facility.name), item.reason,
        item.riskZoneIds, item.omittedRiskZoneIdCount);
    });
    exclusions.routes.forEach(function (item) {
      appendExcluded("路线：" + textValue(item.route && item.route.id), item.reason,
        item.riskZoneIds, item.omittedRiskZoneIdCount);
    });
    const limitations = exclusions.facilityLimitations.concat(exclusions.routeLimitations);
    Array.from(new Set(limitations)).forEach(function (value) {
      if (typeof value === "string" && value.trim()) appendExcluded("使用限制", value, [], 0);
    });
    const count = elements.excludedResults.children.length;
    elements.excludedCount.textContent = count + " 项";
    if (count === 0) appendEmpty(elements.excludedResults, "当前没有排除明细；仍需人工确认现场条件。");
  }

  function appendExcluded(title, reason, zoneIDs, omitted) {
    const item = resultItem(title);
    item.classList.add(title === "使用限制" ? "result-item-warning" : "result-item-excluded");
    appendText(item, "p", textValue(reason));
    if (zoneIDs.length) appendText(item, "p", "命中风险区：" + zoneIDs.join("、"));
    if (omitted > 0) appendText(item, "p", "另有 " + omitted + " 个风险区标识因响应上限省略。");
    elements.excludedResults.appendChild(item);
  }

  function appendLimitations(parent, values) {
    if (!Array.isArray(values) || values.length === 0) return;
    const list = document.createElement("ul");
    values.forEach(function (value) { appendText(list, "li", value); });
    parent.appendChild(list);
  }

  function readPoint(target) {
    const point = optionalPoint(target);
    if (!point) throw new Error((target === "origin" ? "起点" : "终点") + "坐标不完整或超出 WGS84 范围");
    return point;
  }

  function optionalPoint(target) {
    const fields = pointFields(target);
    if (fields.longitude.value.trim() === "" || fields.latitude.value.trim() === "") return null;
    const point = { longitude: Number(fields.longitude.value), latitude: Number(fields.latitude.value) };
    return validPoint(point) ? point : null;
  }

  function pointFields(target) {
    return target === "origin" ?
      { longitude: elements.originLongitude, latitude: elements.originLatitude } :
      { longitude: elements.destinationLongitude, latitude: elements.destinationLatitude };
  }

  function validPoint(point) {
    return point && typeof point.longitude === "number" && typeof point.latitude === "number" &&
      Number.isFinite(point.longitude) && Number.isFinite(point.latitude) && point.longitude >= -180 &&
      point.longitude <= 180 && point.latitude >= -90 && point.latitude <= 90;
  }

  function requireData(payload, operation) {
    if (!payload || !payload.data || typeof payload.data !== "object") {
      throw new Error(operation + "接口返回的数据结构无效");
    }
    return payload.data;
  }

  function cityCode(value, label) {
    const result = value.trim();
    if (!/^[0-9]{1,12}$/.test(result)) throw new Error(label + "高德 citycode 必须是 1 至 12 位数字");
    return result;
  }

  function resultItem(title) {
    const item = document.createElement("article"); item.className = "result-item";
    appendText(item, "h4", title); return item;
  }

  function metrics(values) {
    const list = document.createElement("dl");
    values.forEach(function (value) {
      const group = document.createElement("div");
      appendText(group, "dt", value[0]); appendText(group, "dd", value[1]); list.appendChild(group);
    });
    return list;
  }

  function commandButton(label, className) {
    const button = document.createElement("button");
    button.type = "button"; button.className = className; button.textContent = label; return button;
  }

  function appendEmpty(parent, message) {
    const value = document.createElement("p");
    value.className = "empty-result"; value.textContent = message; parent.appendChild(value);
  }

  function appendText(parent, tag, value) {
    const child = document.createElement(tag); child.textContent = value; parent.appendChild(child);
  }

  function setBusy(button, busy) { button.disabled = busy; }

  function setOperationState(state, message) {
    elements.message.className = "dispatch-state" + (state ? " dispatch-state-" + state : "");
    elements.message.textContent = message;
  }

  function setResultState(kind, state, message) {
    const element = kind === "facilities" ? elements.facilityStatus : elements.routeStatus;
    element.className = "result-state result-state-" + state; element.textContent = message;
  }

  function requestTimeout() {
    const value = Number(root.dataset.requestTimeoutMs);
    return Number.isSafeInteger(value) && value >= 25 && value <= 60000 ? value : 20000;
  }

  function responseLimit() {
    const value = Number(root.dataset.maxResponseBytes);
    return Number.isSafeInteger(value) && value > 0 && value <= MAX_RESPONSE_BYTES ? value : MAX_RESPONSE_BYTES;
  }

  function parseRequiredTime(value, label) {
    if (typeof value !== "string") throw new Error(label + "缺失或无效");
    const match = STRICT_UTC_RFC3339.exec(value);
    const timestamp = match ? Date.parse(value) : Number.NaN;
    if (!match || !Number.isFinite(timestamp) || !calendarMatches(timestamp, match)) {
      throw new Error(label + "缺失或无效");
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

  function positiveLimit(value, maximum) {
    return typeof value === "number" && Number.isSafeInteger(value) && value > 0 && value <= maximum;
  }

  function safeCount(value, label) {
    if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw new Error(label + "无效");
    return value;
  }

  function requirePositiveNumber(value, label) {
    if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) throw new Error(label + "无效");
  }

  function requireNonNegativeNumber(value, label) {
    if (typeof value !== "number" || !Number.isFinite(value) || value < 0) throw new Error(label + "无效");
  }

  function requireRangeNumber(value, minimum, maximum, label) {
    if (typeof value !== "number" || !Number.isFinite(value) || value < minimum || value > maximum) {
      throw new Error(label + "无效");
    }
  }

  function errorText(error) {
    const requestID = error && error.requestId ? "，请求 ID：" + error.requestId : "";
    return (error instanceof Error ? error.message : "请求失败") + requestID;
  }

  function textValue(value) {
    return value === undefined || value === null || String(value).trim() === "" ? "未提供" : String(value);
  }

  function sourceText(source) {
    return source ? [source.provider, source.dataset].filter(Boolean).join(" / ") || "未提供" : "未提供";
  }

  function pointText(point) { return validPoint(point) ? point.longitude.toFixed(6) + ", " + point.latitude.toFixed(6) : "未提供"; }
  function distanceText(value) { return value >= 1000 ? (value / 1000).toFixed(1) + " km" : Math.round(value) + " m"; }
  function durationText(value) { return Math.max(1, Math.round(value / 60)) + " 分钟"; }
  function riskScoreText(route) {
    return route.riskScoreProvided ? route.riskScore.toFixed(1) + " / 100" : "未提供，仅执行风险区相交闸门";
  }
  function facilityTypeText(value) { return ({ shelter: "应急避难场所", hospital: "医院", transport: "交通设施" })[value] || textValue(value); }
  function routeColor(index) { return ["#53d694", "#5ebdd1", "#f0b85a", "#d9e0dc"][index % 4]; }

  function stepText(steps, omitted) {
    const values = steps.map(function (step) { return step && step.instruction; }).filter(Boolean).slice(0, 3);
    const base = values.length ? "主要指引：" + values.join("；") : "供应商未提供分步导航指引。";
    return omitted > 0 ? base + " 另有 " + omitted + " 步因响应上限省略。" : base;
  }
})();
