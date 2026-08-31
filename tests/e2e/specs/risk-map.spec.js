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

test("旧式外接矩形快照明确提示包含境外区域", async ({ page, request }) => {
  await setScenario(request, "legacy_bbox");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
  await expect(page.locator("#risk-coverage-scope")).toContainText("中国外接矩形预筛选");
  await expect(page.locator("#risk-coverage-scope")).toContainText("包含部分境外区域");
  await expect(page.locator("#risk-coverage-scope")).toContainText("非官方国界依据");
});

test("覆盖范围契约损坏时执行 fail-closed", async ({ page, request }) => {
  await setScenario(request, "invalid_coverage");
  await page.goto("/");
  await expectUnavailable(page, "风险地图接口契约不完整");
  await expectClearedMetadata(page);
});

test("覆盖范围版本与说明不一致时执行 fail-closed", async ({ page, request }) => {
  await setScenario(request, "coverage_version_mismatch");
  await page.goto("/");
  await expectUnavailable(page, "风险地图接口契约不完整");
  await expectClearedMetadata(page);
});

test("当前有效风险快照自动回填损失输入并启用评估按钮", async ({ page, request }) => {
  await setScenario(request, "success");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
  await expect(page.locator("#risk-visible-count")).toHaveText("本次地图绘制 1 个风险区");
  await expect(page.locator("#risk-total-count")).toHaveText("本快照范围内共 1 · 未绘制 0");
  await expect(page.locator("#risk-coverage-scope")).toContainText("CHN ADM0 边界（2019）");
  await expect(page.locator("#risk-coverage-scope")).toContainText("来源：geoBoundaries, Wikimedia Commons");
  await expect(page.locator("#risk-coverage-scope")).toContainText("许可：Public Domain");
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);
  expect(await page.locator("#loss-snapshot-id").evaluate((input) => input.checkValidity())).toBe(true);
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();
  await expect(page.locator("#loss-assessment-status")).toContainText("已绑定风险地图当前有效快照");
});

test("部分风险区未绘制时保留当前状态并解释 3000 与 24553", async ({ page, request }) => {
  await setScenario(request, "partial_omission");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  const message = page.locator("#risk-map-message");
  await expect(message).toHaveClass(/map-state-current/);
  await expect(message).toContainText("风险较高区域优先显示");
  await expect(message).toContainText("未绘制不代表无风险");
  await expect(page.locator("#risk-visible-count")).toHaveText("本次地图绘制 3,000 个风险区");
  await expect(page.locator("#risk-total-count")).toContainText("本快照范围内共 24,553 · 未绘制 21,553");
  await expect(page.locator("#risk-total-count")).toContainText("几何复杂 153");
  await expect(page.locator("#risk-total-count")).toContainText("数量、顶点或响应大小上限 21,400");
  await expect(page.locator("#risk-coverage-scope")).toContainText("CHN ADM0 边界（2019）");
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();
});

test("风险范围摘要在桌面与移动视口不横向溢出", async ({ page, request }) => {
  await setScenario(request, "success");
  await page.clock.install({ time: FIXED_NOW });
  for (const viewport of [{ width: 390, height: 844 }, { width: 1440, height: 900 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/");
    await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
    const layout = await page.locator(".risk-toolbar").evaluate((toolbar) => {
      const summary = toolbar.querySelector(".risk-count-summary");
      const toolbarRect = toolbar.getBoundingClientRect();
      const summaryRect = summary.getBoundingClientRect();
      const viewportWidth = document.documentElement.clientWidth;
      const offenders = Array.from(document.querySelectorAll("body *")).map((element) => {
        const rect = element.getBoundingClientRect();
        return { tag: element.tagName, id: element.id, className: String(element.className || ""),
          left: Math.round(rect.left), right: Math.round(rect.right), width: Math.round(rect.width) };
      }).filter((item) => item.left < -1 || item.right > viewportWidth + 1)
        .sort((left, right) => right.right - left.right).slice(0, 5);
      return {
        pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        summaryLeft: summaryRect.left - toolbarRect.left,
        summaryRight: toolbarRect.right - summaryRect.right,
        offenders
      };
    });
    expect(layout.pageOverflow, JSON.stringify(layout.offenders)).toBeLessThanOrEqual(1);
    expect(layout.summaryLeft).toBeGreaterThanOrEqual(-1);
    expect(layout.summaryRight).toBeGreaterThanOrEqual(-1);
  }
});

test("假时钟跨过 validTo 后保留72小时研究参考快照但不冒充当前", async ({ page, request }) => {
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
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();
  await expect(page.locator("#loss-assessment-status")).toContainText("最近 72 小时内最后一次成功快照");
});

test("长期开页跨过72小时降级窗口后自动清除损失评估快照", async ({ page, request }) => {
  await setScenario(request, "short_validity");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  await page.clock.fastForward(600_100);
  await expect(page.locator("#loss-snapshot-id")).toHaveValue(SNAPSHOT_ID);
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();

  await page.clock.fastForward(72 * 60 * 60 * 1000 + 100);

  await expect(page.locator("#loss-snapshot-id")).toHaveValue("");
  await expect(page.locator("#loss-assessment-run")).toBeDisabled();
  await expect(page.locator("#loss-assessment-status")).toContainText("当前没有可用于估算的数据");
});

test("超过72小时的过期风险快照不再自动绑定损失评估", async ({ page, request }) => {
  await setScenario(request, "too_old_for_loss_reference");
  await page.clock.install({ time: FIXED_NOW });
  await page.goto("/");

  await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-stale/);
  await expect(page.locator("#risk-data-status")).toHaveText("数据已过期");
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
  await expect(page.locator("#risk-visible-count")).toHaveText("本次地图绘制 0 个风险区");
  await expect(page.locator("#risk-total-count")).toContainText("本快照范围内共 1 · 未绘制 1");
  await expect(page.locator("#risk-total-count")).toContainText("几何复杂 1");
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
  await expect(page.locator("#risk-visible-count")).toHaveText("本次地图绘制 0 个风险区");
  await expect(page.locator("#risk-total-count")).toContainText("本快照范围内共 1 · 未绘制 1");
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
    await expect(page.locator("#risk-total-count")).toHaveText("总数不可用");
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
  await expect(page.locator("#risk-total-count")).toHaveText("总数不可用");
  await expect(page.locator("#risk-coverage-scope")).toHaveText("统计范围不可用");
}

async function expectClearedMetadata(page) {
  await expect(page.locator("#risk-decision-level")).toHaveText("不可用");
  await expect(page.locator("#risk-data-status")).toHaveText("不可用");
  await expect(page.locator("#risk-confidence")).toHaveText("未提供");
  await expect(page.locator("#risk-model")).toHaveText("未提供");
  await expect(page.locator("#risk-source")).toHaveText("未提供");
  await expect(page.locator("#risk-rule-version")).toHaveText("未提供");
}
