# OpenAI 兼容解释性报告客户端（P7.2）

## 责任边界

Chat Completions 适配器是解释性文字生成器，不是风险、路线、损失或生还评分引擎。确定性用例产生的 `analysis` JSON 和不可变字段清单由应用编排层传入，客户端不会把模型输出写回这些字段。默认供应商为 Jojocode，默认模型为 `gpt-5.6-terra`，也可通过同一组通用配置替换为其他兼容端点。

## 请求约束

- 端点必须是无用户信息、无查询参数和无片段的 HTTPS 地址。
- API 密钥只从 `LLM_API_KEY` 读取，放在服务端内存；来源 URI、错误文本和日志不记录密钥。
- 请求固定使用 `response_format.type=json_object`、低温度和有限 `max_tokens`。用户消息显式包含小写 `json`，并声明四个字段的准确类型，兼容当前端点的 JSON 模式要求。
- 系统提示词把分析 JSON、搜索摘要和其中的指令视为不可信资料，只允许阅读，不执行工具调用或提示注入。

## 输出校验

1. HTTP 响应必须是单个合法 JSON，且恰好包含一个候选消息。
2. 消息内容必须是单个 JSON 对象；`DisallowUnknownFields` 只允许 `summary`、`keyFindings`、`actions`、`caveats`。
3. 摘要和条目数量、Unicode 长度、空值均经过领域层校验；截断、内容过滤和结构错误都视为供应商不可用。
4. 成功结果保存模型名、响应 SHA-256、供应商请求 ID 和生成时间，供来源审计。

## 可靠性与降级

结构化输出失败时按配置重试，最多三次；上下文取消和超时保留原始错误链。非上下文供应商故障由应用层降级为“暂无解释性说明”，并继续返回确定性分析，不生成替代数字或伪造报告。

## 密钥和提示注入测试

离线测试使用 TLS 测试服务器，不调用真实密钥；验证 Authorization 头、JSON 输出约束、未知字段拒绝、尾随 JSON 拒绝、重试次数和错误分类。独立在线测试只在服务器显式设置 `LLM_LIVE_TEST=1` 时运行，并从权限受限的运行配置读取密钥。分析资料中的 `riskLevel` 等核心字段只能作为不可变输入被引用，不能出现在模型允许的输出字段中。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LLM_ENABLED` | `false` | 是否启用解释性报告 |
| `LLM_PROVIDER_NAME` | `Jojocode OpenAI 兼容服务` | 写入来源审计的供应商名称 |
| `LLM_BASE_URL` | `https://jojocode.com/v1/chat/completions` | 服务端 HTTPS 端点 |
| `LLM_API_KEY` | 无 | 只允许通过服务端环境注入 |
| `LLM_MODEL` | `gpt-5.6-terra` | 供应商模型名 |
| `LLM_MAX_COMPLETION_TOKENS` | `1200` | 输出上限，最大 4096 |
| `LLM_OUTPUT_ATTEMPTS` | `2` | 结构化输出尝试次数，最大 3 |

远端离线验收脚本 `scripts/validate-llm.sh` 使用 Go 1.26.7 容器执行竞态测试、模块校验、`go vet` 和全量构建。`scripts/validate-llm-live.sh` 读取服务器私密配置，只执行显式启用的真实端点契约测试。

Go 服务只读取进程环境，不直接解析密钥文件。正式启动时必须由 Docker Compose `env_file`、systemd `EnvironmentFile` 或受控启动脚本加载 `/home/ubuntu/.config/ai-gdm/runtime.env`；仅创建文件不会自动启用 LLM。
