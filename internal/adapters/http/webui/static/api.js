(function () {
  "use strict";

  const DEFAULT_TIMEOUT_MS = 20000;
  const DEFAULT_MAX_RESPONSE_BYTES = 8 * 1024 * 1024;
  const ABSOLUTE_MAX_RESPONSE_BYTES = 8 * 1024 * 1024;
  const CSRF_HEADER_VALUE = "ai-gdm-browser-v1";
  let adminToken = "";
  let adminAuthorizationRevision = 0;
  let adminStatus = null;

  class APIError extends Error {
    constructor(message, status, code, requestId) {
      super(message);
      this.name = "APIError";
      this.status = status || 0;
      this.code = code || "request_failed";
      this.requestId = requestId || "";
    }
  }

  async function requestJSON(endpoint, options) {
    const settings = normalizeSettings(options);
    const authorization = authorizationSnapshot(settings.method);
    const controller = new AbortController();
    const timer = window.setTimeout(function () { controller.abort(); }, settings.timeoutMs);
    try {
      const response = await fetch(sameOriginEndpoint(endpoint),
        requestOptions(settings, controller.signal, authorization.token));
      if (response.status === 401) clearRejectedAuthorization(authorization);
      const payload = await parsePayload(response, settings.maxResponseBytes);
      if (!response.ok) {
        throw responseError(response.status, payload);
      }
      if (!payload || typeof payload !== "object") {
        throw new APIError("接口返回的数据结构无效", response.status, "invalid_response", "");
      }
      if (settings.includeResponseMetadata) {
        return { payload: payload, status: response.status, location: response.headers.get("Location") || "" };
      }
      return payload;
    } catch (error) {
      if (error && error.name === "AbortError") {
        throw new APIError("请求处理超时", 0, "request_timeout", "");
      }
      throw error;
    } finally {
      window.clearTimeout(timer);
    }
  }

  function normalizeSettings(options) {
    const settings = Object.assign({ method: "GET", timeoutMs: DEFAULT_TIMEOUT_MS,
      maxResponseBytes: DEFAULT_MAX_RESPONSE_BYTES }, options || {});
    settings.method = String(settings.method || "GET").toUpperCase();
    if (!Number.isSafeInteger(settings.timeoutMs) || settings.timeoutMs < 25 || settings.timeoutMs > 60000) {
      throw new APIError("请求超时配置无效", 0, "invalid_client_limit", "");
    }
    if (!Number.isSafeInteger(settings.maxResponseBytes) || settings.maxResponseBytes <= 0 ||
      settings.maxResponseBytes > ABSOLUTE_MAX_RESPONSE_BYTES) {
      throw new APIError("响应字节上限配置无效", 0, "invalid_client_limit", "");
    }
    if (typeof settings.includeResponseMetadata !== "boolean" && settings.includeResponseMetadata !== undefined) {
      throw new APIError("响应元数据配置无效", 0, "invalid_client_limit", "");
    }
    return settings;
  }

  function requestOptions(settings, signal, authorizationToken) {
    const headers = Object.assign({ Accept: "application/json" }, settings.headers || {});
    const result = { method: settings.method, headers: headers, cache: "no-store", credentials: "omit",
      referrerPolicy: "no-referrer", signal: signal };
    if (isUnsafeMethod(settings.method)) {
      headers["Content-Type"] = "application/json";
      headers["X-CSRF-Token"] = CSRF_HEADER_VALUE;
      if (authorizationToken) headers.Authorization = "Bearer " + authorizationToken;
    }
    if (settings.body !== undefined) {
      headers["Content-Type"] = "application/json";
      result.body = JSON.stringify(settings.body);
    }
    return result;
  }

  function sameOriginEndpoint(endpoint) {
    let value;
    try { value = new URL(endpoint, window.location.href); } catch (_) {
      throw new APIError("接口地址无效", 0, "invalid_endpoint", "");
    }
    if (value.origin !== window.location.origin || value.username || value.password) {
      throw new APIError("接口地址不允许跨站访问", 0, "cross_origin_endpoint", "");
    }
    return value.href;
  }

  function isUnsafeMethod(method) {
    return method !== "GET" && method !== "HEAD" && method !== "OPTIONS";
  }

  function authorizationSnapshot(method) {
    return { token: isUnsafeMethod(method) ? adminToken : "", revision: adminAuthorizationRevision };
  }

  function setAdminAuthorization(value) {
    const token = String(value || "");
    if (!validAdminToken(token)) {
      clearAdminAuthorization("管理员令牌必须是 32 至 256 位可见 ASCII 字符");
      return false;
    }
    adminToken = token;
    adminAuthorizationRevision += 1;
    renderAdminStatus("已授权，仅保留在当前页面内存", "authorized");
    return true;
  }

  function clearAdminAuthorization(message) {
    adminToken = "";
    adminAuthorizationRevision += 1;
    renderAdminStatus(message || "未授权", "unauthorized");
  }

  function clearRejectedAuthorization(authorization) {
    if (authorization.revision !== adminAuthorizationRevision) return;
    if (!authorization.token && !adminToken) {
      renderAdminStatus("授权无效，请重新输入管理员令牌", "unauthorized");
      return;
    }
    if (authorization.token !== adminToken) return;
    clearAdminAuthorization("授权无效，请重新输入管理员令牌");
  }

  function hasAdminAuthorization() { return adminToken !== ""; }

  function validAdminToken(value) {
    if (value.length < 32 || value.length > 256) return false;
    for (let index = 0; index < value.length; index += 1) {
      const code = value.charCodeAt(index);
      if (code < 0x21 || code > 0x7e) return false;
    }
    return true;
  }

  function renderAdminStatus(message, state) {
    if (!adminStatus) return;
    adminStatus.textContent = message;
    adminStatus.dataset.state = state;
  }

  function bindAdminAuthorization() {
    const group = document.getElementById("admin-auth-form");
    const input = document.getElementById("admin-token");
    const submitButton = document.getElementById("admin-auth-submit");
    const clearButton = document.getElementById("admin-auth-clear");
    adminStatus = document.getElementById("admin-auth-status");
    if (!group || !input || !submitButton || !clearButton || !adminStatus) return;
    input.value = "";
    clearAdminAuthorization();
    function authorize() {
      const value = input.value;
      input.value = "";
      setAdminAuthorization(value);
    }
    submitButton.addEventListener("click", authorize);
    input.addEventListener("keydown", function (event) {
      if (event.key !== "Enter") return;
      event.preventDefault();
      authorize();
    });
    clearButton.addEventListener("click", function () {
      input.value = "";
      clearAdminAuthorization();
    });
    input.disabled = false;
    submitButton.disabled = false;
    clearButton.disabled = false;
  }

  async function parsePayload(response, maxBytes) {
    const text = await readBoundedText(response, maxBytes);
    if (!text) return null;
    try { return JSON.parse(text); } catch (_) { return null; }
  }

  async function readBoundedText(response, maxBytes) {
    const length = Number(response.headers.get("Content-Length"));
    if (Number.isFinite(length) && length > maxBytes) throw responseTooLarge(maxBytes);
    if (!response.body || !response.body.getReader) {
      const text = await response.text();
      if (new TextEncoder().encode(text).byteLength > maxBytes) throw responseTooLarge(maxBytes);
      return text;
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let total = 0;
    let text = "";
    try {
      while (true) {
        const part = await reader.read();
        if (part.done) break;
        total += part.value.byteLength;
        if (total > maxBytes) throw responseTooLarge(maxBytes);
        text += decoder.decode(part.value, { stream: true });
      }
      return text + decoder.decode();
    } catch (error) {
      try { await reader.cancel(); } catch (_) { /* 响应可能已经被中止。 */ }
      throw error;
    }
  }

  function responseTooLarge(maxBytes) {
    return new APIError("接口响应超过浏览器安全上限（" + maxBytes + " 字节）", 0, "response_too_large", "");
  }

  function responseError(status, payload) {
    const value = payload && payload.error;
    if (value && value.message) {
      return new APIError(value.message, status, value.code, value.requestId);
    }
    const defaults = {
      400: "请求参数无效", 401: "需要管理员授权", 403: "请求来源未获授权",
      404: "请求的数据或能力尚不可用", 429: "请求过于频繁", 503: "供应商或实时数据暂时不可用"
    };
    return new APIError(defaults[status] || "接口请求失败（HTTP " + status + "）", status, "http_error", "");
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", bindAdminAuthorization, { once: true });
  } else {
    bindAdminAuthorization();
  }

  window.AIGDM = Object.assign(window.AIGDM || {}, {
    APIError: APIError,
    requestJSON: requestJSON,
    setAdminAuthorization: setAdminAuthorization,
    clearAdminAuthorization: clearAdminAuthorization,
    hasAdminAuthorization: hasAdminAuthorization
  });
})();
