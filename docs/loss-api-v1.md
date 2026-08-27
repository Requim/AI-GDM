# 灾害损失评估接口 v1

## 目标

接口把确定性损失引擎的结果保存为可回放记录，并提供来源审计。金额由 Go 应用层按 \`ai-gdm-loss-formula-v1\` 计算，接口层不接受客户端覆盖公式、金额或置信度。

## 路由

服务挂载在 \`/api/v1/loss\`：

- \`POST /assessments\`：提交风险快照、强度带和带来源的资产暴露，返回 \`201\` 与 \`Location\`。
- \`GET /assessments/{assessmentId}\`：读取不可变评估结果。
- \`GET /assessments/{assessmentId}/sources\`：读取评估时保存的输入引用和限制。

请求体使用严格 JSON 解码，未知字段、重复 JSON、超过 1 MiB 或超过 1000 条暴露都会被拒绝。暴露量必须携带真实来源、数据年份和覆盖率；缺少风险快照、空间分析、成本或脆弱性基线时返回 \`insufficient_data\`，不会生成示例金额。

## 持久化与幂等

\`loss_assessments\` 保存评估 JSON、来源引用、快照标识、公式版本和状态。相同 ID 的相同内容可以重复提交；相同 ID 的不同内容会返回校验错误，避免审计记录被覆盖。数据库迁移使用嵌入式 SQL，查询后再次执行领域校验。

## 来源安全

来源引用在响应层删除 URL 用户信息以及 \`key\`、\`token\`、\`signature\`、\`api_key\` 等查询参数。数据库仍只保存评估所需的来源摘要，原始供应商密钥必须通过运行环境注入，不能进入请求、响应或 Git。

## 错误状态

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | \`invalid_request\` | 请求 JSON、标识或字段边界无效 |
| 404 | \`assessment_not_found\` | 没有对应评估记录 |
| 503 | \`insufficient_data\` | 实时快照、空间分析或基线不足 |
| 504 | \`request_timeout\` | 上游或请求处理超时 |
| 500 | \`internal_error\` | 未分类的服务内部错误 |

每个响应都带 \`requestId\`；数值结果只能作为辅助研判，不能替代现场核查、专业评估或政府应急指挥流程。
