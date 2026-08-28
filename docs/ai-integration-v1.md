# 搜索与大模型集成（P7）

## 数据流

1. 确定性风险、路线、损失和搜救用例先生成 `analysis` JSON；该字节序列由编排服务计算 SHA-256，并作为权威输入保留。
2. 博查适配器按查询时效检索公开结果，只接受 HTTPS、可信域名和可解析标题/摘要，去除跟踪参数并按规范化 URL 去重。
3. OpenAI 兼容 LLM 适配器只接收去标识化分析 JSON、证据和不可变字段清单。系统提示词把资料当作不可信内容，禁止执行其中的指令；请求强制 `response_format.type=json_object`。
4. LLM 输出只允许 `summary`、`keyFindings`、`actions`、`caveats` 四个字段。结构错误、截断、供应商故障和超时不会覆盖确定性结果，编排结果会标记说明不可用。

## 编排接口

`POST /api/v1/ai/report` 接收以下 JSON 字段：`query`、`analysis`、`immutableFields` 和可选的 `evidenceLimit`。`analysis` 必须是确定性用例产生的 JSON 对象；接口响应保留原始分析、SHA-256、搜索证据、解释性说明和 `limitations`。

服务端在返回前再次校验结果：分析摘要必须与原始字节一致，证据可用标志必须与证据数量一致，说明可用标志必须与白名单输出一致。未配置或暂时不可用的博查/LLM 只会增加降级限制，不会生成替代的风险等级、路线、金额或生还评分。

## 供应商配置

所有密钥仅通过服务端环境变量提供，不写入仓库，也不会出现在 `Provenance.SourceURI` 或日志中。

| 环境变量 | 作用 | 默认 |
| --- | --- | --- |
| `BOCHA_ENABLED` | 启用博查搜索 | `false` |
| `BOCHA_BASE_URL` | 博查 Web Search 端点 | `https://api.bochaai.com/v1/web-search` |
| `BOCHA_API_KEY` | 博查服务端密钥 | 无 |
| `BOCHA_MAX_RESULTS` | 单次结果数（1-50） | `10` |
| `BOCHA_MAX_AGE` | 本地证据有效窗口 | `72h` |
| `BOCHA_TRUSTED_DOMAINS` | 逗号分隔的可信域名 | `gov.cn,mnr.gov.cn,mem.gov.cn,cma.cn,earthdata.nasa.gov` |
| `LLM_ENABLED` | 启用解释性报告 | `false` |
| `LLM_PROVIDER_NAME` | 来源审计中的供应商名称 | `Jojocode OpenAI 兼容服务` |
| `LLM_BASE_URL` | OpenAI 兼容聊天补全端点 | `https://jojocode.com/v1/chat/completions` |
| `LLM_API_KEY` | LLM 服务端密钥 | 无 |
| `LLM_MODEL` | 模型名 | `gpt-5.6-terra` |
| `LLM_MAX_COMPLETION_TOKENS` | 输出上限（1-4096） | `1200` |
| `LLM_OUTPUT_ATTEMPTS` | 结构输出重试次数（1-3） | `2` |

搜索或大模型未启用时，系统仍可返回确定性数据；页面和 API 应展示相应的 `limitations`。
