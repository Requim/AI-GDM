(function () {
  "use strict";

  const DEFAULT_TIMEOUT_MS = 20000;
  const DEFAULT_MAX_RESPONSE_BYTES = 8 * 1024 * 1024;
  const ABSOLUTE_MAX_RESPONSE_BYTES = 8 * 1024 * 1024;

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
    const controller = new AbortController();
    const timer = window.setTimeout(function () { controller.abort(); }, settings.timeoutMs);
    try {
      const response = await fetch(endpoint, requestOptions(settings, controller.signal));
      const payload = await parsePayload(response, settings.maxResponseBytes);
      if (!response.ok) throw responseError(response.status, payload);
      if (!payload || typeof payload !== "object") {
        throw new APIError("接口返回的数据结构无效", response.status, "invalid_response", "");
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
    if (!Number.isSafeInteger(settings.timeoutMs) || settings.timeoutMs < 25 || settings.timeoutMs > 60000) {
      throw new APIError("请求超时配置无效", 0, "invalid_client_limit", "");
    }
    if (!Number.isSafeInteger(settings.maxResponseBytes) || settings.maxResponseBytes <= 0 ||
      settings.maxResponseBytes > ABSOLUTE_MAX_RESPONSE_BYTES) {
      throw new APIError("响应字节上限配置无效", 0, "invalid_client_limit", "");
    }
    return settings;
  }

  function requestOptions(settings, signal) {
    const headers = Object.assign({ Accept: "application/json" }, settings.headers || {});
    const result = { method: settings.method, headers: headers, cache: "no-store", signal: signal };
    if (settings.body !== undefined) {
      headers["Content-Type"] = "application/json";
      result.body = JSON.stringify(settings.body);
    }
    return result;
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

  window.AIGDM = Object.assign(window.AIGDM || {}, { APIError: APIError, requestJSON: requestJSON });
})();
