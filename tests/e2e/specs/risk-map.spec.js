import { expect, test } from "@playwright/test";

const API_PATH = "/api/v1/hazards/landslide/risks/latest/map";
const FIXED_NOW = new Date("2026-08-28T00:00:00Z");

test.beforeEach(async ({ page }) => {
  await page.route(/^https:\/\/[a-z]\.tile\.openstreetmap\.org\//, async (route) => {
    await route.fulfill({ status: 204, body: "" });
  });
});

for (const scenario of ["missing_valid_to", "invalid_valid_to"]) {
  test(`${scenario} 对缺失或非法有效期执行 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await page.goto("/");
    await expectUnavailable(page, "风险数据有效期缺失或无效");
    await expectClearedMetadata(page);
  });
}

test("假时钟跨过 validTo 后同步降级全部当前状态", async ({ page, request }) => {
  await setScenario(request, "short_validity");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");
  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
  await expect(page.locator("#risk-data-status")).toHaveText("当前数据");

  await page.clock.fastForward(2_100);

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-stale/);
  await expect(page.locator("#risk-decision-level")).toHaveText("已过期 / 高");
  await expect(page.locator("#risk-data-status")).toHaveText("数据已过期");
  await expect(page.locator("#risk-assessment-status")).toContainText("数据已过期");
  await expect(page.locator("#risk-limitations-list")).toContainText("已跨过有效期");
});

test("成功结果后 503 清除旧模型、来源、置信度和规则", async ({ page, request }) => {
  await setScenario(request, "success_then_503");
  await page.goto("/");
  await expectLoadedMetadata(page);

  await page.locator("#risk-map-refresh").click();

  await expectUnavailable(page, "实时风险数据暂时不可用");
  await expectClearedMetadata(page);
});

test("成功结果后请求超时同样清除旧结论", async ({ page, request }) => {
  await setScenario(request, "success_then_timeout");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");
  await expectLoadedMetadata(page);
  const pending = page.waitForRequest((value) => value.url().endsWith(API_PATH));
  await page.locator("#risk-map-refresh").click();
  await pending;

  await page.clock.fastForward(20_100);

  await expectUnavailable(page, "请求处理超时");
  await expectClearedMetadata(page);
});

for (const scenario of ["too_many_zones", "complex_geometry"]) {
  test(`${scenario} 超过浏览器资源边界时 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await page.goto("/");
    await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-unavailable/);
    await expect(page.locator("#risk-visible-count")).toHaveText("未显示风险区");
    await expect(page.locator("#risk-limitations-list")).toContainText("禁止沿用页面上一次成功结果");
    await expectClearedMetadata(page);
  });
}

async function setScenario(request, name) {
  const response = await request.post("/__fixture/scenario", { data: { name } });
  expect(response.ok()).toBeTruthy();
}

async function expectLoadedMetadata(page) {
  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
  await expect(page.locator("#risk-model")).toContainText("NASA LHASA");
  await expect(page.locator("#risk-source")).toContainText("NASA Earthdata");
  await expect(page.locator("#risk-confidence")).toContainText("高（输入质量）");
  await expect(page.locator("#risk-rule-version")).toHaveText("ai-gdm-risk-rules-v1");
}

async function expectUnavailable(page, message) {
  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-unavailable/);
  await expect(page.locator("#risk-map-message")).toContainText(message);
  await expect(page.locator("#risk-visible-count")).toHaveText("未显示风险区");
}

async function expectClearedMetadata(page) {
  await expect(page.locator("#risk-decision-level")).toHaveText("不可用");
  await expect(page.locator("#risk-data-status")).toHaveText("不可用");
  await expect(page.locator("#risk-confidence")).toHaveText("未提供");
  await expect(page.locator("#risk-model")).toHaveText("未提供");
  await expect(page.locator("#risk-source")).toHaveText("未提供");
  await expect(page.locator("#risk-rule-version")).toHaveText("未提供");
}
