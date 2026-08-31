import { expect, test } from "@playwright/test";

const LOSS_PATH = "/api/v1/loss/assessments";
const AI_PATH = "/api/v1/ai/report";
const LOSS_SNAPSHOT_ID = "snapshot-e2e-20260828";
const SURVIVAL_CASE_ID = "case-oso-2014";
const SURVIVAL_ASSESSMENT_ID = "sha256:830da326807c37d810886e4eeeed303aca4c8216ce839dd25f904b872b05550f";
const FIXED_NOW = new Date("2026-08-28T00:00:00Z");
const TILE = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M/wHwAF/gL+Xc7pAAAAAElFTkSuQmCC", "base64");

test.beforeEach(async ({ page }) => {
  await page.route(/^https:\/\/[a-z]\.tile\.openstreetmap\.org\//, async (route) => {
    await route.fulfill({ status: 200, contentType: "image/png", body: TILE });
  });
});

test("评估区解释当前灾损、历史回放与 AI 解读的用途和边界", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);

  await expect(page.locator("#assessment-title")).toHaveText("灾损估算、历史案例与 AI 解读");
  await expect(page.locator(".assessment-purpose-grid")).toContainText("估算条件直接损失");
  await expect(page.locator(".assessment-purpose-grid")).toContainText("检查规则如何评分");
  await expect(page.locator(".assessment-purpose-grid")).toContainText("解释前两类结果");
  await expect(page.locator("#assessment-panel-loss")).toContainText("不是风险等级");
  await expect(page.locator("#assessment-panel-loss")).toContainText("不是伤亡人数");
  await expect(page.locator("#assessment-panel-loss")).toContainText("不是概率加权的期望损失");

  await selectTab(page, "survival");
  await expect(page.locator("#assessment-panel-survival")).toContainText("不是临床生还预测");
  await expect(page.locator("#assessment-panel-survival")).toContainText("各类输入按不同权重加减分");
  await selectTab(page, "ai");
  await expect(page.locator("#assessment-panel-ai")).toContainText("条件灾损估算");
  await expect(page.locator("#assessment-panel-ai")).toContainText("历史案例回放");
  await expect(page.locator("#assessment-panel-ai")).toContainText("不会重新计算");
  await expect(page.locator("#assessment-panel-ai")).toContainText("AI 文字可能出错或与固定结果冲突");
});

test("风险地图不可用且快照输入为空时损失按钮禁用且不得发送 POST", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);

  await expect(page.locator("#loss-snapshot-id")).toHaveValue("");
  await expect(page.locator("#loss-assessment-run")).toBeDisabled();
  await page.locator("#loss-snapshot-id").press("Enter");
  await expect(page.locator("#loss-assessment-status")).toContainText("请等待风险地图加载有效数据");
  await expectFixtureCall(request, "loss_post", 0);
});

test("损失评估仅提交 snapshotId，并跟随 Location 与 sources 审计", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);
  const pending = page.waitForRequest((value) => value.url().endsWith(LOSS_PATH));

  await submitLoss(page);

  expect((await pending).postDataJSON()).toEqual({ snapshotId: LOSS_SNAPSHOT_ID });
  await expect(page.locator("#loss-assessment-status")).toHaveClass(/assessment-state-current/);
  const assessmentID = await currentLossAssessmentID(page);
  await expect(page.locator("#loss-low-amount")).toContainText("30.00");
  await expect(page.locator("#loss-central-amount")).toContainText("60.00");
  await expect(page.locator("#loss-high-amount")).toContainText("90.00");
  await expect(page.locator("#loss-population")).toContainText("50");
  await expect(page.locator("#loss-road-length")).toContainText("10");
  await expect(page.locator("#loss-facilities")).toContainText("2");
  await expect(page.locator("#loss-population")).toContainText("来自当前风险分析");
  await expect(page.locator("#loss-road-length")).toContainText("国家级");
  await expect(page.locator("#loss-facilities")).toContainText("国家级");
  await expect(page.locator("#loss-source-list")).toContainText("spatial-analysis-e2e-v1");
  await expect(page.locator("#loss-source-list")).toContainText("暴露投影 exposure-");
  await expect(page.locator("#loss-source-list")).toContainText("ai-gdm-loss-risk-projection-v1");
  await expect(page.locator("#loss-source-list")).toContainText("行政边界 CHN-ADM0-2026");
  await expect(page.locator("#loss-source-list")).toContainText("摘要 eeeeeeeeeeee...");
  await expect(page.locator("#loss-source-list")).not.toContainText("secret");
  await expectLossSourceGroup(page, "来源审计引用", "/spatial/input");
  await expectLossSourceGroup(page, "空间分析输入", "/spatial/input");
  await expectLossSourceGroup(page, "空间分析数据集", "/spatial/dataset");
  await expectLossSourceGroup(page, "人口暴露来源", "/population/shared");
  await expectLossSourceGroup(page, "道路暴露来源", "/road/shared");
  await expectLossSourceGroup(page, "设施暴露来源", "/facility/shared");
  const sourceHrefs = await page.locator("#loss-source-list a").evaluateAll((links) => links.map((link) => link.href));
  expect(sourceHrefs.length).toBeGreaterThan(0);
  expect(sourceHrefs.every((href) => href.startsWith("https://") && !href.includes("@") &&
    !/[#]|password=|passwd=|session=|x-amz-|credential=|token=/i.test(href))).toBeTruthy();
  const projection = await fetchLossProjectionInPage(page, assessmentID);
  expect(Object.keys(projection).sort()).toEqual([
    "adminBoundaryDigest", "adminBoundaryId", "calculatedAt", "datasetReferences", "digest", "id",
    "inputReferences", "projectionCollectedAt", "projectionDigest", "projectionId", "projectionValidFrom",
    "projectionLimitations", "projectionValidTo", "projectionVersion", "regionCode", "sourceReferenceDigests", "status",
    "totalAreaSquareMeters", "version"
  ].sort());
  expect(projection.projectionId).toMatch(/^exposure-[0-9a-f]{64}$/);
  expect(projection.projectionVersion).toBe("ai-gdm-loss-risk-projection-v1");
  expect(projection.projectionDigest).toMatch(/^[0-9a-f]{64}$/);
  expect(projection.regionCode).toBe("CN");
  expect(projection.adminBoundaryId).toBe("CHN-ADM0-2026");
  expect(projection.adminBoundaryDigest).toMatch(/^[0-9a-f]{64}$/);
  expect(Date.parse(projection.projectionValidFrom)).toBeLessThanOrEqual(Date.parse(projection.projectionCollectedAt));
  expect(Date.parse(projection.projectionValidTo)).toBeGreaterThan(Date.parse(FIXED_NOW));
  expect(projection.projectionLimitations).toEqual([]);
  expect(projection.sourceReferenceDigests.length).toBeGreaterThan(0);
  expect(projection.sourceReferenceDigests.every((value) => /^[0-9a-f]{64}$/.test(value))).toBeTruthy();
  const audit = await fetchLossAuditInPage(page, assessmentID);
  expect(Object.keys(audit).sort()).toEqual([
    "adminBoundaryDigest", "adminBoundaryId", "analysisDigest", "analysisId", "analysisVersion", "assessmentId",
    "calculatedAt", "evidence", "formulaVersion", "inputDigest", "inputReferenceCount", "inputReferences",
    "limitations", "projectionCollectedAt", "projectionDigest", "projectionId", "projectionValidFrom",
    "projectionLimitations", "projectionValidTo", "projectionVersion", "scope", "snapshotId",
    "sourceReferenceDigests", "status"
  ].sort());
  expect(audit.assessmentId).toBe(assessmentID);
  expect(audit.projectionId).toBe(projection.projectionId);
  expect(audit.projectionVersion).toBe(projection.projectionVersion);
  expect(audit.projectionDigest).toBe(projection.projectionDigest);
  expect(audit.projectionCollectedAt).toBe(projection.projectionCollectedAt);
  expect(audit.projectionValidFrom).toBe(projection.projectionValidFrom);
  expect(audit.projectionValidTo).toBe(projection.projectionValidTo);
  expect(audit.projectionLimitations).toEqual(projection.projectionLimitations);
  expect(audit.sourceReferenceDigests).toEqual(projection.sourceReferenceDigests);
  expect(audit.adminBoundaryId).toBe(projection.adminBoundaryId);
  expect(audit.adminBoundaryDigest).toBe(projection.adminBoundaryDigest);
  await expect(page.locator(`#ai-analysis-reference option[value="loss_assessment:${assessmentID}"]`)).toHaveCount(1);
  await expectFixtureCall(request, "loss_get", 2);
  await expectFixtureCall(request, "loss_sources", 2);
});

test("局部热点研究参考区间使用琥珀状态并严格绑定道路案例基线", async ({ page, request }) => {
  await setScenario(request, "loss_reference_only");
  await openAssessment(page);

  await submitLoss(page);

  await expect(page.locator("#loss-assessment-status")).toHaveClass(/assessment-state-warning/);
  await expect(page.locator("#loss-assessment-status")).not.toHaveClass(/assessment-state-current/);
  await expect(page.locator("#loss-assessment-status")).toContainText("局部热点研究参考区间");
  await expect(page.locator("#loss-result-state")).toHaveText("局部热点研究参考区间 · 输入质量：较低");
  await expect(page.locator("#loss-low-amount")).toContainText("3,064.60");
  await expect(page.locator("#loss-central-amount")).toContainText("3,643.60");
  await expect(page.locator("#loss-high-amount")).toContainText("4,222.60");
  await expect(page.locator("#loss-road-length")).toContainText("研究案例参考基线");
  await expect(page.locator("#loss-population")).toContainText("局部热点暴露背景");
  await expect(page.locator("#loss-facilities")).toContainText("局部热点暴露背景");
  await expect(page.locator("#loss-source-list")).toContainText("研究案例参考基线");
  for (const limitation of [
    "本次金额为局部热点研究参考区间，不代表全国或法定灾损",
    "研究参考金额仅计算道路；人口和设施仅作暴露背景，未货币化",
    "道路条件损失参数来自西藏吉隆藏布流域案例并按历史欧元汇率换算，跨区域外推不确定性高",
    "结果用于辅助研判，不替代法定灾损核定"
  ]) await expect(page.locator("#loss-limitation-list")).toContainText(limitation);
  const assessmentID = await currentLossAssessmentID(page);
  const result = await fetchLossAssessmentInPage(page, assessmentID);
  expect(result.status).toBe("reference_only");
  expect(result.confidence).toBe(0.49);
  expect(result.confidenceBand).toBe("low");
  expect(result.limitations).not.toContain(
    "风险与暴露投影已过期，当前仅使用最近 72 小时内最后一次成功数据生成研究参考区间，不代表实时情况"
  );
  expect(result.includedAssets).toEqual(["road"]);
  expect(result.metrics.affectedRoads.baselineLevel).toBe("reference_case");
  expect(result.metrics.conditionalDirectLoss.baselineLevel).toBe("reference_case");
  for (const name of ["impactArea", "affectedPopulation", "affectedFacilities"]) {
    expect(result.metrics[name].baselineLevel).toBe("not_applicable");
  }
  expect(Object.values(result.metrics).every((metric) => metric.status === "reference_only")).toBeTruthy();
  for (const baseline of result.evidence.costBaselines.concat(result.evidence.vulnerabilities)) {
    expect(baseline.status).toBe("demo_only");
    expect(baseline.baselineLevel).toBe("reference_case");
    expect(baseline).not.toHaveProperty("approvedBy");
  }
});

test("最近72小时最后成功投影生成很低置信度研究参考且明确非实时", async ({ page, request }) => {
  await setScenario(request, "loss_reference_stale_projection");
  await openAssessment(page);

  await submitLoss(page);

  const staleLimitation =
    "风险与暴露投影已过期，当前仅使用最近 72 小时内最后一次成功数据生成研究参考区间，不代表实时情况";
  await expect(page.locator("#loss-assessment-status")).toHaveClass(/assessment-state-warning/);
  await expect(page.locator("#loss-assessment-status")).not.toHaveClass(/assessment-state-current/);
  await expect(page.locator("#loss-assessment-status")).toContainText("最后成功数据研究参考区间");
  await expect(page.locator("#loss-result-state")).toHaveText("最后成功数据研究参考区间 · 输入质量：很低");
  await expect(page.locator("#loss-limitation-list")).toContainText(staleLimitation);
  await expect(page.locator("#loss-central-amount")).not.toHaveText("--");
  const assessmentID = await currentLossAssessmentID(page);
  const result = await fetchLossAssessmentInPage(page, assessmentID);
  expect(result.status).toBe("reference_only");
  expect(result.confidence).toBe(0.24);
  expect(result.confidenceBand).toBe("very_low");
  expect(result.limitations).toContain(staleLimitation);
  await fetchLossAuditInPage(page, assessmentID);
  await expect(page.locator(`#ai-analysis-reference option[value="loss_assessment:${assessmentID}"]`))
    .toContainText("最后成功数据研究参考区间");
});

test("最后成功投影高置信度篡改时浏览器 fail-closed", async ({ page, request }) => {
  await setScenario(request, "loss_reference_bad_stale_confidence");
  await openAssessment(page);

  await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
  await page.locator("#loss-assessment-run").click();

  await expectLossFailClosed(page, "最后成功数据降级状态或置信度无效");
  await expect(page.locator(`#ai-analysis-reference option[value^="loss_assessment:"]`)).toHaveCount(0);
});

for (const [scenario, message] of [
  ["loss_reference_bad_cost_status", "成本基线条目无效"],
  ["loss_reference_bad_vulnerability_level", "脆弱性基线条目无效"]
]) {
  test(`${scenario} 时研究参考基线篡改 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);

    await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page, message);
    await expectFixtureCall(request, "loss_get", 0);
  });
}

test("供应商省略限制贯穿投影证据、置信度和可见限制", async ({ page, request }) => {
  await setScenario(request, "loss_projection_limitation");
  await openAssessment(page);

  await submitLoss(page);

  const limitation = "跳过非闭合设施 way 42，设施数量可能低估";
  await expect(page.locator("#loss-result-state")).toHaveText("可计算 · 输入质量：中等");
  await expect(page.locator("#loss-limitation-list")).toContainText(limitation);
  const assessmentID = await currentLossAssessmentID(page);
  const projection = await fetchLossProjectionInPage(page, assessmentID);
  const audit = await fetchLossAuditInPage(page, assessmentID);
  expect(projection.projectionLimitations).toEqual([limitation]);
  expect(audit.projectionLimitations).toEqual([limitation]);
});

for (const [scenario, message] of [
  ["loss_projection_limitations_missing", "空间分析证据无效"],
  ["loss_audit_projection_limitations_mismatch", "损失来源审计投影身份不一致"]
]) {
  test(`${scenario} 时投影限制 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);

    await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page, message);
  });
}

test("超过 JavaScript 安全整数的金额保持十进制精度", async ({ page, request }) => {
  await setScenario(request, "loss_big_integer");
  await openAssessment(page);

  await submitLoss(page);

  await expect(page.locator("#loss-central-amount")).toContainText("90,071,992,547,409.93");
  await expect(page.locator("#loss-central-amount")).not.toContainText("90,071,992,547,409.92");
});

test("全国投影使用的国家级基线在每个损失分项中显式展示", async ({ page, request }) => {
  await setScenario(request, "loss_national_baseline");
  await openAssessment(page);

  await submitLoss(page);

  await expect(page.locator("#loss-population")).toContainText("来自当前风险分析");
  for (const selector of ["#loss-road-length", "#loss-facilities"]) {
    await expect(page.locator(selector)).toContainText("国家级基线");
  }
});

test("超过 32 条损失限制仍保留领域强制免责声明并给出省略提示", async ({ page, request }) => {
  await setScenario(request, "loss_many_limitations");
  await openAssessment(page);

  await submitLoss(page);

  const limitations = page.locator("#loss-limitation-list li");
  await expect(limitations).toHaveCount(32);
  await expect(page.locator("#loss-limitation-list")).toContainText("仅估算道路和风险区内 POI 设施的直接物理损失");
  await expect(page.locator("#loss-limitation-list")).toContainText("结果用于辅助研判，不替代法定灾损核定");
  await expect(page.locator("#loss-limitation-list")).toContainText(/另有 \d+ 条限制未展开/);
});

test("损失评估成功后 503 清空金额、来源与 AI 引用", async ({ page, request }) => {
  await setScenario(request, "loss_success_then_503");
  await openAssessment(page);
  await submitLoss(page);
  await currentLossAssessmentID(page);

  await page.locator("#loss-assessment-run").click();

  await expectLossFailClosed(page, "损失评估依赖暂时不可用");
});

for (const [scenario, stage] of [["loss_get_503", "GET"], ["loss_sources_503", "sources"]]) {
  test(`保存链路 ${stage} 返回 503 时不展示部分损失结果`, async ({ page, request }) => {
    await setScenario(request, "success");
    await openAssessment(page);
    await submitLoss(page);
    await currentLossAssessmentID(page);
    await setScenario(request, scenario);

    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page, "损失评估依赖暂时不可用");
    await expect(page.locator("#loss-assessment-run")).toBeEnabled();
  });
}

test("损失评估超时后清空旧结果且按钮可重试", async ({ page, request }) => {
  await page.clock.install({ time: FIXED_NOW });
  await setScenario(request, "success");
  await openAssessment(page);
  await submitLoss(page);
  await setScenario(request, "loss_timeout");

  const pending = page.waitForRequest((value) => value.url().endsWith(LOSS_PATH));
  await page.locator("#loss-assessment-run").click();
  await pending;
  await page.clock.fastForward(30_100);

  await expectLossFailClosed(page, "请求处理超时");
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();
});

test("损失 GET 返回坏结构时清空旧值并拒绝建立引用", async ({ page, request }) => {
  await setScenario(request, "loss_bad_wire");
  await openAssessment(page);

  await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
  await page.locator("#loss-assessment-run").click();

  await expectLossFailClosed(page, "损失评估身份、状态或时间契约无效");
});

for (const [scenario, label] of [
  ["loss_included_assets_mismatch", "includedAssets 与 exposures 资产集合不一致"],
  ["loss_cost_unit_mismatch", "成本单位与暴露单位不一致"],
  ["loss_input_reference_mismatch", "顶层输入引用与证据来源集合不一致"]
]) {
  test(`${label} 时损失结果 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);

    await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page);
  });
}

for (const [scenario, label] of [
  ["loss_bad_time_order", "评估时间早于快照运行时间"],
  ["loss_snapshot_expired_at_assessment", "快照在评估时已经过期"],
  ["loss_spatial_after_assessment", "空间分析晚于评估"],
  ["loss_projection_collected_after_assessment", "暴露投影采集晚于评估"],
  ["loss_projection_expired_at_assessment", "暴露投影在评估时已经过期"],
  ["loss_projection_invalid_window", "暴露投影有效窗口倒置"],
  ["loss_admin_boundary_bad_digest", "行政边界摘要损坏"],
  ["loss_source_fetched_after_assessment", "风险来源抓取晚于评估"],
  ["loss_source_valid_from_after_assessment", "风险来源有效期开始晚于评估"],
  ["loss_source_observed_after_assessment", "风险来源观测时间晚于评估"],
  ["loss_source_published_after_assessment", "风险来源发布时间晚于评估"],
  ["loss_source_revision_seen_after_assessment", "风险来源修订首次发现时间晚于评估"],
  ["loss_cost_price_after_assessment", "成本价格基准日晚于评估"],
  ["loss_baseline_fetched_after_assessment", "成本基线抓取晚于评估"],
  ["loss_vulnerability_fetched_after_assessment", "脆弱性基线抓取晚于评估"]
]) {
  test(`${label}时损失结果 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);

    await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page);
  });
}

test("来源审计行政边界身份与评估证据不一致时 fail-closed", async ({ page, request }) => {
  await setScenario(request, "loss_audit_admin_boundary_mismatch");
  await openAssessment(page);

  await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
  await page.locator("#loss-assessment-run").click();

  await expectLossFailClosed(page, "损失来源审计投影身份不一致");
});

for (const [scenario, label] of [
  ["loss_cost_missing_semantic", "成本语义键缺失"],
  ["loss_cost_duplicate_semantic", "成本语义键重复"],
  ["loss_cost_extra_semantic", "成本语义键多余"],
  ["loss_vulnerability_missing_semantic", "脆弱性语义键缺失"],
  ["loss_vulnerability_duplicate_semantic", "脆弱性语义键重复"],
  ["loss_vulnerability_extra_semantic", "脆弱性语义键多余"]
]) {
  test(`${label} 时固定基线证据 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);

    await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page);
  });
}

for (const scenario of ["loss_private_source", "loss_localhost_source", "loss_ipv6_source",
  "loss_ipv4_mapped_source", "loss_local_source", "loss_internal_source"]) {
  test(`${scenario} HTTPS 来源仍按私网地址 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);

    await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page);
    await expect(page.locator("#loss-source-list a")).toHaveCount(0);
  });
}

for (const [scenario, message] of [
  ["loss_location_missing", "损失评估 Location 无效"],
  ["loss_location_wrong_id", "损失评估 Location 未绑定同源资源"],
  ["loss_location_query", "损失评估 Location 未绑定同源资源"],
  ["loss_location_hash", "损失评估 Location 未绑定同源资源"],
  ["loss_location_extra_path", "损失评估 Location 未绑定同源资源"],
  ["loss_location_encoded", "损失评估 Location 未绑定同源资源"],
  ["loss_location_cross_origin", "损失评估 Location 未绑定同源资源"]
]) {
  test(`${scenario} Location 不允许改变资源身份或来源`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);

    await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page, message);
    await expectFixtureCall(request, "loss_get", 0);
  });
}

for (const [scenario, label] of [
  ["loss_content_length_oversized", "POST Content-Length"],
  ["loss_chunked_oversized", "POST chunked"],
  ["loss_get_content_length_oversized", "GET Content-Length"],
  ["loss_get_chunked_oversized", "GET chunked"],
  ["loss_sources_content_length_oversized", "sources Content-Length"],
  ["loss_sources_chunked_oversized", "sources chunked"]
]) {
  test(`${label} 超过 1 MiB 时损失结果 fail-closed`, async ({ page, request }) => {
    await setScenario(request, "success");
    await openAssessment(page);
    await submitLoss(page);
    await currentLossAssessmentID(page);
    await setScenario(request, scenario);

    await page.locator("#loss-assessment-run").click();

    await expectLossFailClosed(page, "接口响应超过浏览器安全上限");
    await expect(page.locator("#loss-assessment-run")).toBeEnabled();
  });
}

test("修改 snapshotId 会使迟到损失响应失效并恢复按钮", async ({ page, request }) => {
  await setScenario(request, "loss_delayed");
  await openAssessment(page);
  await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
  const pending = page.waitForRequest((value) => value.url().endsWith(LOSS_PATH));

  await page.locator("#loss-assessment-run").click();
  await pending;
  await page.locator("#loss-snapshot-id").fill("snapshot-new-input");

  await expect(page.locator("#loss-assessment-status")).toContainText("旧损失评估已清除");
  await expect(page.locator("#loss-assessment-run")).toBeEnabled();
  await page.waitForTimeout(900);
  await expect(page.locator("#loss-assessment-id")).toHaveText("尚未生成估算");
  await expect(page.locator("#loss-low-amount")).toHaveText("--");
});

test("评估标签支持方向键、Home、End 与 roving tabindex", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);
  const loss = page.locator("#assessment-tab-loss");
  const survival = page.locator("#assessment-tab-survival");
  const ai = page.locator("#assessment-tab-ai");

  await loss.focus();
  await loss.press("ArrowRight");
  await expect(survival).toBeFocused();
  await expect(survival).toHaveAttribute("aria-selected", "true");
  await expect(survival).toHaveAttribute("tabindex", "0");
  await survival.press("End");
  await expect(ai).toBeFocused();
  await ai.press("Home");
  await expect(loss).toBeFocused();
  await loss.press("ArrowLeft");
  await expect(ai).toBeFocused();
});

test("历史案例详情区分未知伤情、恢复 ready 状态并展示安全 USGS 来源", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);
  await selectCase(page);

  await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-replay/);
  await expect(page.locator("#survival-assessment-status")).toContainText("已就绪");
  await expect(page.locator("#survival-case-summary")).toContainText("公开死亡数");
  await expect(page.locator("#survival-case-summary")).toContainText("43");
  await expect(page.locator("#survival-case-summary")).toContainText("未知 / 未按统一口径披露");
  const source = page.getByRole("link", { name: "查看 USGS HTTPS 来源" });
  await expect(source).toHaveAttribute("href", /^https:\/\/www\.usgs\.gov\//);
  await expect(source).toHaveAttribute("rel", "noopener noreferrer");
  await expect(page.locator("#survival-case-summary")).toContainText("USGS Five Years Later");
});

test("历史回放保留案例、评估、模型卡限制并使用非当前状态", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);
  await selectCase(page);

  await runReplay(page);

  await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-replay/);
  await expect(page.locator("#survival-assessment-status")).not.toHaveClass(/assessment-state-current/);
  await expect(page.locator("#survival-score")).toHaveText("35 / 100");
  await expect(page.locator("#survival-limitation-list")).toContainText("伤情未按统一口径披露");
  await expect(page.locator("#survival-limitation-list")).toContainText("规则未经过个体层面的临床校准");
  await expect(page.locator("#survival-limitation-list")).toContainText("不得用于实时人员评估");
  await expect(page.locator("#survival-limitation-list")).toContainText("不得用于实时人员评估或自动放弃搜救");
});

for (const [scenario, message] of [
  ["survival_model_card_503", "生还评估模型卡暂时不可用"],
  ["survival_model_card_missing_field", "生还评估模型卡契约无效"]
]) {
  test(`${scenario} 时模型卡 fail-closed 且禁止运行历史回放`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);
    await selectTab(page, "survival");

    await page.locator("#survival-case-select").selectOption(SURVIVAL_CASE_ID);

    await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
    await expect(page.locator("#survival-assessment-status")).toContainText(message);
    await expect(page.locator("#survival-assessment-run")).toBeDisabled();
    await expect(page.locator("#survival-score")).toHaveText("--");
  });
}

test("模型卡版本与真实回放结果不一致时清空结果并拒绝建立引用", async ({ page, request }) => {
  await setScenario(request, "survival_model_card_wrong_version");
  await openAssessment(page);
  await selectCase(page);

  await page.locator("#survival-assessment-run").click();

  await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
  await expect(page.locator("#survival-assessment-status")).toContainText("人工复核绑定");
  await expect(page.locator("#survival-score")).toHaveText("--");
  await expect(page.locator(`#ai-analysis-reference option[value="survival_assessment:${SURVIVAL_ASSESSMENT_ID}"]`)).toHaveCount(0);
});

for (const [scenario, message] of [
  ["survival_source_invalid", "历史案例来源契约无效"],
  ["survival_source_invalid_window", "历史案例来源有效期无效"],
  ["survival_scenario_completeness_mismatch", "合成匿名场景契约无效"]
]) {
  test(`${scenario} 时真实案例详情契约 fail-closed`, async ({ page, request }) => {
    await setScenario(request, "success");
    await openAssessment(page);
    await setScenario(request, scenario);
    await selectTab(page, "survival");

    await page.locator("#survival-case-select").selectOption(SURVIVAL_CASE_ID);

    await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
    await expect(page.locator("#survival-assessment-status")).toContainText(message);
    await expect(page.locator("#survival-case-summary")).not.toContainText("Oso");
    await expect(page.locator("#survival-assessment-run")).toBeDisabled();
  });
}

test("案例详情成功后 503 清空旧详情、因素和限制", async ({ page, request }) => {
  await setScenario(request, "survival_detail_success_then_503");
  await openAssessment(page);
  await selectCase(page);
  await page.locator("#survival-case-select").selectOption("");

  await page.locator("#survival-case-select").selectOption(SURVIVAL_CASE_ID);

  await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
  await expect(page.locator("#survival-case-summary")).not.toContainText("Oso");
  await expect(page.locator("#survival-factor-list")).toContainText("回放后会列出哪些输入让分数升高或降低");
  await expect(page.locator("#survival-limitation-list")).toContainText("选择案例后会展示这类结果不能如何使用");
});

test("历史回放成功后 503 清旧且可在供应商恢复后重试", async ({ page, request }) => {
  await setScenario(request, "survival_replay_success_then_503");
  await openAssessment(page);
  await selectCase(page);
  await runReplay(page);

  await page.locator("#survival-assessment-run").click();
  await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
  await expect(page.locator("#survival-score")).toHaveText("--");
  await expect(page.locator("#survival-assessment-run")).toBeEnabled();

  await setScenario(request, "success");
  await runReplay(page);
  await expect(page.locator("#survival-score")).toHaveText("35 / 100");
});

test("历史回放超时清空旧结果并允许再次点击", async ({ page, request }) => {
  await page.clock.install({ time: FIXED_NOW });
  await setScenario(request, "success");
  await openAssessment(page);
  await selectCase(page);
  await runReplay(page);
  await setScenario(request, "survival_replay_timeout");

  const pending = page.waitForRequest((value) => value.url().includes("/replays/cases/"));
  await page.locator("#survival-assessment-run").click();
  await pending;
  await page.clock.fastForward(30_100);

  await expect(page.locator("#survival-assessment-status")).toContainText("请求处理超时");
  await expect(page.locator("#survival-score")).toHaveText("--");
  await expect(page.locator("#survival-assessment-run")).toBeEnabled();
});

for (const scenario of ["survival_cases_content_length_oversized", "survival_cases_chunked_oversized"]) {
  test(`${scenario} 时案例目录按浏览器字节上限 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await page.goto("/#assessment");

    await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
    await expect(page.locator("#survival-assessment-status")).toContainText("接口响应超过浏览器安全上限");
    await expect(page.locator("#survival-case-select")).toBeDisabled();
  });
}

for (const scenario of ["survival_detail_content_length_oversized", "survival_detail_chunked_oversized"]) {
  test(`${scenario} 时案例详情不保留旧数据`, async ({ page, request }) => {
    await setScenario(request, "success");
    await openAssessment(page);
    await setScenario(request, scenario);
    await selectTab(page, "survival");

    await page.locator("#survival-case-select").selectOption(SURVIVAL_CASE_ID);

    await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
    await expect(page.locator("#survival-assessment-status")).toContainText("接口响应超过浏览器安全上限");
    await expect(page.locator("#survival-case-summary")).not.toContainText("Oso");
  });
}

for (const scenario of ["survival_replay_content_length_oversized", "survival_replay_chunked_oversized"]) {
  test(`${scenario} 时历史回放清空并恢复按钮`, async ({ page, request }) => {
    await setScenario(request, "success");
    await openAssessment(page);
    await selectCase(page);
    await setScenario(request, scenario);

    await page.locator("#survival-assessment-run").click();

    await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
    await expect(page.locator("#survival-assessment-status")).toContainText("接口响应超过浏览器安全上限");
    await expect(page.locator("#survival-score")).toHaveText("--");
    await expect(page.locator("#survival-assessment-run")).toBeEnabled();
  });
}

test("场景内容被篡改但摘要未更新时详情 fail-closed", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);
  await setScenario(request, "survival_scenario_tampered");
  await selectTab(page, "survival");

  await page.locator("#survival-case-select").selectOption(SURVIVAL_CASE_ID);

  await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
  await expect(page.locator("#survival-assessment-status")).toContainText("场景摘要校验失败");
  await expect(page.locator("#survival-assessment-run")).toBeDisabled();
});

for (const [scenario, message] of [
  ["survival_bad_assessment_id", "评估标识校验失败"],
  ["survival_missing_calculated_at", "人工复核绑定"],
  ["survival_invalid_calculated_at", "人工复核绑定"],
  ["survival_calculated_before_scenario", "人工复核绑定"]
]) {
  test(`${scenario} 时完整评估身份与时间契约 fail-closed`, async ({ page, request }) => {
    await setScenario(request, "success");
    await openAssessment(page);
    await selectCase(page);
    await setScenario(request, scenario);

    await page.locator("#survival-assessment-run").click();

    await expect(page.locator("#survival-assessment-status")).toHaveClass(/assessment-state-error/);
    await expect(page.locator("#survival-assessment-status")).toContainText(message);
    await expect(page.locator("#survival-score")).toHaveText("--");
    await expect(page.locator(`#ai-analysis-reference option[value="survival_assessment:${SURVIVAL_ASSESSMENT_ID}"]`)).toHaveCount(0);
  });
}

test("案例切换使真实迟到成功回放失效且允许重新评估", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);
  await selectCase(page);
  await setScenario(request, "survival_replay_delayed");
  const pending = page.waitForRequest((value) => value.url().includes("/replays/cases/"));

  await page.locator("#survival-assessment-run").click();
  await pending;
  await page.locator("#survival-case-select").selectOption("");
  await page.waitForTimeout(900);

  await expect(page.locator("#survival-score")).toHaveText("--");
  await expect(page.locator(`#ai-analysis-reference option[value="survival_assessment:${SURVIVAL_ASSESSMENT_ID}"]`)).toHaveCount(0);
  await setScenario(request, "success");
  await selectCase(page);
  await runReplay(page);
  await expect(page.locator("#survival-score")).toHaveText("35 / 100");
});

test("生还 Authority 扩展字段、usage 精确 schema 与 SHA 校验通过", async ({ page, request }) => {
  await setScenario(request, "success");
  await openAssessment(page);
  await prepareSurvivalReference(page);
  const pending = page.waitForRequest((value) => value.url().endsWith(AI_PATH));

  await runAI(page);

  await expectSurvivalAIRequest(pending);
  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-replay/);
  await expect(page.locator("#ai-authority-kind")).toHaveText("历史案例回放");
  await expect(page.locator("#ai-authority-id")).toHaveText(SURVIVAL_ASSESSMENT_ID);
  await expect(page.locator("#ai-analysis-digest")).toHaveText(/^[0-9a-f]{64}$/);
});

test("usage 多余字段被浏览器 fail-closed", async ({ page, request }) => {
  await setScenario(request, "ai_bad_usage");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await page.locator("#ai-report-run").click();

  await expectAIFailClosed(page, "生还 Authority 契约无效");
});

for (const [scenario, message] of [
  ["ai_bad_survival_factors", "生还 Authority 契约无效"],
  ["ai_bad_survival_limitations", "生还 Authority 限制契约无效"]
]) {
  test(`${scenario} 时浏览器拒绝损坏的确定性数组`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);
    await prepareSurvivalReference(page);

    await page.locator("#ai-report-run").click();

    await expectAIFailClosed(page, message);
  });
}

test("矛盾生还 narrative 不会覆盖确定性评分、因素与限制", async ({ page, request }) => {
  await setScenario(request, "ai_survival_contradiction");
  await openAssessment(page);
  await prepareSurvivalReference(page);
  const score = await page.locator("#survival-score").textContent();
  const factors = await page.locator("#survival-factor-list li").allTextContents();
  const limitations = await page.locator("#survival-limitation-list li").allTextContents();

  await runAI(page);
  await expect(page.locator("#ai-report-narrative")).toContainText("生还评分已经修改为 0");
  await selectTab(page, "survival");

  await expect(page.locator("#survival-score")).toHaveText(score.trim());
  expect(await page.locator("#survival-factor-list li").allTextContents()).toEqual(factors);
  expect(await page.locator("#survival-limitation-list li").allTextContents()).toEqual(limitations);
});

test("无搜索与 LLM 供应商时返回空数组并保留确定性引用", async ({ page, request }) => {
  await setScenario(request, "ai_no_suppliers");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await runAI(page);

  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-warning/);
  await expect(page.locator("#ai-report-status")).toContainText("AI 服务暂不可用");
  await expect(page.locator("#ai-report-narrative")).toContainText("暂无解释性说明");
  await expect(page.locator("#ai-evidence-list")).toContainText("没有通过校验的实时搜索证据");
  await expect(page.locator("#ai-authority-id")).toHaveText(SURVIVAL_ASSESSMENT_ID);
});

test("慢搜索在 AI 页面预算前降级并保留 Authority", async ({ page, request }) => {
  test.setTimeout(60_000);
  await setScenario(request, "ai_slow_search_degraded");
  await openAssessment(page);
  await prepareSurvivalReference(page);
  const pending = page.waitForRequest((value) => value.url().endsWith(AI_PATH));
  const startedAt = Date.now();

  await runAI(page, 44_000);

  expect(Date.now() - startedAt).toBeGreaterThanOrEqual(6_500);
  await expectSurvivalAIRequest(pending);
  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-warning/);
  await expect(page.locator("#ai-report-status")).toContainText("公开搜索证据暂不可用");
  await expect(page.locator("#ai-authority-id")).toHaveText(SURVIVAL_ASSESSMENT_ID);
  await expect(page.locator("#ai-evidence-list")).toContainText("没有通过校验的实时搜索证据");
  await expect(page.locator("#ai-report-narrative")).toContainText("实时搜索供应商超时");
});

test("慢 LLM 在 AI 页面预算前降级并保留 Authority 与证据", async ({ page, request }) => {
  test.setTimeout(60_000);
  await setScenario(request, "ai_slow_llm_degraded");
  await openAssessment(page);
  await prepareSurvivalReference(page);
  const pending = page.waitForRequest((value) => value.url().endsWith(AI_PATH));
  const startedAt = Date.now();

  await runAI(page, 44_000);

  expect(Date.now() - startedAt).toBeGreaterThanOrEqual(31_500);
  await expectSurvivalAIRequest(pending);
  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-warning/);
  await expect(page.locator("#ai-authority-id")).toHaveText(SURVIVAL_ASSESSMENT_ID);
  await expect(page.locator("#ai-evidence-list a")).toHaveCount(1);
  await expect(page.locator("#ai-report-narrative")).toContainText("暂无解释性说明");
  await expect(page.locator("#ai-report-narrative")).toContainText("解释性大模型暂时不可用");
});

test("解释结构首次无效时固定重试一次并成功", async ({ page, request }) => {
  await setScenario(request, "ai_structured_retry_success");
  await openAssessment(page);
  await prepareSurvivalReference(page);
  const pending = page.waitForRequest((value) => value.url().endsWith(AI_PATH));

  await runAI(page);

  await expectSurvivalAIRequest(pending);
  await expectFixtureCall(request, "ai_report", 1);
  await expectFixtureCall(request, "ai_structured_attempt", 2);
  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-replay/);
  await expect(page.locator("#ai-authority-id")).toHaveText(SURVIVAL_ASSESSMENT_ID);
  await expect(page.locator("#ai-report-narrative")).toContainText("固定重试一次后返回合规说明");
});

test("AI 成功后 503 清空旧 narrative、证据与摘要并可重试", async ({ page, request }) => {
  await setScenario(request, "ai_success_then_503");
  await openAssessment(page);
  await prepareSurvivalReference(page);
  await runAI(page);

  await page.locator("#ai-report-run").click();

  await expectAIFailClosed(page, "智能研判外部供应商暂时不可用");
  await expect(page.locator("#ai-report-run")).toBeEnabled();
  await setScenario(request, "success");
  await runAI(page);
  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-replay/);
});

test("AI 超时清空旧结果并恢复按钮", async ({ page, request }) => {
  await page.clock.install({ time: FIXED_NOW });
  await setScenario(request, "success");
  await openAssessment(page);
  await prepareSurvivalReference(page);
  await runAI(page);
  await setScenario(request, "ai_timeout");

  const pending = page.waitForRequest((value) => value.url().endsWith(AI_PATH));
  await page.locator("#ai-report-run").click();
  await pending;
  await page.clock.fastForward(30_100);

  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-loading/);
  await page.clock.fastForward(15_000);

  await expectAIFailClosed(page, "请求处理超时");
  await expect(page.locator("#ai-report-run")).toBeEnabled();
});

for (const [scenario, label] of [
  ["ai_content_length_oversized", "Content-Length"],
  ["ai_chunked_oversized", "chunked"]
]) {
  test(`AI ${label} 响应超过 1 MiB 时清空旧解释`, async ({ page, request }) => {
    await setScenario(request, "success");
    await openAssessment(page);
    await prepareSurvivalReference(page);
    await runAI(page);
    await setScenario(request, scenario);

    await page.locator("#ai-report-run").click();

    await expectAIFailClosed(page, "接口响应超过浏览器安全上限");
    await expect(page.locator("#ai-report-run")).toBeEnabled();
  });
}

test("AI 引用切换使迟到响应失效并恢复按钮", async ({ page, request }) => {
  await setScenario(request, "ai_delayed");
  await openAssessment(page);
  const lossID = await prepareLossReference(page);
  await prepareSurvivalReference(page);
  await selectTab(page, "ai");
  await page.locator("#ai-analysis-reference").selectOption(`loss_assessment:${lossID}`);
  const pending = page.waitForRequest((value) => value.url().endsWith(AI_PATH));

  await page.locator("#ai-report-run").click();
  await pending;
  await page.locator("#ai-analysis-reference").selectOption(`survival_assessment:${SURVIVAL_ASSESSMENT_ID}`);

  await expect(page.locator("#ai-report-status")).toContainText("已切换要解释的结果");
  await expect(page.locator("#ai-report-run")).toBeEnabled();
  await page.waitForTimeout(900);
  await expect(page.locator("#ai-authority-id")).toHaveText("未提供");
  await runAI(page);
  await expect(page.locator("#ai-authority-id")).toHaveText(SURVIVAL_ASSESSMENT_ID);
});

test("AI 响应进入 WebCrypto 校验后切换引用仍丢弃旧结果", async ({ page, request }) => {
  await setScenario(request, "ai_contradiction");
  await openAssessment(page);
  const lossID = await prepareLossReference(page);
  await prepareSurvivalReference(page);
  await selectTab(page, "ai");
  await page.locator("#ai-analysis-reference").selectOption(`loss_assessment:${lossID}`);
  await delayAIDigest(page);

  await page.locator("#ai-report-run").click();
  await page.waitForFunction(() => window.__aiDigestPending === true);
  await page.locator("#ai-analysis-reference").selectOption(`survival_assessment:${SURVIVAL_ASSESSMENT_ID}`);
  await expect(page.locator("#ai-report-run")).toBeEnabled();
  await page.evaluate(() => window.__releaseAIDigest());

  await expect(page.locator("#ai-report-status")).toContainText("已切换要解释的结果");
  await expect(page.locator("#ai-authority-id")).toHaveText("未提供");
  await expect(page.locator("#ai-report-narrative")).toContainText("尚未生成通俗说明");
  await expect(page.locator("#ai-report-narrative")).not.toContainText("确定性损失金额已经修改为 0 元");
  await expect(page.locator("#ai-report-run")).toBeEnabled();
});

test("证据历史 crawledAt 早于 Authority 时仍接受服务端结果", async ({ page, request }) => {
  await setScenario(request, "ai_crawled_before_authority");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await runAI(page);

  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-replay/);
  await expect(page.locator("#ai-evidence-list a")).toHaveCount(1);
  await expect(page.locator("#ai-authority-id")).toHaveText(SURVIVAL_ASSESSMENT_ID);
});

for (const [scenario, message] of [
  ["ai_bad_time_order", "智能研判子记录时间晚于报告生成时间"],
  ["ai_future_time", "智能研判结果契约无效"],
  ["ai_narrative_before_authority", "智能研判子记录时间早于权威分析解析时间"],
  ["ai_evidence_before_authority", "智能研判子记录时间早于权威分析解析时间"],
  ["ai_evidence_after_narrative", "智能研判证据时间晚于 AI 解释生成时间"],
  ["ai_future_crawled_at", "搜索证据条目无效"],
  ["ai_crawled_after_source", "搜索证据抓取时间晚于来源获取时间"],
  ["ai_bad_sha", "权威分析摘要校验失败"]
]) {
  test(`${scenario} 时智能研判 fail-closed`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);
    await prepareSurvivalReference(page);

    await page.locator("#ai-report-run").click();

    await expectAIFailClosed(page, message);
  });
}

test("AI 与证据注入字符串仅按文本展示", async ({ page, request }) => {
  await setScenario(request, "ai_injection");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await runAI(page);

  await expect(page.locator("#ai-report-narrative")).toContainText("<img src=x");
  await expect(page.locator("#ai-evidence-list")).toContainText("公开灾害信息来源");
  await expect(page.locator("#ai-evidence-list")).toContainText("标题与摘要已去标识化");
  await expect(page.locator("#ai-evidence-list")).not.toContainText("<svg onload=");
  await expect(page.locator("#ai-evidence-list a")).toHaveAttribute("href", "https://mnr.gov.cn/");
  await expect(page.locator("#assessment img, #assessment svg, #assessment script")).toHaveCount(0);
  expect(await page.evaluate(() => Boolean(window.__assessmentInjected))).toBeFalsy();
});

test("同域不同公开证据保留独立不可逆引用且不泄露子域路径", async ({ page, request }) => {
  await setScenario(request, "ai_same_domain_distinct");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await runAI(page);

  const items = page.locator("#ai-evidence-list .assessment-source-item");
  await expect(items).toHaveCount(2);
  await expect(items.locator("a")).toHaveCount(2);
  const hrefs = await items.locator("a").evaluateAll((links) => links.map((link) => link.href));
  expect(hrefs).toEqual(["https://mnr.gov.cn/", "https://mnr.gov.cn/"]);
  const references = await items.locator("code").allTextContents();
  expect(references).toHaveLength(2);
  expect(references[0]).toMatch(/^证据引用 sha256:[0-9a-f]{64}$/);
  expect(references[1]).toMatch(/^证据引用 sha256:[0-9a-f]{64}$/);
  expect(references[0]).not.toBe(references[1]);
  const evidenceText = await page.locator("#ai-evidence-list").textContent();
  expect(evidenceText).not.toContain("zhangsan");
  expect(evidenceText).not.toContain("/news/");
  expect(evidenceText).not.toContain("revision=2");
});

test("搜索来源自由文本字段不会进入 AI 响应或页面", async ({ page, request }) => {
  await setScenario(request, "ai_provenance_pii");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await runAI(page);

  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-replay/);
  const assessmentText = await page.locator("#assessment").textContent();
  for (const sensitive of ["张三", "E12345678", "人民南路四段27号"]) {
    expect(assessmentText).not.toContain(sensitive);
  }
  await expect(page.locator("#ai-evidence-list code")).toHaveText(/^证据引用 sha256:[0-9a-f]{64}$/);
});

test("服务端若回流未最小化 provenance 页面会 fail-closed", async ({ page, request }) => {
  await setScenario(request, "ai_unminimized_provenance");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await page.locator("#ai-report-run").click();

  await expectAIFailClosed(page, "搜索证据公开来源最小化契约无效");
  const assessmentText = await page.locator("#assessment").textContent();
  for (const sensitive of ["张三", "E12345678", "人民南路四段27号"]) {
    expect(assessmentText).not.toContain(sensitive);
  }
});

test("AI 搜索证据非 HTTPS 地址时拒绝旧结果与链接", async ({ page, request }) => {
  await setScenario(request, "ai_unsafe_url");
  await openAssessment(page);
  await prepareSurvivalReference(page);

  await page.locator("#ai-report-run").click();

  await expectAIFailClosed(page, "搜索证据链接不是安全 HTTPS 地址");
  await expect(page.locator("#ai-evidence-list a")).toHaveCount(0);
});

for (const scenario of ["ai_private_source", "ai_localhost_source", "ai_ipv6_source",
  "ai_ipv4_mapped_source", "ai_local_source", "ai_internal_source"]) {
  test(`${scenario} 即使使用 HTTPS 也拒绝私网证据来源`, async ({ page, request }) => {
    await setScenario(request, scenario);
    await openAssessment(page);
    await prepareSurvivalReference(page);

    await page.locator("#ai-report-run").click();

    await expectAIFailClosed(page);
    await expect(page.locator("#ai-evidence-list a")).toHaveCount(0);
  });
}

test("矛盾 AI narrative 不会覆盖确定性金额与状态", async ({ page, request }) => {
  await setScenario(request, "ai_contradiction");
  await openAssessment(page);
  const lossID = await prepareLossReference(page);
  const amounts = await lossValues(page);
  await selectTab(page, "ai");

  await runAI(page);

  await expect(page.locator(".ai-boundary-note")).toContainText("请以前两个页签显示的固定规则结果为准");
  await expect(page.locator("#ai-report-narrative")).toContainText("确定性损失金额已经修改为 0 元");
  expect(await lossValues(page)).toEqual(amounts);
  await expect(page.locator("#loss-assessment-id")).toHaveText(lossID);
  await expect(page.locator("#loss-result-state")).toContainText("可计算");
});

for (const viewport of [{ width: 390, height: 844 }, { width: 1440, height: 900 }]) {
  test(`${viewport.width}x${viewport.height} 评估工作台无控件重叠或横向溢出`, async ({ page, request }) => {
    await page.setViewportSize(viewport);
    await setScenario(request, "success");
    await openAssessment(page);
    await prepareLossReference(page);
    await prepareSurvivalReference(page);
    await selectTab(page, "ai");
    await runAI(page);
    for (const tab of ["loss", "survival", "ai"]) {
      await selectTab(page, tab);
      await page.locator(`#assessment-panel-${tab}`).scrollIntoViewIfNeeded();
      const diagnostics = await assessmentLayout(page);
      expect(diagnostics.sectionOverflow, `${tab} 页签横向溢出`).toBeLessThanOrEqual(2);
      expect(diagnostics.controlCollisions, `${tab} 页签控件重叠`).toEqual([]);
      expect(diagnostics.textOverflow, `${tab} 页签文字溢出`).toEqual([]);
    }
  });
}

async function setScenario(request, name) {
  const response = await request.post("/__fixture/scenario", { data: { name } });
  expect(response.ok()).toBeTruthy();
}

async function openAssessment(page) {
  await page.goto("/#assessment");
  await expect(page.locator("#assessment")).toBeVisible();
  await expect(page.locator("#survival-case-select option")).toHaveCount(4);
}

async function selectTab(page, name) {
  await page.locator(`#assessment-tab-${name}`).click();
  await expect(page.locator(`#assessment-panel-${name}`)).toBeVisible();
}

async function submitLoss(page) {
  await selectTab(page, "loss");
  await page.locator("#loss-snapshot-id").fill(LOSS_SNAPSHOT_ID);
  expect(await page.locator("#loss-snapshot-id").evaluate((input) => input.checkValidity())).toBe(true);
  await page.locator("#loss-assessment-run").click();
  await expect(page.locator("#loss-assessment-status")).not.toHaveClass(/assessment-state-loading/);
}

async function selectCase(page) {
  await selectTab(page, "survival");
  await page.locator("#survival-case-select").selectOption(SURVIVAL_CASE_ID);
  await expect(page.locator("#survival-assessment-run")).toBeEnabled();
  await expect(page.locator("#survival-assessment-status")).not.toHaveClass(/assessment-state-loading/);
}

async function runReplay(page) {
  await page.locator("#survival-assessment-run").click();
  await expect(page.locator("#survival-assessment-status")).not.toHaveClass(/assessment-state-loading/);
}

async function prepareSurvivalReference(page) {
  await selectCase(page);
  await runReplay(page);
  await expect(page.locator(`#ai-analysis-reference option[value="survival_assessment:${SURVIVAL_ASSESSMENT_ID}"]`)).toHaveCount(1);
  await selectTab(page, "ai");
  await page.locator("#ai-analysis-reference").selectOption(`survival_assessment:${SURVIVAL_ASSESSMENT_ID}`);
}

async function prepareLossReference(page) {
  await submitLoss(page);
  const assessmentID = await currentLossAssessmentID(page);
  await expect(page.locator(`#ai-analysis-reference option[value="loss_assessment:${assessmentID}"]`)).toHaveCount(1);
  return assessmentID;
}

async function runAI(page, timeout = 6_000) {
  expect(await page.locator("#ai-report-form").evaluate((form) => form.checkValidity())).toBe(true);
  await page.locator("#ai-report-run").click();
  await expect(page.locator("#ai-report-status")).not.toHaveClass(/assessment-state-loading/, { timeout });
}

async function expectSurvivalAIRequest(pending) {
  const body = (await pending).postDataJSON();
  expect(body).toEqual({
    analysisRef: { kind: "survival_assessment", id: SURVIVAL_ASSESSMENT_ID }, evidenceLimit: 5
  });
  expect(body).not.toHaveProperty("force");
}

async function delayAIDigest(page) {
  await page.evaluate(() => {
    const subtle = window.crypto.subtle;
    const original = subtle.digest.bind(subtle);
    let release;
    const gate = new Promise((resolve) => { release = resolve; });
    window.__aiDigestPending = false;
    window.__releaseAIDigest = release;
    Object.defineProperty(subtle, "digest", { configurable: true, value: async (...args) => {
      window.__aiDigestPending = true;
      await gate;
      return original(...args);
    } });
  });
}

async function expectLossFailClosed(page, message) {
  await expect(page.locator("#loss-assessment-status")).toHaveClass(/assessment-state-error/);
  if (message) await expect(page.locator("#loss-assessment-status")).toContainText(message);
  await expect(page.locator("#loss-assessment-id")).toHaveText("尚未生成估算");
  await expect(page.locator("#loss-low-amount")).toHaveText("--");
  await expect(page.locator("#loss-central-amount")).toHaveText("--");
  await expect(page.locator("#loss-high-amount")).toHaveText("--");
  await expect(page.locator("#loss-source-list")).toContainText("完成估算后");
  await expect(page.locator(`#ai-analysis-reference option[value^="loss_assessment:"]`)).toHaveCount(0);
}

async function expectAIFailClosed(page, message) {
  await expect(page.locator("#ai-report-status")).toHaveClass(/assessment-state-error/);
  if (message) await expect(page.locator("#ai-report-status")).toContainText(message);
  await expect(page.locator("#ai-authority-id")).toHaveText("未提供");
  await expect(page.locator("#ai-analysis-digest")).toHaveText("未提供");
  await expect(page.locator("#ai-report-narrative")).toContainText("尚未生成通俗说明");
  await expect(page.locator("#ai-evidence-list")).toContainText("尚未获得搜索证据");
}

async function expectFixtureCall(request, operation, count) {
  await expect.poll(async () => {
    const response = await request.get("/__fixture/state");
    const payload = await response.json();
    return payload.data.calls[operation] || 0;
  }).toBe(count);
}

async function expectLossSourceGroup(page, label, hrefPart) {
  const group = page.locator("#loss-source-list .assessment-source-item").filter({ hasText: label });
  await expect(group).toHaveCount(1);
  await expect(group.locator(`a[href*="${hrefPart}"]`)).toHaveCount(1);
}

async function fetchLossProjectionInPage(page, assessmentID) {
  return (await fetchLossAssessmentInPage(page, assessmentID)).evidence.spatialAnalysis;
}

async function fetchLossAssessmentInPage(page, assessmentID) {
  return page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/loss/assessments/${encodeURIComponent(id)}`, {
      headers: { Accept: "application/json" }
    });
    if (!response.ok) throw new Error(`读取损失评估失败: ${response.status}`);
    const payload = await response.json();
    return payload.data;
  }, assessmentID);
}

async function fetchLossAuditInPage(page, assessmentID) {
  return page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/loss/assessments/${encodeURIComponent(id)}/sources`, {
      headers: { Accept: "application/json" }
    });
    if (!response.ok) throw new Error(`读取损失来源审计失败: ${response.status}`);
    const payload = await response.json();
    return payload.data;
  }, assessmentID);
}

async function lossValues(page) {
  return Promise.all(["#loss-low-amount", "#loss-central-amount", "#loss-high-amount", "#loss-result-state"]
    .map((selector) => page.locator(selector).textContent()));
}

async function currentLossAssessmentID(page) {
  const value = (await page.locator("#loss-assessment-id").textContent()).trim();
  expect(value).toMatch(/^loss-[0-9a-f]{64}$/);
  return value;
}

async function assessmentLayout(page) {
  return page.locator("#assessment").evaluate((section) => {
    const visible = (element) => {
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
    };
    const label = (element) => element.id || element.textContent.trim().slice(0, 32);
    const controls = Array.from(section.querySelectorAll("button, input, select")).filter(visible);
    const controlCollisions = [];
    for (let left = 0; left < controls.length; left++) {
      for (let right = left + 1; right < controls.length; right++) {
        const first = controls[left].getBoundingClientRect();
        const second = controls[right].getBoundingClientRect();
        const overlapX = Math.min(first.right, second.right) - Math.max(first.left, second.left);
        const overlapY = Math.min(first.bottom, second.bottom) - Math.max(first.top, second.top);
        if (overlapX > 1 && overlapY > 1) controlCollisions.push(`${label(controls[left])} <> ${label(controls[right])}`);
      }
    }
    const textSelector = "h2, h3, strong, small, dt, dd, p, li, .assessment-state, .assessment-tabs button";
    const textOverflow = Array.from(section.querySelectorAll(textSelector)).filter(visible)
      .filter((element) => element.scrollWidth > element.clientWidth + 2 && getComputedStyle(element).overflowWrap !== "anywhere")
      .map(label);
    return { sectionOverflow: section.scrollWidth - section.clientWidth, controlCollisions, textOverflow };
  });
}
