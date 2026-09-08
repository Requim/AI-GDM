(function () {
  "use strict";

  const URL = "https://backup.opentopomap.org/{z}/{x}/{y}.png";
  const ATTRIBUTION = 'Map data: &copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>' +
    ' contributors, SRTM | &copy; <a href="https://opentopomap.org">OpenTopoMap</a>' +
    ' (<a href="https://creativecommons.org/licenses/by-sa/3.0/">CC-BY-SA</a>)';

  // 仅加载当前视口；备用服务原生最高 15 级，更高缩放不请求不存在的瓦片。
  function attach(map, status) {
    const tiles = window.L.tileLayer(URL, {
      minNativeZoom: 1, maxNativeZoom: 15, maxZoom: 18,
      detectRetina: false, updateWhenIdle: true, keepBuffer: 1,
      attribution: ATTRIBUTION
    });
    const states = new Map();
    let timer = 0;
    let timedOut = false;
    function updateStatus() {
      const values = Array.from(states.values());
      status.hidden = !values.includes("failed") && !timedOut;
      if (values.length > 0 && values.every(value => value === "loaded")) {
        window.clearTimeout(timer);
        timedOut = false;
        status.hidden = true;
      }
    }
    function startTimer() {
      window.clearTimeout(timer);
      timer = window.setTimeout(function () {
        timedOut = true;
        updateStatus();
      }, 12000);
    }
    tiles.on("loading", startTimer);
    tiles.on("tileloadstart", function (event) { states.set(event.tile, "pending"); });
    tiles.on("tileload", function (event) { states.set(event.tile, "loaded"); updateStatus(); });
    tiles.on("tileerror", function (event) { states.set(event.tile, "failed"); updateStatus(); });
    tiles.on("tileunload", function (event) { states.delete(event.tile); updateStatus(); });
    tiles.on("load", updateStatus);
    map.on("unload", function () { window.clearTimeout(timer); states.clear(); });
    retryFailedTiles(map, tiles, states);
    tiles.addTo(map);
    return tiles;
  }

  function retryFailedTiles(map, tiles, states) {
    const attempts = new WeakMap();
    const timers = new Map();
    function cancel(tile) {
      window.clearTimeout(timers.get(tile));
      timers.delete(tile);
    }
    tiles.on("tileerror", function (event) {
      const tile = event.tile;
      const count = attempts.get(tile) || 0;
      cancel(tile);
      if (count >= 2) return;
      attempts.set(tile, count + 1);
      timers.set(tile, window.setTimeout(function () {
        timers.delete(tile);
        if (states.has(tile)) tile.src = tile.src;
      }, 1000 * (count + 1)));
    });
    tiles.on("tileload", function (event) { cancel(event.tile); });
    tiles.on("tileunload", function (event) { cancel(event.tile); });
    map.on("unload", function () {
      timers.forEach(function (timer) { window.clearTimeout(timer); });
      timers.clear();
    });
  }

  window.AIGDMBasemap = Object.freeze({ attach: attach });
}());
