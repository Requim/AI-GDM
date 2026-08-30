# AI-GDM v0.1.0 发布说明

发布日期：2026-08-30

## 交付范围

`v0.1.0` 是面向面试评估的首个可部署 MVP，包含：

- 中文浏览器控制台、实时风险地图、疏散工作台、损失评估、历史生还回放和 AI 解释面板。
- Go 1.26.7 Linux/Windows AMD64 可执行文件。
- Linux AMD64 应用、PostGIS、Redis 三个完整 Docker 原始镜像。
- Shell/PowerShell 一键部署入口、Compose、空密钥模板、manifest 和 SHA-256。
- 数据来源、模型卡、限制、安全、部署与 API 文档。
- 从最终发布 commit 动态导出的 Git 提交日志及 SHA-256 Release 附件。

## 主要能力

- NASA Earthdata GIS LHASA 近实时滑坡数据采集、GDAL 空间处理、30 分钟调度和最后成功结果回退。
- Open-Meteo 气象上下文、PostGIS 风险区与空间暴露分析。
- 高德 Web 服务候选设施与驾车、步行、公交路线，服务端风险区门禁和确定性排序。
- 版本化损失公式、已批准基线约束、十进制金额 wire 契约和完整来源审计。
- 公开历史案例与合成匿名场景的确定性生还回放及机器可读模型卡。
- 博查证据过滤与 OpenAI 兼容 LLM 的 Authority 约束解释；无供应商时保留确定性结果并降级。
- 请求边界、管理员令牌、CSRF、速率限制、安全响应头、指标、审计和供应商状态。

## 发布制品

- `ai-gdm-v0.1.0-linux-amd64.tar.gz`
- `ai-gdm-v0.1.0-linux-amd64.tar.gz.sha256`
- `ai-gdm-v0.1.0-git-log.txt`
- `ai-gdm-v0.1.0-git-log.txt.sha256`

发布包内 `manifest.json` 是源码与制品的权威绑定，必须记录最终 `sourceCommit`、Git tree、规范源码 SHA-256、镜像 ID、平台、大小和全部 payload 摘要。

## 验证口径

- Go 全量测试、vet 和 build。
- PostgreSQL/PostGIS、Redis、GDAL、迁移和持久化验证。
- 风险地图、疏散、评估、安全与系统浏览器回归。
- 从空镜像缓存执行 `docker load`、一键部署、重复部署和保留卷停启。
- 腾讯 Ubuntu 常驻部署后从服务器内外检查控制台、存活/就绪探针、容器身份、只读根和数据卷。

精确 commit、tree、包摘要、Release 资产与部署入口以 Git tag、GitHub Release、包内 manifest 和阶段进度记录为准。

## 已知限制

完整限制见 `docs/limitations-v1.md`。关键边界包括：当前真实主链仅为滑坡；无真实已批准损失基线时损失评估不可用；历史生还回放不适用于真实人员；候选路线不证明道路开放；离线镜像不代表实时业务可离线；默认部署为单节点 HTTP 服务。
