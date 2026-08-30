# P9.1 入站安全边界

## 访问策略

- `/healthz`、`/readyz`、控制台静态资源及所有只读 `GET/HEAD` 接口保持可读。
- `/api/v1` 下的非安全方法统一要求 `Authorization: Bearer <APP_ADMIN_TOKEN>`。
- 生产环境缺少 `APP_ADMIN_TOKEN` 时拒绝启动；开发环境缺少令牌时以只读模式运行，所有写请求返回 `503 admin_security_unconfigured`。
- 浏览器控制台只在当前页面的 JavaScript 内存中保存令牌。令牌不写入 Cookie、URL、DOM 属性、`localStorage` 或 `sessionStorage`，刷新页面后自动丢失。

## 浏览器与脚本请求

所有写请求必须同时满足：

- `Content-Type: application/json`，仅允许可选的 UTF-8 charset；
- `Content-Encoding` 缺失或为 `identity`；
- `X-CSRF-Token: ai-gdm-browser-v1`；
- `Origin` 缺失或与服务端实际收到的请求协议及 Host 同源，存在 `Sec-Fetch-Site` 时必须为 `same-origin`；
- 请求体不超过 1 MiB，各业务接口仍执行更小的专用上限。

命令行调用示例：

```sh
curl -X POST http://127.0.0.1:8080/api/v1/hazards/landslide/refresh \
  -H "Authorization: Bearer $APP_ADMIN_TOKEN" \
  -H "X-CSRF-Token: ai-gdm-browser-v1" \
  -H "Content-Type: application/json" \
  --data '{}'
```

## 限流与响应头

- 限流键只使用直连客户端 `RemoteAddr`，不信任外部提供的 `X-Forwarded-For`。
- 刷新、AI、损失和地图写请求使用不同成本；客户端表有固定容量及过期回收，溢出客户端进入共享限流桶。
- 非法或重复 Request-ID 在写入拒绝日志前同样消耗限流预算，超出预算后返回 `429`，避免通过错误请求放大日志。
- `429` 返回 `Retry-After` 和规范 Request-ID。
- 全局下发 CSP、点击劫持防护、`nosniff`、严格 referrer、Permissions Policy、COOP/CORP；HSTS 只对服务端实际收到的 TLS 请求启用。
- 默认不信任 `X-Forwarded-Proto`。如后续使用 TLS 终止代理，必须先建立显式可信代理配置，不能仅凭客户端可写请求头改变同源判断。

## 密钥审计

- 服务端配置校验拒绝占位密钥、控制字符、超长值及不同用途间的密钥复用，错误消息只包含配置项名称。
- Git 候选树门禁直接读取暂存 blob，并单独检查未跟踪文件；拒绝私钥、运行环境文件、超量或非 UTF-8 内容及常见高置信度令牌格式。
- 安全验证在只读候选 tree 快照上运行，并记录 tree SHA 与 source SHA-256；工作区漂移、旧摘要或共享 Docker 名称均不能冒充该次结果。
- P9.1 采用单一管理员角色。按地区中心、操作员和审计员拆分 RBAC 属后续身份系统范围，不能把当前单令牌描述为细粒度授权。
