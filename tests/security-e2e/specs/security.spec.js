import { expect, test } from "@playwright/test";

const token = process.env.SECURITY_TEST_ADMIN_TOKEN || "";
const baseURL = process.env.E2E_BASE_URL || "http://127.0.0.1:18083";

async function openConsole(page, expectReady = true) {
  await page.addInitScript(() => {
    window.__securityViolations = [];
    document.addEventListener("securitypolicyviolation", (event) => {
      window.__securityViolations.push(event.violatedDirective);
    });
  });
  await page.goto("/", { waitUntil: "domcontentloaded" });
  if (expectReady) await assertAuthorizationReady(page);
}

test("管理员令牌只驻留当前页面内存并通过服务端写请求边界", async ({ page, context }) => {
  const requested = observeRequests(page);
  await openConsole(page);
  await page.locator("#admin-token").fill(token);
  await page.getByRole("button", { name: "授权", exact: true }).click();
  await expect(page.locator("#admin-token")).toHaveValue("");
  await expect(page.locator("#admin-auth-status")).toContainText("仅保留在当前页面内存");

  const result = await page.evaluate(() => window.AIGDM.requestJSON("/api/v1/security/probe", {
    method: "POST", body: { command: "probe" }
  }));
  expect(result).toEqual({ ok: true, count: 1 });
  await requested.settle();
  assertProtectedTokenTransport(requested.records);
  const storage = await page.evaluate((secret) => ({
    local: JSON.stringify(localStorage), session: JSON.stringify(sessionStorage),
    htmlContainsSecret: document.documentElement.outerHTML.includes(secret),
    authorized: window.AIGDM.hasAdminAuthorization(), violations: window.__securityViolations
  }), token);
  expect(storage).toEqual({ local: "{}", session: "{}", htmlContainsSecret: false,
    authorized: true, violations: [] });
  expect(await context.cookies()).toEqual([]);

  await page.reload({ waitUntil: "domcontentloaded" });
  expect(await page.evaluate(() => window.AIGDM.hasAdminAuthorization())).toBe(false);
  await expect(page.locator("#admin-auth-status")).toHaveText("未授权");
  const unauthorized = await page.evaluate(async () => {
    try {
      await window.AIGDM.requestJSON("/api/v1/security/probe", { method: "POST", body: {} });
      return null;
    } catch (error) {
      return { status: error.status, code: error.code, message: error.message };
    }
  });
  expect(unauthorized).toMatchObject({ status: 401, code: "admin_authorization_required" });
  await expect(page.locator("#admin-auth-status")).toContainText("重新输入管理员令牌");
  expect(await page.evaluate(() => window.AIGDM.requestJSON("/api/v1/security/probe"))).toEqual({ ok: true, count: 1 });
});

test("非法令牌和跨站接口在客户端 fail-closed", async ({ page }) => {
  await openConsole(page);
  const baseline = await page.evaluate(() => window.AIGDM.requestJSON("/api/v1/security/probe"));
  await page.locator("#admin-token").fill("short-token");
  await page.getByRole("button", { name: "授权", exact: true }).click();
  await expect(page.locator("#admin-auth-status")).toContainText("32 至 256 位");
  expect(await page.evaluate(() => window.AIGDM.hasAdminAuthorization())).toBe(false);

  await page.locator("#admin-token").fill(token);
  await page.getByRole("button", { name: "授权", exact: true }).click();
  const crossOrigin = await page.evaluate(async () => {
    try {
      await window.AIGDM.requestJSON("https://example.com/private", { method: "POST", body: {} });
      return null;
    } catch (error) {
      return { status: error.status, code: error.code, message: error.message };
    }
  });
  expect(crossOrigin).toMatchObject({ code: "cross_origin_endpoint" });
  expect(await page.evaluate(() => window.AIGDM.requestJSON("/api/v1/security/probe"))).toEqual(baseline);
});

test("脚本阻断或禁用时授权控件不会把令牌写入 URL", async ({ page, context, browser }) => {
  await page.route("**/assets/api.js", (route) => route.abort("blockedbyclient"));
  const requested = observeRequests(page);
  await openConsole(page, false);
  await assertInertAuthorization(page, context, requested);
  await assertBusinessControlsDoNotNavigate(page, requested);

  const disabledContext = await browser.newContext({ javaScriptEnabled: false, baseURL });
  const disabledPage = await disabledContext.newPage();
  const disabledRequests = observeRequests(disabledPage);
  await disabledPage.goto("/", { waitUntil: "domcontentloaded" });
  await assertInertAuthorization(disabledPage, disabledContext, disabledRequests);
  await assertBusinessControlsDoNotNavigate(disabledPage, disabledRequests);
  await disabledContext.close();
  await assertInitializationClearsBeforeEnable(browser);
});

test("401 响应体超量或中断时立即清除内存令牌", async ({ page }) => {
  await openConsole(page);
  await assertLateUnauthorizedDoesNotClearReplacement(page);
  for (const scenario of ["oversize_401", "interrupted_401"]) {
    await page.locator("#admin-token").fill(token);
    await page.getByRole("button", { name: "授权", exact: true }).click();
    expect(await page.evaluate(() => window.AIGDM.hasAdminAuthorization())).toBe(true);
    const error = await page.evaluate(async (name) => {
      try {
        await window.AIGDM.requestJSON(`/api/v1/security/probe?response=${name}`, {
          method: "POST", body: { command: "probe" },
          maxResponseBytes: name === "oversize_401" ? 64 : 1024
        });
        return null;
      } catch (reason) {
        return { name: reason.name, code: reason.code || "", message: reason.message };
      }
    }, scenario);
    expect(error).not.toBeNull();
    expect(await page.evaluate(() => window.AIGDM.hasAdminAuthorization())).toBe(false);
    await expect(page.locator("#admin-auth-status")).toContainText("重新输入管理员令牌");
  }
});

test("管理员授权控件在手机和桌面视口不产生横向溢出", async ({ page }) => {
  await openConsole(page);
  for (const viewport of [{ width: 390, height: 844 }, { width: 1440, height: 900 }]) {
    await page.setViewportSize(viewport);
    await page.reload({ waitUntil: "domcontentloaded" });
    const overflow = await page.evaluate(() => {
      const form = document.querySelector("#admin-auth-form").getBoundingClientRect();
      return {
      topbar: document.querySelector(".topbar").scrollWidth - document.querySelector(".topbar").clientWidth,
      tools: document.querySelector(".topbar-tools").scrollWidth - document.querySelector(".topbar-tools").clientWidth,
      formLeft: Math.round(form.left), formRight: Math.round(form.right), viewport: window.innerWidth
      };
    });
    expect(overflow.topbar).toBe(0);
    expect(overflow.tools).toBe(0);
    expect(overflow.formLeft).toBeGreaterThanOrEqual(0);
    expect(overflow.formRight).toBeLessThanOrEqual(overflow.viewport);
    await expect(page.locator("#admin-auth-form")).toBeVisible();
  }
});

async function assertInertAuthorization(page, context, requested) {
  const originalURL = page.url();
  const input = page.locator("#admin-token");
  await expect(input).toBeDisabled();
  await expect(page.locator("#admin-auth-submit")).toBeDisabled();
  await expect(page.locator("#admin-auth-clear")).toBeDisabled();
  await input.focus();
  await page.keyboard.type(token);
  await page.waitForTimeout(100);
  await requested.settle();
  expect(page.url()).toBe(originalURL);
  expect(requested.records.filter((request) => request.navigation)).toHaveLength(1);
  expect(JSON.stringify(requested.records)).not.toContain(token);
  await expect(input).toHaveValue("");
  expect(await input.getAttribute("name")).toBeNull();
  expect(await input.getAttribute("value")).toBeNull();
  expect(await page.locator("#admin-auth-submit").getAttribute("type")).toBe("button");
  expect(await page.locator("#admin-auth-form").evaluate((node) => ({
    tag: node.tagName, nestedForm: Boolean(node.closest("form"))
  }))).toEqual({ tag: "DIV", nestedForm: false });
  expect(await page.locator("#admin-auth-form").evaluate((node, secret) =>
    node.outerHTML.includes(secret), token)).toBe(false);
  expect(await page.evaluate(() => ({ local: JSON.stringify(localStorage), session: JSON.stringify(sessionStorage) })))
    .toEqual({ local: "{}", session: "{}" });
  expect(await context.cookies()).toEqual([]);
}

async function assertBusinessControlsDoNotNavigate(page, requested) {
  const originalURL = page.url();
  const values = ["104.066541", "30.572269", "104.082000", "30.590000", "security-snapshot-id"];
  const selectors = ["#origin-longitude", "#origin-latitude", "#destination-longitude",
    "#destination-latitude", "#loss-snapshot-id"];
  for (let index = 0; index < selectors.length; index += 1) {
    await page.locator(selectors[index]).fill(values[index]);
  }
  await page.locator("#route-plan").click();
  await page.locator("#loss-assessment-run").click();
  await page.locator("#loss-snapshot-id").press("Enter");
  await page.waitForTimeout(100);
  await requested.settle();
  expect(page.url()).toBe(originalURL);
  expect(requested.records.filter((request) => request.navigation)).toHaveLength(1);
  const serialized = JSON.stringify(requested.records);
  values.forEach((value) => expect(serialized).not.toContain(value));
  await assertBusinessControlMarkup(page);
}

async function assertBusinessControlMarkup(page) {
  for (const selector of ["#evacuation-form", "#loss-assessment-form"]) {
    expect(await page.locator(selector).evaluate((node) => ({
      tag: node.tagName, nestedForm: Boolean(node.closest("form"))
    }))).toEqual({ tag: "DIV", nestedForm: false });
  }
  for (const selector of ["#route-plan", "#loss-assessment-run"]) {
    await expect(page.locator(selector)).toHaveAttribute("type", "button");
  }
  for (const selector of ["#origin-longitude", "#origin-latitude", "#destination-longitude",
    "#destination-latitude", "#loss-snapshot-id"]) {
    expect(await page.locator(selector).getAttribute("name")).toBeNull();
  }
}

async function assertLateUnauthorizedDoesNotClearReplacement(page) {
  const replacement = (token.startsWith("A") ? "B" : "A") + token.slice(1);
  let releaseResponse;
  let markRequest;
  const requested = new Promise((resolve) => { markRequest = resolve; });
  const responseGate = new Promise((resolve) => { releaseResponse = resolve; });
  await page.route("**/api/v1/security/probe?response=late_401", async (route) => {
    markRequest();
    await responseGate;
    await route.fulfill({ status: 401, contentType: "application/json", body: '{"error":{"code":"expired"}}' });
  });
  await page.evaluate(() => {
    window.__lateUnauthorized = window.AIGDM.requestJSON("/api/v1/security/probe?response=late_401", {
      method: "POST", body: { command: "probe" }
    }).then(() => null, (error) => ({ status: error.status, code: error.code }));
  });
  await requested;
  await page.locator("#admin-token").fill(replacement);
  await page.getByRole("button", { name: "授权", exact: true }).click();
  releaseResponse();
  expect(await page.evaluate(() => window.__lateUnauthorized)).toMatchObject({ status: 401 });
  expect(await page.evaluate(() => window.AIGDM.hasAdminAuthorization())).toBe(true);
  await expect(page.locator("#admin-auth-status")).toContainText("仅保留在当前页面内存");
  await page.unroute("**/api/v1/security/probe?response=late_401");
  await assertReplacementTokenIsUsed(page, replacement);
}

async function assertReplacementTokenIsUsed(page, replacement) {
  let observed = "";
  await page.route("**/api/v1/security/probe?response=replacement", async (route) => {
    observed = (await route.request().allHeaders()).authorization || "";
    await route.fulfill({ status: 200, contentType: "application/json", body: '{"ok":true}' });
  });
  expect(await page.evaluate(() => window.AIGDM.requestJSON("/api/v1/security/probe?response=replacement", {
    method: "POST", body: { command: "probe" }
  }))).toEqual({ ok: true });
  expect(observed).toBe(`Bearer ${replacement}`);
  await page.unroute("**/api/v1/security/probe?response=replacement");
}

async function assertAuthorizationReady(page) {
  await expect(page.locator("#admin-token")).toBeEnabled();
  await expect(page.locator("#admin-token")).toHaveValue("");
  await expect(page.locator("#admin-auth-submit")).toBeEnabled();
  await expect(page.locator("#admin-auth-clear")).toBeEnabled();
}

async function assertInitializationClearsBeforeEnable(browser) {
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  await page.route("**/assets/api.js", async (route) => {
    await gate;
    await route.continue();
  });
  const navigation = page.goto("/", { waitUntil: "domcontentloaded" });
  const input = page.locator("#admin-token");
  await input.waitFor({ state: "attached" });
  await expect(input).toBeDisabled();
  await input.evaluate((node) => { node.value = "必须由初始化清空"; });
  release();
  await navigation;
  await assertAuthorizationReady(page);
  await context.close();
}

function observeRequests(page) {
  const records = [];
  const pending = [];
  page.on("request", (request) => {
    const record = { url: request.url(), method: request.method(), headers: {},
      body: request.postData() || "", navigation: request.isNavigationRequest() };
    records.push(record);
    record.headers = request.headers();
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/v1/security/probe") {
      pending.push(request.allHeaders().then((headers) => { record.headers = headers; },
        () => { record.headers = request.headers(); }));
    }
  });
  return { records, settle: () => Promise.all(pending) };
}

function assertProtectedTokenTransport(requested) {
  const writes = requested.filter((request) => request.method === "POST" &&
    new URL(request.url).pathname === "/api/v1/security/probe");
  expect(writes).toHaveLength(1);
  const request = writes[0];
  expect(request.url).not.toContain(token);
  expect(request.body).not.toContain(token);
  expect(request.headers.authorization).toBe(`Bearer ${token}`);
  const remainingHeaders = { ...request.headers };
  delete remainingHeaders.authorization;
  expect(JSON.stringify(remainingHeaders)).not.toContain(token);
}
