import { expect, test } from "@playwright/test";

const FACILITIES_PATH = "/api/v1/map/places/nearby";
const ROUTES_PATH = "/api/v1/map/routes";
const FIXED_NOW = new Date("2026-08-28T00:00:00Z");
const INJECTION_NAME = '<img src=x onerror="window.__evacuationInjected=true">';
const INJECTION_VENDOR = '<svg onload="window.__evacuationInjected=true">';
const INJECTION_LIMIT = "<script>window.__evacuationInjected=true</script>";
const TILE = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M/wHwAF/gL+Xc7pAAAAAElFTkSuQmCC", "base64");

const INVALID_SNAPSHOT_SCENARIOS = [
  "missing_snapshot_valid_to",
  "missing_source_valid_to",
  "array_snapshot_valid_to",
  "array_source_valid_to",
  "non_strict_valid_to",
  "invalid_calendar_valid_to",
  "mismatched_valid_to",
  "missing_source_stale",
  "invalid_source_stale"
];

test.beforeEach(async ({ page }) => {
  await page.route(/^https:\/\/[a-z]\.tile\.openstreetmap\.org\//, async (route) => {
    await route.fulfill({ status: 200, contentType: "image/png", body: TILE });
  });
});

test("HTML 注入内容仅作为设施、供应商和限制文本展示", async ({ page, request }) => {
  await setScenario(request, "html_injection");
  await openWorkbench(page);
  await fillCoordinates(page);
  await searchFacilities(page);
  await planRoutes(page);

  await expect(page.locator("#facility-results")).toContainText(INJECTION_NAME);
  await expect(page.locator("#facility-results")).toContainText(INJECTION_VENDOR);
  await expect(page.locator("#route-results")).toContainText(INJECTION_VENDOR);
  await expect(page.locator("#route-results")).toContainText(INJECTION_LIMIT);
  await expect(page.locator("#evacuation .result-list img, #evacuation .result-list svg, #evacuation .result-list script")).toHaveCount(0);
  expect(await page.evaluate(() => Boolean(window.__evacuationInjected))).toBeFalsy();
});

test("输入改变立即清空旧设施结果且迟到响应不能覆盖新输入", async ({ page, request }) => {
  await setScenario(request, "success");
  await openWorkbench(page);
  await fillOrigin(page);
  await searchFacilities(page);
  await expect(page.locator("#facility-results .result-item")).toHaveCount(1);

  await setScenario(request, "facility_delayed");
  const lateResponse = page.waitForResponse((response) => response.url().endsWith(FACILITIES_PATH));
  await page.locator("#facility-search").click();
  await expect(page.locator("#facility-results .result-item")).toHaveCount(0);
  await page.locator("#facility-radius").fill("6000");
  await expect(page.locator("#facility-status")).toContainText("搜索半径已改变，旧设施结果已清除");
  await lateResponse;
  await expect(page.locator("#facility-search")).toBeEnabled();
  await expect(page.locator("#facility-results")).not.toContainText("迟到设施结果");
  await expect(page.locator("#facility-results .result-item")).toHaveCount(0);
});

test("输入改变立即清空旧路线结果且迟到响应不能覆盖新输入", async ({ page, request }) => {
  await setScenario(request, "success");
  await openWorkbench(page);
  await fillCoordinates(page);
  await planRoutes(page);
  await expect(page.locator("#route-results .result-item")).toHaveCount(1);

  await setScenario(request, "route_delayed");
  const lateResponse = page.waitForResponse((response) => response.url().endsWith(ROUTES_PATH));
  await page.locator("#route-plan").click();
  await expect(page.locator("#route-results .result-item")).toHaveCount(0);
  await page.locator("#destination-longitude").fill("104.083000");
  await expect(page.locator("#route-status")).toContainText("终点已改变，旧路线结果已清除");
  await lateResponse;
  await expect(page.locator("#route-plan")).toBeEnabled();
  await expect(page.locator("#route-results")).not.toContainText("迟到路线供应商");
  await expect(page.locator("#route-results .result-item")).toHaveCount(0);
});

test("设施成功后 503 清空旧结果", async ({ page, request }) => {
  await setScenario(request, "facility_success_then_503");
  await openWorkbench(page);
  await fillOrigin(page);
  await searchFacilities(page);
  await expect(page.locator("#facility-results .result-item")).toHaveCount(1);

  await page.locator("#facility-search").click();

  await expectFacilityFailClosed(page, "地图供应商暂时不可用");
});

test("路线成功后超时清空旧结果", async ({ page, request }) => {
  await setScenario(request, "route_success_then_timeout");
  await page.clock.install({ time: FIXED_NOW });
  await openWorkbench(page);
  await fillCoordinates(page);
  await planRoutes(page);
  await expect(page.locator("#route-results .result-item")).toHaveCount(1);

  const pending = page.waitForRequest((value) => value.url().endsWith(ROUTES_PATH));
  await page.locator("#route-plan").click();
  await pending;
  await page.clock.fastForward(20_100);

  await expectRouteFailClosed(page, "请求处理超时");
});

test("公交 citycode 显示、校验并透传到 typed transit stub", async ({ page, request }) => {
  await setScenario(request, "transit_citycodes");
  await openWorkbench(page);
  await fillCoordinates(page);
  await expect(page.locator("#transit-city-fields")).toBeHidden();
  await expect(page.locator("#origin-city")).toBeDisabled();
  await expect(page.locator("#destination-city")).toBeDisabled();
  await expect(page.locator("#origin-city")).not.toHaveAttribute("required", "");
  await expect(page.locator("#destination-city")).not.toHaveAttribute("required", "");

  await selectTravelMode(page, "transit");
  await expect(page.locator("#transit-city-fields")).toBeVisible();
  await expect(page.locator("#origin-city")).toBeEnabled();
  await expect(page.locator("#destination-city")).toBeEnabled();
  await expect(page.locator("#origin-city")).toHaveAttribute("required", "");
  await expect(page.locator("#destination-city")).toHaveAttribute("required", "");
  let routeRequestCount = 0;
  page.on("request", (value) => { if (value.url().endsWith(ROUTES_PATH)) routeRequestCount++; });

  expect(await page.locator("#evacuation-form").evaluate((form) => form.checkValidity())).toBe(false);
  await page.locator("#route-plan").click();
  expect(await page.locator("#origin-city").evaluate((input) => input.validationMessage.length > 0)).toBe(true);
  expect(routeRequestCount).toBe(0);

  await page.locator("#origin-city").fill("abc");
  await page.locator("#destination-city").fill("021");
  expect(await page.locator("#evacuation-form").evaluate((form) => form.checkValidity())).toBe(false);
  expect(await page.locator("#origin-city").evaluate((input) => input.validationMessage.length > 0)).toBe(true);
  expect(routeRequestCount).toBe(0);

  await page.locator("#origin-city").fill("028");
  const pending = page.waitForRequest((value) => value.url().endsWith(ROUTES_PATH));
  await planRoutes(page);
  const body = (await pending).postDataJSON();
  expect(body).toMatchObject({ mode: "transit", originCity: "028", destinationCity: "021" });
  await expect(page.locator("#route-results .result-item")).toHaveCount(1);
});

test("驾车和步行请求不携带已填写的公交 citycode", async ({ page, request }) => {
  await setScenario(request, "success");
  await openWorkbench(page);
  await fillCoordinates(page);
  await selectTravelMode(page, "transit");
  await page.locator("#origin-city").fill("028");
  await page.locator("#destination-city").fill("021");

  for (const mode of ["driving", "walking"]) {
    await selectTravelMode(page, mode);
    await expect(page.locator("#transit-city-fields")).toBeHidden();
    await expect(page.locator("#origin-city")).toBeDisabled();
    await expect(page.locator("#destination-city")).toBeDisabled();
    await expect(page.locator("#origin-city")).not.toHaveAttribute("required", "");
    await expect(page.locator("#destination-city")).not.toHaveAttribute("required", "");
    const pending = page.waitForRequest((value) => value.url().endsWith(ROUTES_PATH));
    await planRoutes(page);
    const body = (await pending).postDataJSON();
    expect(body.mode).toBe(mode);
    expect(body).not.toHaveProperty("originCity");
    expect(body).not.toHaveProperty("destinationCity");
  }
});

for (const scenario of INVALID_SNAPSHOT_SCENARIOS) {
  test(`${scenario} 风险快照 wire 契约异常时 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openWorkbench(page);
    await fillOrigin(page);
    await page.locator("#facility-search").click();
    await expectFacilityFailClosed(page, "未沿用上一次设施结果");
  });
}

test("短有效期跨越后设施和路线状态与限制同步过期", async ({ page, request }) => {
  await setScenario(request, "short_validity");
  await page.clock.install({ time: FIXED_NOW });
  await openWorkbench(page);
  await fillCoordinates(page);
  await searchFacilities(page);
  await planRoutes(page);
  await expect(page.locator("#facility-status")).toHaveClass(/result-state-current/);
  await expect(page.locator("#route-status")).toHaveClass(/result-state-current/);

  await page.clock.fastForward(600_100);

  await expect(page.locator("#facility-status")).toHaveClass(/result-state-warning/);
  await expect(page.locator("#route-status")).toHaveClass(/result-state-warning/);
  await expect(page.locator("#facility-results")).toHaveAttribute("data-freshness", "expired");
  await expect(page.locator("#route-results")).toHaveAttribute("data-freshness", "expired");
  await expect(page.locator("#excluded-results")).toContainText("结果已跨过风险快照有效期");
});

test("有效期内 fallback 明确显示最后成功风险图层", async ({ page, request }) => {
  await setScenario(request, "fallback_unexpired");
  await page.clock.install({ time: FIXED_NOW });
  await openWorkbench(page);
  await fillCoordinates(page);
  await searchFacilities(page);
  await planRoutes(page);

  await expect(page.locator("#facility-status")).toContainText("有效期内的最后成功风险图层");
  await expect(page.locator("#route-status")).toContainText("有效期内的最后成功风险图层");
  await expect(page.locator("#facility-results")).toHaveAttribute("data-freshness", "fallback");
  await expect(page.locator("#route-results")).toHaveAttribute("data-freshness", "fallback");
});

test("fallback 跨越有效期后升级为过期状态", async ({ page, request }) => {
  await setScenario(request, "fallback_then_expiry");
  await page.clock.install({ time: FIXED_NOW });
  await openWorkbench(page);
  await fillCoordinates(page);
  await searchFacilities(page);
  await planRoutes(page);
  await expect(page.locator("#facility-results")).toHaveAttribute("data-freshness", "fallback");
  await expect(page.locator("#route-results")).toHaveAttribute("data-freshness", "fallback");

  await page.clock.fastForward(600_100);

  await expect(page.locator("#facility-results")).toHaveAttribute("data-freshness", "expired");
  await expect(page.locator("#route-results")).toHaveAttribute("data-freshness", "expired");
  await expect(page.locator("#facility-status")).toContainText("风险快照已过期");
  await expect(page.locator("#route-status")).toContainText("风险快照已过期");
});

test("零设施明确限定为本次供应商有界结果", async ({ page, request }) => {
  await setScenario(request, "zero_facilities");
  await openWorkbench(page);
  await fillOrigin(page);
  await searchFacilities(page);

  await expect(page.locator("#facility-status")).toHaveClass(/result-state-current/);
  await expect(page.locator("#facility-count")).toHaveText("0 / 0 个可选，0 / 0 个排除");
  await expect(page.locator("#facility-results")).toContainText("本次有界供应商结果中没有风险区外候选设施");
  await expect(page.locator("#facility-results")).toContainText("不能据此断言周边不存在其他设施");
});

test("真实 mapapi 对零米设施仍显式返回距离字段", async ({ page, request }) => {
  await setScenario(request, "facility_zero_distance");
  await openWorkbench(page);
  await fillOrigin(page);
  const payload = await searchFacilitiesAndRead(page);

  expect(Object.hasOwn(payload.data.facilities[0], "distanceMeters")).toBeTruthy();
  expect(payload.data.facilities[0].distanceMeters).toBe(0);
  await expect(page.locator("#facility-results")).toContainText("0 m");
});

test("供应商无风险分数时显示未提供而不是零分", async ({ page, request }) => {
  await setScenario(request, "risk_score_unavailable");
  await openWorkbench(page);
  await fillCoordinates(page);
  await planRoutes(page);

  await expect(page.locator("#route-results")).toContainText("未提供，仅执行风险区相交闸门");
  await expect(page.locator("#route-results")).not.toContainText("0.0 / 100");
  await expect(page.locator("#route-status")).toContainText("供应商未提供风险分数");
});

test("供应商显式零风险分数显示为零而不是未提供", async ({ page, request }) => {
  await setScenario(request, "risk_score_zero");
  await openWorkbench(page);
  await fillCoordinates(page);
  await planRoutes(page);

  await expect(page.locator("#route-results")).toContainText("0.0 / 100");
  await expect(page.locator("#route-results")).not.toContainText("未提供，仅执行风险区相交闸门");
});

test("真实 mapapi 将无步骤和无限制路线规范化为空数组", async ({ page, request }) => {
  await setScenario(request, "route_empty_optional_lists");
  await openWorkbench(page);
  await fillCoordinates(page);
  const payload = await planRoutesAndRead(page);

  expect(payload.data.routes[0].steps).toEqual([]);
  expect(payload.data.routes[0].limitations).toEqual([]);
  await expect(page.locator("#route-results")).toContainText("供应商未提供分步导航指引");
});

test("混合风险分数按每条路线的 provided 状态展示", async ({ page, request }) => {
  await setScenario(request, "risk_score_mixed");
  await openWorkbench(page);
  await fillCoordinates(page);
  const payload = await planRoutesAndRead(page);

  const routes = page.locator("#route-results .result-item");
  await expect(routes).toHaveCount(2);
  await expect(routes.nth(0)).toContainText("27.5 / 100");
  await expect(routes.nth(1)).toContainText("未提供，仅执行风险区相交闸门");
  expect(payload.data.routes.map((route) => route.id)).toEqual(["route-scored", "route-missing"]);
  expect(payload.data.routes.map((route) => route.rank)).toEqual([1, 2]);
  expect(payload.data.routes.every((route) => route.intersectsRiskZone === false)).toBeTruthy();
});

test("设施数量达到供应商首屏 25 条上限时正常展示", async ({ page, request }) => {
  await setScenario(request, "facility_page_limit");
  await openWorkbench(page);
  await fillOrigin(page);
  const payload = await searchFacilitiesAndRead(page);

  await expect(page.locator("#facility-results .result-item")).toHaveCount(25);
  await expect(page.locator("#facility-count")).toHaveText("25 / 25 个可选，0 / 0 个排除");
  expect(payload.data.filter.candidateCount).toBe(25);
  expect(payload.data.limits.providerPageSizeLimit).toBe(25);
});

test("设施数量超过供应商首屏 25 条时真实 mapapi fail-closed", async ({ page, request }) => {
  await setScenario(request, "facility_excess_count");
  await openWorkbench(page);
  await fillOrigin(page);
  await page.locator("#facility-search").click();
  await expectFacilityFailClosed(page, "地图供应商结果不满足安全显示约束");
});

test("路线数量由真实 mapapi 投影为 10 可见和 2 省略", async ({ page, request }) => {
  await setScenario(request, "route_excess_count");
  await openWorkbench(page);
  await fillCoordinates(page);
  const payload = await planRoutesAndRead(page);

  await expect(page.locator("#route-results .result-item")).toHaveCount(10);
  await expect(page.locator("#route-count")).toHaveText("10 / 12 条可选，0 / 0 条排除");
  await expect(page.locator("#excluded-results")).toContainText("路线响应受展示数量和总顶点上限约束");
  expect(payload.data.omittedRouteCount).toBe(2);
  expect(payload.data.routes.map((route) => route.rank)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
});

test("真实路线安全服务重算唯一排名并输出候选安全标记", async ({ page, request }) => {
  await setScenario(request, "route_rank_contract");
  await openWorkbench(page);
  await fillCoordinates(page);
  const payload = await planRoutesAndRead(page);

  expect(payload.data.routes.map((route) => route.id)).toEqual([
    "route-score-10-fast", "route-score-10-slow", "route-score-40", "route-missing"
  ]);
  expect(payload.data.routes.map((route) => route.rank)).toEqual([1, 2, 3, 4]);
  expect(payload.data.routes.every((route) => route.intersectsRiskZone === false)).toBeTruthy();
  expect(payload.data.routes.every((route) => route.geometryByteCount > 0)).toBeTruthy();
  await expect(page.locator("#route-results .result-item h4")).toHaveText([
    "候选路线 #1", "候选路线 #2", "候选路线 #3", "候选路线 #4"
  ]);
});

test("供应商风险区相交标记通过真实安全服务进入排除明细", async ({ page, request }) => {
  await setScenario(request, "route_intersects_flag");
  await openWorkbench(page);
  await fillCoordinates(page);
  const payload = await planRoutesAndRead(page);

  expect(payload.data.routes).toEqual([]);
  expect(payload.data.excluded).toHaveLength(1);
  expect(payload.data.excluded[0].route.intersectsRiskZone).toBe(true);
  expect(payload.data.excluded[0].riskZoneIds).toEqual([]);
  expect(payload.data.excluded[0].omittedRiskZoneIdCount).toBe(0);
  expect(payload.data.excluded[0].reason).toContain("供应商已标记路线穿越风险区");
  await expect(page.locator("#route-results")).toContainText("没有通过当前风险区相交闸门");
  await expect(page.locator("#excluded-results")).toContainText("供应商已标记路线穿越风险区");
});

test("真实风险区几何将设施和路线全部排除时顶层状态不可用", async ({ page, request }) => {
  await setScenario(request, "risk_zone_all_excluded");
  await openWorkbench(page);
  await fillCoordinates(page);
  await searchFacilities(page);
  await planRoutes(page);

  await expect(page.locator("#facility-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#route-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#facility-status")).toContainText("全部被当前风险区门禁排除");
  await expect(page.locator("#route-status")).toContainText("全部被当前风险区门禁排除");
  await expect(page.locator("#facility-count")).toHaveText("0 / 0 个可选，1 / 1 个排除");
  await expect(page.locator("#route-count")).toHaveText("0 / 0 条可选，1 / 1 条排除");
  await expect(page.locator("#excluded-results")).toContainText("zone-all-excluded");
});

test("真实风险区全排除的 fallback 跨期后仍保留双重不可用事实", async ({ page, request }) => {
  await setScenario(request, "risk_zone_all_excluded_fallback_then_expiry");
  await page.clock.install({ time: FIXED_NOW });
  await openWorkbench(page);
  await fillCoordinates(page);
  await searchFacilities(page);
  await planRoutes(page);

  await expect(page.locator("#facility-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#route-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#facility-status")).toContainText("来自最后成功回退风险图层");
  await expect(page.locator("#route-status")).toContainText("来自最后成功回退风险图层");
  await expect(page.locator("#facility-status")).toContainText("全部被当前风险区门禁排除");
  await expect(page.locator("#route-status")).toContainText("全部被当前风险区门禁排除");
  await expect(page.locator("#facility-results")).toHaveAttribute("data-freshness", "fallback");
  await expect(page.locator("#route-results")).toHaveAttribute("data-freshness", "fallback");

  await page.clock.fastForward(600_100);

  await expect(page.locator("#facility-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#route-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#facility-status")).toContainText("设施结果已过期，且");
  await expect(page.locator("#route-status")).toContainText("路线结果已过期，且");
  await expect(page.locator("#facility-status")).toContainText("全部被当前风险区门禁排除");
  await expect(page.locator("#route-status")).toContainText("全部被当前风险区门禁排除");
  await expect(page.locator("#facility-results")).toHaveAttribute("data-freshness", "expired");
  await expect(page.locator("#route-results")).toHaveAttribute("data-freshness", "expired");
});

test("设施可写入候选终点、清除旧路线并继续规划", async ({ page, request }) => {
  await setScenario(request, "success");
  await openWorkbench(page);
  await fillOrigin(page);
  await page.locator("#destination-longitude").fill("104.095000");
  await page.locator("#destination-latitude").fill("30.605000");
  await searchFacilities(page);
  await planRoutes(page);
  await expect(page.locator("#route-results .result-item")).toHaveCount(1);

  await page.getByRole("button", { name: "设为候选终点" }).click();

  await expect(page.locator("#destination-longitude")).toHaveValue("104.082000");
  await expect(page.locator("#destination-latitude")).toHaveValue("30.590000");
  await expect(page.locator("#route-status")).toContainText("终点已改变，旧路线结果已清除");
  await expect(page.locator("#route-count")).toHaveText("尚未规划");
  await expect(page.locator("#route-results .result-item")).toHaveCount(0);

  const payload = await planRoutesAndRead(page);
  expect(payload.data.routes[0].destination).toEqual({ longitude: 104.082, latitude: 30.59 });
  await expect(page.locator("#route-results .result-item")).toHaveCount(1);
});

test("设施全部由真实响应字节上限省略时保持不可用", async ({ page, request }) => {
  await setScenario(request, "facility_all_omitted");
  await openWorkbench(page);
  await fillOrigin(page);
  const payload = await searchFacilitiesAndRead(page);

  expect(payload.data.filter).toMatchObject({ allowedCount: 1, visibleAllowedCount: 0, omittedAllowedCount: 1 });
  await expect(page.locator("#facility-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#facility-results")).toContainText("全部因响应安全上限省略");
});

test("路线全部由真实响应字节上限省略时保持不可用", async ({ page, request }) => {
  await setScenario(request, "route_all_omitted");
  await openWorkbench(page);
  await fillCoordinates(page);
  const payload = await planRoutesAndRead(page);

  expect(payload.data).toMatchObject({ totalRouteCount: 1, visibleRouteCount: 0, omittedRouteCount: 1 });
  await expect(page.locator("#route-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#route-results")).toContainText("全部因响应安全上限省略");
});

test("设施与路线全省略后跨期仍保留省略和过期双重事实", async ({ page, request }) => {
  await setScenario(request, "all_omitted_short_validity");
  await page.clock.install({ time: FIXED_NOW });
  await openWorkbench(page);
  await fillCoordinates(page);
  await searchFacilities(page);
  await planRoutes(page);
  await expect(page.locator("#facility-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#route-status")).toHaveClass(/result-state-unavailable/);

  await page.clock.fastForward(600_100);

  await expect(page.locator("#facility-status")).toContainText("已过期，且风险区外候选设施全部");
  await expect(page.locator("#route-status")).toContainText("已过期，且候选路线全部");
  await expect(page.locator("#facility-status")).toHaveClass(/result-state-unavailable/);
  await expect(page.locator("#route-status")).toHaveClass(/result-state-unavailable/);
});

test("缺失设施省略计数的 raw wire 契约 fail-closed", async ({ page, request }) => {
  await setScenario(request, "facility_missing_omitted_count");
  await openWorkbench(page);
  await fillOrigin(page);
  await page.locator("#facility-search").click();
  await expectFacilityFailClosed(page, "省略可选设施数无效");
});

test("缺失路线省略计数的 raw wire 契约 fail-closed", async ({ page, request }) => {
  await setScenario(request, "route_missing_omitted_count");
  await openWorkbench(page);
  await fillCoordinates(page);
  await page.locator("#route-plan").click();
  await expectRouteFailClosed(page, "省略候选路线数无效");
});

test("超大路线几何 fail-closed", async ({ page, request }) => {
  await setScenario(request, "route_excess_geometry");
  await openWorkbench(page);
  await fillCoordinates(page);
  await page.locator("#route-plan").click();
  await expectRouteFailClosed(page, "地图供应商结果不满足安全显示约束");
});

test("超大接口响应 fail-closed", async ({ page, request }) => {
  await setScenario(request, "oversized_response");
  await openWorkbench(page);
  await fillOrigin(page);
  await page.locator("#facility-search").click();
  await expectFacilityFailClosed(page, "接口响应超过浏览器安全上限");
});

for (const viewport of [{ width: 390, height: 844 }, { width: 1440, height: 900 }]) {
  test(`${viewport.width}x${viewport.height} 无明显重叠且地图非空`, async ({ page, request }) => {
    await page.setViewportSize(viewport);
    await setScenario(request, "success");
    await openWorkbench(page);
    await fillCoordinates(page);
    await searchFacilities(page);
    await planRoutes(page);
    await page.locator("#evacuation").scrollIntoViewIfNeeded();

    const map = await mapDiagnostics(page);
    expect(map.width).toBeGreaterThan(300);
    expect(map.height).toBeGreaterThanOrEqual(400);
    expect(map.hasPane).toBeTruthy();
    expect(map.pathCount > 0 || map.canvasHasPixels).toBeTruthy();
    expect(map.background).not.toBe("rgba(0, 0, 0, 0)");

    const layout = await layoutDiagnostics(page);
    expect(layout.sectionOverflow).toBeLessThanOrEqual(2);
    expect(layout.controlCollisions).toEqual([]);
    expect(layout.textOverflow).toEqual([]);
  });
}

async function setScenario(request, name) {
  const response = await request.post("/__fixture/scenario", { data: { name } });
  expect(response.ok()).toBeTruthy();
}

async function openWorkbench(page) {
  await page.goto("/#evacuation");
  await expect(page.locator("#evacuation")).toBeVisible();
  await expect(page.locator("#evacuation-map-canvas .leaflet-map-pane")).toBeAttached();
}

async function fillOrigin(page) {
  await page.locator("#origin-longitude").fill("104.066541");
  await page.locator("#origin-latitude").fill("30.572269");
}

async function fillCoordinates(page) {
  await fillOrigin(page);
  await page.locator("#destination-longitude").fill("104.082000");
  await page.locator("#destination-latitude").fill("30.590000");
}

async function searchFacilities(page) {
  await page.locator("#facility-search").click();
  await expect(page.locator("#facility-status")).not.toHaveClass(/result-state-loading/);
}

async function searchFacilitiesAndRead(page) {
  const pending = page.waitForRequest((request) => request.url().endsWith(FACILITIES_PATH));
  await searchFacilities(page);
  return fetchWire(page, FACILITIES_PATH, (await pending).postDataJSON());
}

async function planRoutes(page) {
  const formIsValid = await page.locator("#evacuation-form").evaluate((form) => form.checkValidity());
  expect(formIsValid).toBe(true);
  await page.locator("#route-plan").click();
  await expect(page.locator("#route-status")).not.toHaveClass(/result-state-loading/);
}

async function selectTravelMode(page, mode) {
  const input = page.locator(`input[name="travelMode"][value="${mode}"]`);
  await page.locator(`.travel-mode label:has(input[value="${mode}"]) span`).click();
  await expect(input).toBeChecked();
}

async function planRoutesAndRead(page) {
  const pending = page.waitForRequest((request) => request.url().endsWith(ROUTES_PATH));
  await planRoutes(page);
  return fetchWire(page, ROUTES_PATH, (await pending).postDataJSON());
}

async function fetchWire(page, path, body) {
  return page.evaluate(async (input) => {
    const response = await fetch(input.path, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input.body)
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error && payload.error.message || `HTTP ${response.status}`);
    return payload;
  }, { path, body });
}

async function expectFacilityFailClosed(page, message) {
  await expect(page.locator("#facility-status")).toHaveClass(/result-state-error/);
  await expect(page.locator("#facility-status")).toContainText(message);
  await expect(page.locator("#facility-results .result-item")).toHaveCount(0);
  await expect(page.locator("#facility-results")).toContainText("尚无与当前输入绑定的设施结果");
  await expect(page.locator("#facility-count")).toHaveText("尚未搜索");
}

async function expectRouteFailClosed(page, message) {
  await expect(page.locator("#route-status")).toHaveClass(/result-state-error/);
  await expect(page.locator("#route-status")).toContainText(message);
  await expect(page.locator("#route-results .result-item")).toHaveCount(0);
  await expect(page.locator("#route-results")).toContainText("尚无与当前输入绑定的路线结果");
  await expect(page.locator("#route-count")).toHaveText("尚未规划");
}

async function mapDiagnostics(page) {
  return page.locator("#evacuation-map-canvas").evaluate((element) => {
    const canvases = Array.from(element.querySelectorAll(".leaflet-overlay-pane canvas"));
    const canvasHasPixels = canvases.some((canvas) => {
      const context = canvas.getContext("2d");
      if (!context || canvas.width === 0 || canvas.height === 0) return false;
      const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
      for (let index = 3; index < pixels.length; index += 4) {
        if (pixels[index] > 0) return true;
      }
      return false;
    });
    return {
      width: element.clientWidth,
      height: element.clientHeight,
      hasPane: Boolean(element.querySelector(".leaflet-map-pane")),
      pathCount: element.querySelectorAll(".leaflet-overlay-pane path").length,
      canvasHasPixels,
      background: getComputedStyle(element).backgroundColor
    };
  });
}

async function layoutDiagnostics(page) {
  return page.locator("#evacuation").evaluate((section) => {
    const visible = (element) => {
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
    };
    const label = (element) => element.id || element.getAttribute("name") || element.textContent.trim().slice(0, 24);
    const controls = Array.from(section.querySelectorAll("button, input:not([type=radio]), select")).filter(visible);
    const controlCollisions = [];
    for (let left = 0; left < controls.length; left++) {
      for (let right = left + 1; right < controls.length; right++) {
        const first = controls[left].getBoundingClientRect();
        const second = controls[right].getBoundingClientRect();
        const overlapX = Math.min(first.right, second.right) - Math.max(first.left, second.left);
        const overlapY = Math.min(first.bottom, second.bottom) - Math.max(first.top, second.top);
        if (overlapX > 1 && overlapY > 1) controlCollisions.push(`${label(controls[left])} <> ${label(controls[right])}`);
      }
    }
    const textSelector = "h2, h3, h4, .dispatch-state, .result-state, .result-heading, .result-item p, .result-item dd, .empty-result, .segmented-control span, .segmented-control button, .evacuation-actions button";
    const textOverflow = Array.from(section.querySelectorAll(textSelector)).filter(visible)
      .filter((element) => element.scrollWidth > element.clientWidth + 2).map(label);
    return { sectionOverflow: section.scrollWidth - section.clientWidth, controlCollisions, textOverflow };
  });
}
