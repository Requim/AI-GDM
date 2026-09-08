import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.route(/^https:\/\/[a-z]\.tile\.openstreetmap\.org\//, route => route.abort());
});

test("地图默认首页，导航保留输入并支持历史记录", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#risk-map")).toBeVisible();
  await expect(page.locator("#sources")).toBeHidden();
  await page.locator('[data-workspace-link="evacuation"]').click();
  await page.locator("#origin-longitude").fill("104.066541");
  await page.locator('[data-workspace-link="sources"]').click();
  await expect(page.locator("#sources")).toBeVisible();
  await page.goBack();
  await expect(page.locator("#evacuation")).toBeVisible();
  await expect(page.locator("#origin-longitude")).toHaveValue("104.066541");
  await page.goForward();
  await expect(page.locator("#sources")).toBeVisible();
  await page.goto("/#evacuation");
  await expect(page.locator("#evacuation-map-canvas")).toBeVisible();
  await expect(page.locator("#evacuation-map-canvas")).toHaveClass(/leaflet-container/);
  const bounds = await page.locator("#evacuation-map-canvas").boundingBox();
  expect(bounds.width).toBeGreaterThan(200);
  expect(bounds.height).toBeGreaterThan(300);
});

test("授权对话框支持 Escape、焦点恢复且不持久化令牌", async ({ page }) => {
  await page.goto("/");
  await page.locator("#admin-auth-open").click();
  await expect(page.locator("#admin-token")).toBeFocused();
  await page.locator("#admin-token").fill("workspace-test-token-not-a-real-secret-123");
  await page.locator("#admin-auth-submit").click();
  await expect(page.locator("#header-auth-status")).toHaveText("已授权");
  await page.keyboard.press("Escape");
  await expect(page.locator("#admin-auth-dialog")).not.toBeVisible();
  await expect(page.locator("#admin-auth-open")).toBeFocused();
  await page.locator('[data-workspace-link="sources"]').click();
  await expect(page.locator("#header-auth-status")).toHaveText("已授权");
  expect(await page.evaluate(() => localStorage.length + sessionStorage.length)).toBe(0);
  await page.reload();
  await expect(page.locator("#header-auth-status")).toHaveText("未授权");
});

test("底图失败不冒充风险接口失败", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#risk-basemap-status")).toBeVisible();
  await expect(page.locator("#risk-map-message")).not.toHaveText(/底图/);
});

test("风险分区隐藏时仍清除超出参考窗口的快照", async ({ page, request }) => {
  await request.post("/__fixture/scenario", { data: { name: "short_validity" } });
  await page.clock.install({ time: new Date("2026-08-28T00:00:00Z") });
  await page.goto("/");
  await expect(page.locator("#loss-snapshot-id")).toHaveValue("snapshot-browser");
  await page.locator('[data-workspace-link="evacuation"]').click();
  await page.clock.fastForward(73 * 60 * 60 * 1000);
  await expect(page.locator("#loss-snapshot-id")).toHaveValue("");
  await expect(page.locator("#loss-assessment-run")).toBeDisabled();
});

for (const width of [1920, 1440, 768, 390]) {
  test(`${width}px 工作区无横向溢出且导航可用`, async ({ page }) => {
    const height = width === 768 ? 1024 : width === 390 ? 844 : width === 1920 ? 1080 : 900;
    await page.setViewportSize({ width, height });
    await page.goto("/");
    for (const key of ["risk-map", "sources", "evacuation", "assessment"]) {
      if (width <= 900) await page.locator("#navigation-toggle").click();
      await page.locator(`[data-workspace-link="${key}"]`).click();
      await expect(page.locator(`#${key}`)).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      if (process.env.QA_CAPTURE_DIR) {
        await page.screenshot({ path: `${process.env.QA_CAPTURE_DIR}/${key}-${width}.png`, fullPage: true });
      }
    }
  });
}
