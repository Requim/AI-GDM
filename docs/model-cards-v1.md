# AI-GDM 模型卡索引 v1

## 总体原则

AI-GDM 的风险等级、路线排序、损失金额和生还评分均由版本化确定性代码计算。大模型只接收服务端构造的 allowlist Authority，用于生成非权威文字解释，不得回写或覆盖确定性结果。

| 模块 | 当前版本 |
| --- | --- |
| 风险研判 | `ai-gdm-risk-rules-v1` |
| 路线安全 | `ai-gdm-route-safety-rules-v1` |
| 损失公式 | `ai-gdm-loss-formula-v2` |
| 生还回放 | `ai-gdm-survival-rules-v1` |
| AI Authority 封包 | `ai-gdm-authority-v1` |
| LHASA 栅格转换 | `lhasa-gdal-3-gdal-3.13.3-china-adm0-fractional-stats` |

## 风险分级规则

- 版本：`ai-gdm-risk-rules-v1`
- 适用灾种：当前生产数据链仅为 `landslide`。
- 输入：内容寻址的 LHASA 快照、风险区、来源时效和可用的气象上下文。
- 输出：风险等级、置信度、触发因素、数据状态和规则版本。
- 方法：按固定阈值和时效规则确定性计算，不使用 LLM 评分；CHN ADM0 边界亚像元使用同等级概率掩膜，均值按几何相交比例加权，最小和最大值取所有相交的同等级像元。
- 限制：置信度表示数据质量，不是灾害概率。Open-Meteo 只形成 `context_only`，不得提升 LHASA 等级。LHASA 是近实时模型输出而非现场观测；当前阈值是 AI-GDM 演示规则，不是 NASA 或国家正式预警标准。

详细口径见 `docs/risk-policy-v1.md`。

## 路线安全规则

- 版本：`ai-gdm-route-safety-rules-v1`
- 输入：高德候选路线、对应风险快照和风险区几何。
- 输出：排除原因、安全候选、风险分数、时间/距离和确定性排名。
- 方法：先排除穿越风险区或合同损坏的路线，再按风险、时间和距离排序。
- 限制：候选路线不证明道路开放、运力可用或现场可通行；最终调度必须由应急人员确认。

详细口径见 `docs/map-integration-v1.md`。

## 损失估算规则

- 版本：`ai-gdm-loss-formula-v2`
- 输入：不可变风险评估、无几何有界空间暴露投影、同一已批准基线集合和版本化脆弱性参数。
- 输出：人民币低/中/高条件情景、置信度、影响范围、分项状态、输入引用与限制。
- 方法：按风险区和资产类型执行定点有理数计算，使用稳定舍入规则；跨区 feature 按全局身份去重。
- 限制：当前直接物理损害只计入道路和风险区内 POI 设施，人口仅作为影响上下文；金额不是校准后的期望损失。仓库不内置可作为生产结论的资产价格与脆弱性数值，缺少真实、有效、已批准基线时必须返回 `insufficient_data`。

详细口径见 `docs/loss-engine-v1.md`、`docs/loss-baseline-v1.md` 和 `docs/loss-api-v1.md`。

## 历史生还回放规则

- 版本：`ai-gdm-survival-rules-v1`
- 用途：公开历史案例与合成匿名场景的流程回放。
- 输入：五类有界、去标识化信号及其完整度，绑定案例、场景摘要和模型卡。
- 输出：概率区间、分数区间、搜救优先级、主要因素、限制和 `humanReviewStatus=required`。
- 方法：确定性规则，不训练、不拟合真实失联人员数据。
- 禁止用途：不得录入或推断当前真实人员的生还概率，不得替代搜救指挥和医学判断。

机器可读模型卡由 `GET /api/v1/survival/model-card` 提供。详细口径见 `docs/survival-model-card-v1.md` 和 `docs/survival-assessment-v1.md`。

## AI 解释层

- Authority 封包版本：`ai-gdm-authority-v1`。
- 输入：风险、路线、损失或历史回放的服务端 allowlist 投影，以及经过可信域、时效、隐私和预算过滤的搜索证据。
- 输出：摘要、关键发现、建议动作、证据与限制。
- 机械边界：Authority SHA-256 绑定种类、标识、版本、schema、确定性分析和不可变字段；浏览器再次校验摘要并把 AI 文本与确定性卡片隔离。
- 降级：无搜索或 LLM 时仍可返回 Authority；说明和证据使用非 null 空数组与明确不可用状态。

详细口径见 `docs/ai-integration-v1.md`、`docs/llm-client-v1.md` 和 `docs/search-evidence-v1.md`。
