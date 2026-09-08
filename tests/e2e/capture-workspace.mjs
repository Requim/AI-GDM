import { mkdir } from "node:fs/promises";
import { chromium, expect } from "@playwright/test";

// 隔离夹具的视觉验收：真实底图不拦截，业务结果使用明确标识的测试数据。
const output = process.env.QA_CAPTURE_DIR;
if (!output) throw new Error("必须指定 QA_CAPTURE_DIR");
const targets = {
  "risk-map": process.env.QA_RISK_URL,
  evacuation: process.env.QA_EVACUATION_URL,
  assessment: process.env.QA_ASSESSMENT_URL,
  sources: process.env.QA_RISK_URL
};
if (Object.values(targets).some(value => !value)) throw new Error("必须指定三组隔离夹具地址");
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
try {
  await captureAll();
} finally {
  await browser.close();
}

async function captureAll() {
  for (const [width, height] of [[1440, 900], [1920, 1080], [768, 1024], [390, 844]]) {
    const page = await browser.newPage({ viewport: { width, height }, locale: "zh-CN", timezoneId: "Asia/Shanghai" });
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.clock.install({ time: new Date("2026-08-28T00:00:00Z") });
    for (const [key, baseURL] of Object.entries(targets)) {
      const response = await page.request.post(baseURL + "/__fixture/scenario", { data: { name: "success" } });
      expect(response.ok()).toBe(true);
      await page.goto(baseURL + "/#" + key, { waitUntil: "domcontentloaded" });
      await expect(page.locator("#" + key)).toBeVisible();
      if (key === "assessment") await prepareAssessment(page);
      if (key === "evacuation") await prepareEvacuation(page);
      if (key === "risk-map") await expect(page.locator("#risk-map-message")).toHaveClass(/map-state-current/);
      await checkLayout(page);
      if (["risk-map", "evacuation"].includes(key)) await checkMap(page, key);
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.screenshot({ path: `${output}/${key}-${width}.png`, fullPage: true });
    }
    expect(errors).toEqual([]);
    await page.close();
  }
}

async function prepareAssessment(page) {
  await page.locator("#loss-advanced-options > summary").click();
  await page.locator("#loss-snapshot-id").fill("snapshot-e2e-20260828");
  await page.locator("#loss-assessment-run").click();
  await expect(page.locator("#loss-assessment-status")).toHaveClass(/assessment-state-current/);
  await page.locator("#loss-advanced-options > summary").click();
}

async function prepareEvacuation(page) {
  await page.locator("#origin-longitude").fill("104.066541");
  await page.locator("#origin-latitude").fill("30.572269");
  await page.locator("#destination-longitude").fill("104.082000");
  await page.locator("#destination-latitude").fill("30.590000");
  await page.locator("#facility-search").click();
  await expect(page.locator("#facility-results .result-item")).not.toHaveCount(0);
  await page.locator("#route-plan").click();
  await expect(page.locator("#route-results .result-item")).not.toHaveCount(0);
}

async function checkLayout(page) {
  await page.evaluate(() => document.fonts.ready);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const missing = await page.locator('img[src^="/assets/"]').evaluateAll(images =>
    images.filter(image => !image.complete || image.naturalWidth === 0).map(image => image.src));
  expect(missing).toEqual([]);
}

async function checkMap(page, key) {
  const selector = key === "risk-map" ? "#risk-map-canvas" : "#evacuation-map-canvas";
  let tiles = "loaded";
  try {
    await expect.poll(() => page.locator(selector + " .leaflet-tile").evaluateAll(images =>
      images.filter(image => image.complete && image.naturalWidth > 1).length), { timeout: 15000 }).toBeGreaterThan(0);
  } catch (error) {
    if (process.env.QA_ALLOW_BASEMAP_FAILURE !== "1") throw error;
    tiles = "unavailable";
    await expect(page.locator(key === "risk-map" ? "#risk-basemap-status" : "#evacuation-basemap-status")).toBeVisible();
  }
  const painted = await page.locator(selector + " canvas").evaluateAll(canvases => canvases.some(canvas => {
    const context = canvas.getContext("2d");
    if (!context || !canvas.width || !canvas.height) return false;
    return context.getImageData(0, 0, canvas.width, canvas.height).data.some((value, index) => index % 4 === 3 && value > 0);
  }));
  expect(painted).toBe(true);
  console.log(JSON.stringify({ key, width: page.viewportSize().width, tiles, canvas: "painted" }));
}
