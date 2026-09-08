import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import test from "node:test";

const source = readFileSync(new URL("../../internal/adapters/http/webui/static/basemap.js", import.meta.url), "utf8");

function fixture() {
  const events = {};
  const status = { hidden: true };
  let deadline;
  let options;
  let address;
  const layer = {
    on(name, callback) {
      const previous = events[name];
      events[name] = event => { if (previous) previous(event); callback(event); };
    },
    addTo() { return this; }
  };
  const window = {
    L: { tileLayer(url, config) { address = url; options = config; return layer; } },
    setTimeout(callback) { deadline = callback; return 1; },
    clearTimeout() { deadline = undefined; }
  };
  runInNewContext(source, { window, Map, WeakMap });
  window.AIGDMBasemap.attach({ on(name, callback) { events[name] = callback; } }, status);
  return { events, status, options, address, expire() { deadline(); } };
}

test("固定可达来源、署名和原生缩放上限，不请求未提供的高清瓦片", () => {
  const value = fixture();
  assert.equal(value.address, "https://backup.opentopomap.org/{z}/{x}/{y}.png");
  assert.equal(value.options.maxNativeZoom, 15);
  assert.equal(value.options.detectRetina, false);
  assert.match(value.options.attribution, /OpenStreetMap.*SRTM.*OpenTopoMap.*CC-BY-SA/);
});

test("部分失败后单张成功不能错误清除提示，离开失败瓦片范围后恢复", () => {
  const { events, status } = fixture();
  const bad = {}, good = {};
  events.tileloadstart({ tile: bad });
  events.tileloadstart({ tile: good });
  events.tileerror({ tile: bad });
  events.tileload({ tile: good });
  assert.equal(status.hidden, false);
  events.tileunload({ tile: bad });
  assert.equal(status.hidden, true);
});

test("慢请求到期显示降级，全部加载成功后恢复", () => {
  const value = fixture();
  const tile = {};
  value.events.loading();
  value.events.tileloadstart({ tile });
  value.expire();
  assert.equal(value.status.hidden, false);
  value.events.tileload({ tile });
  assert.equal(value.status.hidden, true);
});

test("后续缩放重新启动监测，不沿用首次成功状态", () => {
  const value = fixture();
  value.events.tileload({ tile: {} });
  value.events.loading();
  value.events.tileloadstart({ tile: {} });
  value.expire();
  assert.equal(value.status.hidden, false);
});

test("失败瓦片最多重试两次，成功和移出视口会取消重试", () => {
  const value = fixture();
  let retries = 0;
  const tile = { get src() { return "tile.png"; }, set src(url) { retries++; } };
  value.events.tileloadstart({ tile });
  value.events.tileerror({ tile });
  value.expire();
  value.events.tileerror({ tile });
  value.expire();
  value.events.tileerror({ tile });
  assert.throws(() => value.expire());
  assert.equal(retries, 2);
  assert.equal(value.status.hidden, false);
  value.events.tileload({ tile });
  assert.equal(value.status.hidden, true);
  const other = {};
  value.events.tileloadstart({ tile: other });
  value.events.tileerror({ tile: other });
  value.events.tileunload({ tile: other });
  assert.throws(() => value.expire());
});
