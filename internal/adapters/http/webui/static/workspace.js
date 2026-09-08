(function () {
  "use strict";

  const labels = { "risk-map": "风险地图", evacuation: "疏散规划", assessment: "智能评估", sources: "数据源" };
  const panels = Array.from(document.querySelectorAll("[data-workspace]"));
  const navigation = document.getElementById("workspace-navigation");
  const toggle = document.getElementById("navigation-toggle");
  const dialog = document.getElementById("admin-auth-dialog");
  const authOpen = document.getElementById("admin-auth-open");
  if (!navigation || !dialog) return;

  document.documentElement.classList.add("workspace-ready");
  window.addEventListener("hashchange", function () { activateWorkspace(true); });
  toggle.addEventListener("click", function () {
    const expanded = toggle.getAttribute("aria-expanded") !== "true";
    toggle.setAttribute("aria-expanded", String(expanded));
    toggle.setAttribute("aria-label", expanded ? "收起导航" : "展开导航");
    navigation.classList.toggle("navigation-open", expanded);
  });
  navigation.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") return;
    navigation.classList.remove("navigation-open");
    toggle.setAttribute("aria-expanded", "false");
    toggle.setAttribute("aria-label", "展开导航");
    toggle.focus();
  });
  bindAuthorization();
  sortSources();
  activateWorkspace(false);

  function activateWorkspace(moveFocus) {
    const hash = window.location.hash.slice(1);
    const target = document.getElementById(hash);
    const parent = target && target.closest("[data-workspace]");
    const key = labels[hash] ? hash : parent ? parent.dataset.workspace : "risk-map";
    panels.forEach(function (panel) { panel.hidden = panel.dataset.workspace !== key; });
    document.querySelectorAll("[data-workspace-link]").forEach(function (link) {
      const active = link.dataset.workspaceLink === key;
      link.classList.toggle("active", active);
      if (active) link.setAttribute("aria-current", "page");
      else link.removeAttribute("aria-current");
    });
    document.getElementById("workspace-title").textContent = labels[key];
    document.title = labels[key] + " | AI-GDM 地质灾害辅助研判";
    navigation.classList.remove("navigation-open");
    toggle.setAttribute("aria-expanded", "false");
    toggle.setAttribute("aria-label", "展开导航");
    if (moveFocus) document.getElementById("workspace-content").focus({ preventScroll: true });
    window.scrollTo(0, 0);
    requestAnimationFrame(function () {
      document.dispatchEvent(new CustomEvent("ai-gdm:workspace-visible", { detail: { workspace: key } }));
    });
  }

  function bindAuthorization() {
    authOpen.addEventListener("click", function () {
      dialog.showModal();
      document.getElementById("admin-token").focus();
    });
    document.getElementById("admin-auth-close").addEventListener("click", function () { dialog.close(); });
    dialog.addEventListener("close", function () {
      document.getElementById("admin-token").value = "";
      authOpen.focus();
    });
    const status = document.getElementById("admin-auth-status");
    const observer = new MutationObserver(function () {
      const header = document.getElementById("header-auth-status");
      header.textContent = status.dataset.state === "authorized" ? "已授权" : "未授权";
      header.dataset.state = status.dataset.state || "";
    });
    observer.observe(status, { childList: true, attributes: true, attributeFilter: ["data-state"] });
  }

  function sortSources() {
    const rows = Array.from(document.querySelectorAll(".source-row[data-state]"));
    const priority = { unavailable: 0, stale: 1, degraded: 2, unknown: 3, waiting: 4, configured: 5, disabled: 6, available: 7 };
    rows.sort(function (left, right) { return (priority[left.dataset.state] ?? 3) - (priority[right.dataset.state] ?? 3); });
    rows.forEach(function (row) { row.parentElement.appendChild(row); });
  }
})();
