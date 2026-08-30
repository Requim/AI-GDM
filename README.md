# AI-GDM 地质灾害智能辅助决策系统

AI-GDM 是面向地质灾害监控中心的浏览器 Web 应用 MVP。系统使用 Go 服务端、PostgreSQL/PostGIS、Redis 和 GDAL，将公开近实时数据、确定性规则、地图服务与受约束的大模型解释组合为可审计的辅助决策流程。

## 已实现能力

- 滑坡风险预警：采集 NASA Earthdata GIS 的 LHASA 近实时栅格，经 GDAL 裁剪、分级和矢量化后展示风险区、时效与来源。
- 疏散调度：调用高德 Web 服务搜索候选避险设施并规划驾车、步行或公交路线，再由服务端排除穿越风险区的路线并排序。
- 损失评估：从权威空间暴露投影和已批准基线生成可解释人民币区间；缺少真实批准基线时明确返回数据不足。
- 生还评估：只对公开历史案例和合成匿名场景执行确定性回放，输出概率区间、搜救优先级、因素与人工复核要求。
- AI 研判：大模型只解释服务端固化的风险、路线、损失或历史回放结果，不能修改确定性等级、排名、金额或评分。
- 运行保障：展示数据源状态、最后成功时间、过期/降级状态、审计事件和 Prometheus 指标。

本项目不是国家正式预警发布系统，不替代现场调查、交管指令、应急指挥或专业人员复核。

## 架构

```text
浏览器
  -> Go HTTP / 模板 / 少量 JavaScript
  -> 应用用例与确定性领域规则
  -> PostgreSQL + PostGIS / Redis / GDAL
  -> Earthdata / Open-Meteo / geoBoundaries / WorldPop / Overpass
  -> 高德 Web 服务 / 博查搜索 / OpenAI 兼容 LLM
```

领域层和应用层通过小粒度 ports 隔离数据库、地图和 AI 供应商。空间数据统一保存为 WGS84；GCJ-02 转换仅发生在高德适配器边界。完整设计见 `docs/architecture.md`。

## 一键部署

正式 Release 的 Linux AMD64 离线包包含三个运行镜像、Linux/Windows Go 可执行文件、Compose、部署脚本、manifest 与 SHA-256。镜像可离线加载，但实时业务数据仍要求服务器能够访问互联网。

Linux 解压发布包并核对外层校验和后执行：

```sh
sha256sum -c ai-gdm-v0.1.0-linux-amd64.tar.gz.sha256
tar -xzf ai-gdm-v0.1.0-linux-amd64.tar.gz
cd ai-gdm-v0.1.0-linux-amd64
sudo sh deploy/deploy.sh
```

Windows Docker Desktop 需切换为 Linux containers：

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\deploy.ps1
```

默认访问：

- 控制台：`http://服务器地址:8080/`
- 存活探针：`GET /healthz`
- 就绪探针：`GET /readyz`
- 指标：受管理员令牌保护的 `GET /metrics`

`/readyz` 表示应用已进入服务期，不代表全部互联网供应商当前可用。默认刷新间隔为 30 分钟；LHASA 默认 12 小时后过期，Open-Meteo 最后成功结果最多回退 6 小时。

发布包不会包含 API Key、管理员令牌、数据库口令、运行数据或损失基线。首次部署会生成私有运行配置；供应商 Key 应通过权限受限的环境文件注入。详细步骤见 `docs/deployment-v1.md` 和 `docs/package-v1.md`。

生产写 API 使用管理员 Bearer Token、同源 Origin 与固定 CSRF 请求头保护。令牌只允许保存在当前页面内存，不进入 URL、Cookie 或浏览器持久存储。默认 HTTP 服务没有 TLS，公网长期运行前应增加可信反向代理和证书。

## 开发与验证

```sh
go test ./...
go vet ./...
go build ./...
```

容器、数据库、缓存、完整构建和浏览器验收在指定腾讯 Ubuntu 环境运行。主要门禁：

```sh
sudo sh scripts/validate-go.sh
sudo sh scripts/validate-package.sh
sudo sh scripts/validate-deploy.sh
```

提交前系统门禁已覆盖系统清单 `15/15`、风险地图 Chromium `17/17`、疏散 Chromium `42/42`、评估 Chromium `119/119`，P10.3 还在空镜像缓存的 Docker-in-Docker 中验证三个镜像加载、重复部署和 PostgreSQL/Redis/LHASA 持久化。正式常驻部署仍须在最终 commit 打包后单独验收。

正式发布包必须从最终 Git commit 构建，并令 manifest 中的 `sourceCommit`、`sourceTree` 与源码摘要保持一致。Git 日志作为 Release 独立附件导出，不提交静态副本。

## 交付文档

- `docs/data-sources-v1.md`：数据来源、刷新和降级口径。
- `docs/model-cards-v1.md`：确定性模型、AI 解释边界和版本。
- `docs/limitations-v1.md`：MVP 已知限制与禁止用途。
- `docs/release-v0.1.0.md`：首个面试评估版本说明。
- `PROGRESS.md`：全部阶段、提交目标和验证记录。
