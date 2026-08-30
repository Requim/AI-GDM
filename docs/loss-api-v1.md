# 灾害损失评估接口 v1

## 目标

接口把确定性损失引擎的结果保存为可回放记录，并提供来源审计。金额由 Go 应用层按 `ai-gdm-loss-formula-v2` 计算，接口层不接受客户端覆盖公式、暴露量、金额或置信度。

## 路由

服务挂载在 `/api/v1/loss`：

- `POST /assessments`：只提交 `{"snapshotId":"..."}`，服务端读取绑定的风险、空间暴露和已批准基线，返回 `201` 与同源 `Location`。
- `GET /assessments/{assessmentId}`：读取不可变评估结果。
- `GET /assessments/{assessmentId}/sources`：读取评估时保存的输入引用和限制。

请求体使用严格 JSON 解码，未知字段、尾随第二个 JSON 或超过 1 MiB 都会被拒绝。客户端不能提交区域、强度、暴露项或基线。服务端只接受当前有效、状态为 `available` 的风险快照，并在同一权威投影中读取全局去重的人口、道路和设施证据；缺少完整空间投影、成本或脆弱性基线时返回 `insufficient_data`，不会生成示例金额。

## 持久化与幂等

`loss_assessments` 保存评估 JSON、来源引用、快照标识、公式版本和状态。ID 只绑定规范化权威输入、基线/投影版本和确定性参数，计算时间不改变身份。相同 ID 的相同内容可以幂等重试；相同 ID 的不同内容会回滚并返回完整性错误，避免审计记录被覆盖。数据库迁移使用嵌入式 SQL，读取时严格校验 JSON、审计列和字节预算。

## 来源安全

来源引用只允许公开 HTTPS 主机，并删除 URL 用户信息、fragment 以及 `key`、`token`、`signature`、`api_key` 等凭据参数；私网、loopback、内部域名和解析失败的引用不会变成可点击链接。数据库只保存评估所需的有界来源摘要，原始供应商密钥必须通过运行环境注入，不能进入请求、响应或 Git。

## 错误状态

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `invalid_request` | 请求 JSON、标识或字段边界无效 |
| 404 | `assessment_not_found` | 没有对应评估记录 |
| 503 | `insufficient_data` | 实时快照、空间分析或基线不足 |
| 504 | `request_timeout` | 上游或请求处理超时 |
| 500 | `internal_error` | 未分类的服务内部错误 |

每个响应都带 `requestId`；数值结果只能作为辅助研判，不能替代现场核查、专业评估或政府应急指挥流程。
