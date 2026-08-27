# Qwen 解释性报告客户端（P7.2）

## 责任边界

Qwen 适配器是解释性文字生成器，不是风险、路线、损失或生还评分引擎。确定性用例产生的 `analysis` JSON 和不可变字段清单由应用编排层传入，客户端不会把模型输出写回这些字段。

## 请求约束

- 端点必须是无用户信息、无查询参数和无片段的 HTTPS 地址。
- API 密钥只从 `QWEN_API_KEY` 读取，放在服务端内存；来源 URI、错误文本和日志不记录密钥。
- 请求固定使用 `response_format.type=json_object`、低温度和有限 `max_tokens`，关闭思考扩展以控制输出范围。
- 系统提示词把分析 JSON、搜索摘要和其中的指令视为不可信资料，只允许阅读，不执行工具调用或提示注入。

## 输出校验

1. HTTP 响应必须是单个合法 JSON，且恰好包含一个候选消息。
2. 消息内容必须是单个 JSON 对象；`DisallowUnknownFields` 只允许 `summary`、`keyFindings`、`actions`、`caveats`。
3. 摘要和条目数量、Unicode 长度、空值均经过领域层校验；截断、内容过滤和结构错误都视为供应商不可用。
4. 成功结果保存模型名、响应 SHA-256、供应商请求 ID 和生成时间，供来源审计。

## 可靠性与降级

结构化输出失败时按配置重试，最多三次；上下文取消和超时保留原始错误链。非上下文供应商故障由应用层降级为“暂无解释性说明”，并继续返回确定性分析，不生成替代数字或伪造报告。

## 密钥和提示注入测试

阶段测试使用本地 TLS 测试服务器，不调用真实密钥；验证 Authorization 头、JSON 输出约束、未知字段拒绝、尾随 JSON 拒绝、重试次数和错误分类。分析资料中的 `riskLevel` 等核心字段只能作为不可变输入被引用，不能出现在模型允许的输出字段中。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `QWEN_ENABLED` | `false` | 是否启用解释性报告 |
| `QWEN_BASE_URL` | `https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions` | 服务端 HTTPS 端点 |
| `QWEN_API_KEY` | 无 | 只允许通过环境变量注入 |
| `QWEN_MODEL` | `qwen-plus` | 供应商模型名 |
| `QWEN_MAX_COMPLETION_TOKENS` | `1200` | 输出上限，最大 4096 |
| `QWEN_OUTPUT_ATTEMPTS` | `2` | 结构化输出尝试次数，最大 3 |

远端验收脚本 `scripts/validate-llm.sh` 使用 Go 1.26.7 容器执行竞态测试、模块校验、`go vet` 和全量构建，不需要真实 API 密钥。
