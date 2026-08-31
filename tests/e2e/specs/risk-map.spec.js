import { expect, test } from "@playwright/test";

const API_PATH = "/api/v1/hazards/landslide/risks/latest/map";
const FIXED_NOW = new Date("2026-08-28T00:00:00Z");
const SNAPSHOT_ID = "snapshot-browser";

test.beforeEach(async ({ page }) => {
  await page.route(/^https:\/\/[a-z]\.tile\.openstreetmap\.org\//, async (route) => {
    await route.fulfill({ status: 204, body: "" });
  });
});

const INVALID_VALID_TO_SCENARIOS = [
  "missing_valid_to",
  "missing_snapshot_valid_to",
  "missing_source_valid_to",
  "invalid_valid_to",
  "non_string_snapshot_valid_to",
  "non_string_source_valid_to",
  "non_strict_valid_to",
  "invalid_calendar_valid_to"
];

for (const scenario of INVALID_VALID_TO_SCENARIOS) {
  test(`${scenario} 对非严格 UTC 有效期执行 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await page.goto("/");
    await expectUnavailable(page, "风险数据有效期缺失或无效");
    await expectClearedMetadata(page);
  });
}

test("snapshot 与 source 有效期不一致时执行 fail-closed", async ({ page, request }) => {
  await setScenario(request, "source_valid_to_mismatch");
  await page.goto("/");
  await expectUnavailable(page, "风险快照与来源有效期不一致");
  await expectClearedMetadata(page);
});

test("当前有效风险快照自动回填损失输入并启用评估按钮", async ({ page, request }) => {
  await setScenario(request, "success");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);
  expect(await page.locator("#loss-snapshot-id").evaluate((input) => input.checkValidity())).toBe(true);
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();
  await expect(page.locator("#loss-assessment-status")).toContainText("已绑定风险地图当前有效快照");
});

test("假时钟跨过 validTo 后同步转为 expired", async ({ page, request }) => {
  await setScenario(request, "short_validity");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");
  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
  await expect(page.locator("#risk-data-status")).toHaveText("当前数据");
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);

  await page.clock.fastForward(600_100);

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-stale/);
  await expect(page.locator("#risk-decision-level")).toHaveText("已过期 / 高");
  await expect(page.locator("#risk-data-status")).toHaveText("数据已过期");
  await expect(page.locator("#risk-assessment-status")).toContainText("数据已过期");
  await expect(page.locator("#risk-limitations-list")).toContainText("已跨过有效期");
  await expect(page.locator("#loss-snapshot-id")).toHaveValue("");
  await expect(page.locator("#loss-assessment-run")).toBeDisabled();
});

test("风险区全部被省略时明确显示地图不可用", async ({ page, request }) => {
  await setScenario(request, "all_zones_omitted");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  const message = page.locator("#risk-map-message");
  await expect(message).toHaveClass(/map-state-unavailable/);
  await expect(message).toContainText("全部因地图安全上限被省略");
  await expect(message).not.toContainText("未生成达到阈值");
  await expect(message).not.toContainText("无风险");
  await expect(page.locator("#risk-visible-count")).toHaveText("已显示 0 / 1 个风险区（全部省略）");
  await expect(page.locator("#risk-model")).toContainText("NASA LHASA");
  await expect(page.locator("#risk-source")).toContainText("NASA Earthdata");
});

test("风险区全部省略后跨期仍保持不可用并保留双重事实", async ({ page, request }) => {
  await setScenario(request, "all_zones_omitted_then_expiry");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  const message = page.locator("#risk-map-message");
  await expect(message).toHaveClass(/map-state-unavailable/);
  await expect(message).toContainText("全部因地图安全上限被省略");

  await page.clock.fastForward(600_100);

  await expect(message).toHaveClass(/map-state-unavailable/);
  await expect(message).toContainText("风险数据已过期");
  await expect(message).toContainText("全部因地图安全上限被省略");
  await expect(message).not.toContainText("展示最后成功图层");
  await expect(page.locator("#risk-visible-count")).toHaveText("已显示 0 / 1 个风险区（全部省略）");
  await expect(page.locator("#risk-decision-level")).toHaveText("已过期 / 高");
  await expect(page.locator("#risk-data-status")).toHaveText("数据已过期");
  await expect(page.locator("#risk-limitations-list")).toContainText("已跨过有效期");
});

test("未过期 fallback 优先于 snapshot 与 source 的 stale 标记", async ({ page, request }) => {
  await setScenario(request, "fallback_unexpired");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-fallback/);
  await expect(page.locator("#risk-map-message")).toContainText("未过期的最后成功回退数据");
  await expect(page.locator("#risk-decision-level")).toHaveText("高");
  await expect(page.locator("#risk-data-status")).toHaveText("最后成功数据回退");
  await expect(page.locator("#risk-assessment-status")).toContainText("最后成功回退数据");
});

test("fallback 跨过 validTo 后 expired 优先", async ({ page, request }) => {
  await setScenario(request, "fallback_then_expiry");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");
  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-fallback/);

  await page.clock.fastForward(600_100);

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-stale/);
  await expect(page.locator("#risk-decision-level")).toHaveText("已过期 / 高");
  await expect(page.locator("#risk-data-status")).toHaveText("数据已过期");
});

test("成功结果后 503 清除旧模型、来源、置信度和规则", async ({ page, request }) => {
  await setScenario(request, "success_then_503");
  await page.goto("/");
  await expectLoadedMetadata(page);
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);

  await page.locator("#risk-map-refresh").click();

  await expectUnavailable(page, "实时风险数据暂时不可用");
  await expectClearedMetadata(page);
  await expect(page.locator("#loss-snapshot-id")).toHaveValue("");
  await expect(page.locator("#loss-assessment-run")).toBeDisabled();
});

test("风险地图刷新失败时保留人工输入的快照标识", async ({ page, request }) => {
  await setScenario(request, "success_then_503");
  await page.goto("/");
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);
  await page.locator("#loss-snapshot-id").fill("snapshot-manual-input");

  await page.locator("#risk-map-refresh").click();

  await expectUnavailable(page, "实时风险数据暂时不可用");
  await expect(page.locator("#loss-snapshot-id")).toHaveValue("snapshot-manual-input");
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();
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

for (const scenario of ["too_many_zones"]) {
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
