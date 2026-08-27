# 实时搜索证据适配（P7.1）

## 目标

博查搜索只提供研判证据候选，不直接改变风险等级、路线、损失金额或失联人员评分。适配器把供应商响应转换为领域 `report.Evidence`，保留可审计来源和抓取时间。

## 请求边界

- 端点必须是无用户信息、无查询参数和无片段的 HTTPS 地址。
- API 密钥只从 `BOCHA_API_KEY` 读取，放在服务端内存，请求日志和来源 URI 不包含密钥。
- 查询去除首尾空白，限制为 512 个 Unicode 字符；结果数限制为 1 至 50。
- 共用 HTTP 客户端负责超时、重试、限流、响应大小和错误脱敏。

## 证据筛选

1. 仅接受 HTTPS 结果地址，移除 `utm_*`、`gclid`、`fbclid`、`key`、`token` 等跟踪或敏感查询参数。
2. 只接受配置的可信域名及其子域名，默认包含国家部委、气象和 NASA Earthdata 域名。
3. 优先使用发布时间，其次使用抓取时间判断本地时效窗口；供应商没有时间字段时保留结果，但页面必须依据来源状态做人工复核。
4. 规范化 URL 后去重，保存响应 SHA-256、供应商请求 ID、来源修订和 `freshness_filtered` 等质量标志。
5. 标题或摘要缺失、业务状态错误、非法 JSON、尾随 JSON 和响应过大时拒绝结果，不生成替代内容。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BOCHA_ENABLED` | `false` | 是否启用搜索供应商 |
| `BOCHA_BASE_URL` | `https://api.bochaai.com/v1/web-search` | 服务端 HTTPS 端点 |
| `BOCHA_API_KEY` | 无 | 只允许通过环境变量注入 |
| `BOCHA_MAX_RESULTS` | `10` | 单次结果上限，最大 50 |
| `BOCHA_MAX_AGE` | `72h` | 本地证据时效窗口 |
| `BOCHA_TRUSTED_DOMAINS` | `gov.cn,mnr.gov.cn,mem.gov.cn,cma.cn,earthdata.nasa.gov` | 逗号分隔域名 |

## 降级规则

搜索未启用或供应商暂时失败时，应用层返回确定性分析并增加限制说明；不会把缓存的旧结果伪装成实时结果，也不会让搜索文本覆盖核心数值结论。

## 验证

远端验收脚本 `scripts/validate-search.sh` 在腾讯 Ubuntu 验证服务器上运行 Go 1.26.7 容器，覆盖竞态测试、模块校验、静态检查和全量构建。
