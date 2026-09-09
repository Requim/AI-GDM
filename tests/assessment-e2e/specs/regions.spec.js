import { expect, test } from "@playwright/test";

const snapshot = "snapshot-e2e-20260828";
const base = "/api/v1/loss";

test.beforeEach(async ({ page, request }) => {
  await request.post("/__fixture/scenario", { data: { name: "success" } });
  await page.route(/^https:\/\/backup\.opentopomap\.org\//, route => route.abort());
  await page.goto("/#assessment");
  await page.locator("#loss-advanced-options > summary").click();
  await page.locator("#loss-snapshot-id").fill(snapshot);
  await page.locator("#loss-province-code").selectOption("CN-130000");
  await expect(page.locator("#loss-region-code option")).toHaveCount(2);
});

test("省市联动只展示所属城市，刷新目录保留所选城市", async ({ page }) => {
  await expect(page.locator("#loss-region-code")).toContainText("石家庄市");
  await expect(page.locator("#loss-region-code")).not.toContainText("南京市");
  await page.locator("#loss-region-code").selectOption("CN-130100");
  await page.locator("#loss-region-load").click();
  await expect(page.locator("#loss-region-code")).toHaveValue("CN-130100");
  await page.locator("#loss-province-code").selectOption("CN-320000");
  await expect(page.locator("#loss-region-code")).toHaveValue("CN-320000");
  await expect(page.locator("#loss-region-code")).toContainText("南京市");
  await expect(page.locator("#loss-region-code")).not.toContainText("石家庄市");
});

test("城市投影使用正确地址，结果和 AI 引用随区域失效", async ({ page }) => {
  const paths = [];
  page.on("request", request => {
    if (request.method() === "POST" && request.url().includes(base)) {
      paths.push({ path: new URL(request.url()).pathname, body: request.postDataJSON() });
    }
  });
  await page.locator("#loss-region-code").selectOption("CN-130100");
  await page.locator("#loss-assessment-run").click();
  await expect(page.locator("#loss-explain-result")).toBeEnabled();
  expect(paths).toEqual([
    { path: base + "/regions/CN-130100/projection", body: { snapshotId: snapshot } },
    { path: base + "/assessments", body: { snapshotId: snapshot, regionCode: "CN-130100" } }
  ]);
  await page.locator("#loss-explain-result").click();
  await expect(page.locator("#ai-analysis-reference")).toHaveValue(/^loss_assessment:/);
  await page.locator("#assessment-tab-loss").click();
  await page.locator("#loss-province-code").selectOption("CN-320000");
  await expect(page.locator("#loss-explain-result")).toBeDisabled();
  await expect(page.locator('#ai-analysis-reference option[value^="loss_assessment:"]')).toHaveCount(0);
});

test("区域投影等待期间切换省份，不提交旧地区损失", async ({ page }) => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  await page.route("**/regions/CN-130000/projection", async route => {
    await gate;
    await route.fulfill({ json: { data: {
      snapshotId: snapshot, regionCode: "CN-130000", status: "available"
    } } });
  });
  const requests = [];
  page.on("request", request => {
    if (request.method() === "POST" && new URL(request.url()).pathname === base + "/assessments") requests.push(request);
  });
  const pending = page.waitForRequest("**/regions/CN-130000/projection");
  await page.locator("#loss-assessment-run").click();
  await pending;
  await page.locator("#loss-province-code").selectOption("CN-320000");
  release();
  await expect(page.locator("#loss-selected-region")).toHaveText("江苏省");
  await expect(page.locator("#loss-region-impact-status")).toContainText("江苏省");
  await page.waitForTimeout(200);
  expect(requests).toHaveLength(0);
});

test("道路供应商失败时保留独立计算的区域影响面积", async ({ page }) => {
  await expect(page.locator("#loss-region-impact-status")).toContainText("风险覆盖");
  await page.route("**/regions/CN-130000/projection", route => route.fulfill({
    status: 503, json: { error: { code: "provider_unavailable", message: "道路数据采集失败" } }
  }));
  await page.locator("#loss-assessment-run").click();
  await expect(page.locator("#loss-assessment-status")).toContainText("道路数据采集失败");
  await expect(page.locator("#loss-impact-area")).toContainText("平方公里");
  await expect(page.locator("#loss-region-impact-status")).toContainText("河北省");
  await expect(page.locator("#loss-explain-result")).toBeDisabled();
});

test("风险快照撤销时清除区域面积和地图，拒绝迟到响应", async ({ page }) => {
  await page.evaluate(snapshotID => {
    document.getElementById("loss-snapshot-id").value = "";
    document.dispatchEvent(new CustomEvent("ai-gdm:risk-snapshot", {
      detail: { available: true, snapshotId: snapshotID, state: "current" }
    }));
  }, snapshot);
  await expect.poll(() => regionalPixels(page)).toBe(true);
  await page.evaluate(() => document.dispatchEvent(new CustomEvent("ai-gdm:risk-snapshot", {
    detail: { available: false, referenceEligible: false, state: "unavailable" }
  })));
  await expect(page.locator("#loss-snapshot-id")).toHaveValue("");
  await expect(page.locator("#loss-assessment-run")).toBeDisabled();
  await expect.poll(() => regionalPixels(page)).toBe(false);
  await expect(page.locator("#loss-impact-area")).not.toContainText("平方公里");
});

async function regionalPixels(page) {
  return page.locator("#loss-region-map").evaluate(element => {
    const canvas = element.querySelector(".leaflet-overlay-pane canvas");
    if (!canvas || !canvas.width || !canvas.height) return false;
    return canvas.getContext("2d").getImageData(0, 0, canvas.width, canvas.height).data.some(
      (value, index) => index % 4 === 3 && value > 0);
  });
}

for (const [width, height] of [[1440, 900], [1920, 1080], [768, 1024], [390, 844]]) {
  test(`区域工作区在 ${width}x${height} 无横向溢出且地图可恢复`, async ({ page }, testInfo) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.setViewportSize({ width, height });
    await expect(page.locator("#loss-region-impact-status")).toContainText("风险覆盖");
    await page.locator("#assessment-tab-ai").click();
    await page.locator("#assessment-tab-loss").click();
    await expect(page.locator("#loss-region-map")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
    expect(await page.locator("#loss-region-map").evaluate(el => el.clientWidth > 200 && el.clientHeight >= 250)).toBeTruthy();
    const basemapStatus = await page.locator("#loss-region-basemap-status").innerText();
    expect(basemapStatus === "" || /不可用|失败|重试/.test(basemapStatus)).toBeTruthy();
    await page.screenshot({ path: testInfo.outputPath(`region-${width}x${height}.png`), fullPage: true });
    expect(errors).toEqual([]);
  });
}
