# AI-GDM 地质灾害智能辅助决策系统

AI-GDM 是面向地质灾害监控中心的浏览器 Web 应用 MVP。系统使用 Go、PostgreSQL/PostGIS、Redis 和 GDAL，将公开近实时数据、确定性规则、地图服务与受约束的大模型解释组合为可审计的辅助决策流程。

本项目不是国家正式预警发布系统，不替代现场调查、交管指令、应急指挥或专业人员复核。

## 快速访问

- 本次评估环境（2026-08-31 已验证）：`http://124.220.67.163:8080/`
- 通用访问格式：`http://服务器地址:8080/`
- 存活探针：`GET /healthz`
- 就绪探针：`GET /readyz`
- Prometheus 指标：受管理员令牌保护的 `GET /metrics`

`/readyz` 只表示应用已进入服务期，不代表 Earthdata、高德、博查或 LLM 当前可用。

## 已实现能力

- 滑坡风险预警：采集 NASA Earthdata GIS 的 LHASA 近实时栅格，先按 WGS84 中国外接矩形下载，再按版本化 CHN ADM0 边界精确裁剪，经 GDAL 分级和矢量化后展示风险区、时效、处理范围与来源。
- 疏散调度：调用高德 Web 服务搜索候选避险设施并规划驾车、步行或公交路线，再由服务端排除穿越风险区的路线并排序。
- 损失评估：优先从权威空间暴露投影和已批准基线生成可解释人民币区间；正式基线缺失时，可对最高风险局部窗口中的道路暴露生成明确标记的研究参考区间。
- 生还评估：只对公开历史案例和合成匿名场景执行确定性回放，输出概率区间、搜救优先级、因素与人工复核要求。
- AI 研判：大模型只解释服务端固化的风险、路线、损失或历史回放结果，不能修改确定性等级、排名、金额或评分。
- 运行保障：展示数据源状态、最后成功时间、过期/降级状态、审计事件和 Prometheus 指标。

## 浏览器使用方式

### 1. 管理员授权

风险地图等只读页面可直接查看。设施搜索、路线规划、损失计算、历史回放、AI 报告和其他写操作需要管理员令牌。

1. 运维人员从服务器私有 `runtime.env` 中读取 `APP_ADMIN_TOKEN`，不要把令牌发送到聊天、工单或日志。
2. 打开页面顶部“管理员授权”，输入令牌后点击“授权”。
3. 令牌只保存在当前页面内存，刷新页面或收到 `401` 后需要重新输入；不会写入 URL、Cookie、`localStorage` 或 `sessionStorage`。

页面需要启用 JavaScript。若核心脚本加载失败，令牌输入框会保持禁用，避免把令牌提交到 URL 或普通表单。

当前默认入口是明文 HTTP。跨公网输入管理员令牌前，应先通过经过验证的同源 TLS 反向代理提供 HTTPS；不要把令牌直接用于不可信网络。

### 2. 查看风险地图

1. 打开“风险地图”，等待页面读取最新完整滑坡风险快照。
2. 查看主状态、数据状态、获取时间、有效期、模型版本和来源。
3. 分别查看“本次地图绘制数”“本快照范围内总数”和“处理范围”。地图绘制数最多为 3000，是浏览器负载上限，不是地理范围；总数才是该风险快照在处理边界内生成的风险区数量。
4. 点击风险区查看等级、面积和规则版本；页面会明确标记过期、回退、供应商不可用或因数量、顶点、响应大小上限被省略的区域。

当前处理范围使用第三方公开的版本化 CHN ADM0 边界，不随地图缩放和拖动变化，也不作为中国法定国界或官方地图依据。页面不会在实时数据不可用时生成模拟风险区。地图为空可能表示当前没有达到阈值的风险区，也可能表示风险区因安全上限未展示，应以主状态、范围和限制说明为准。

### 3. 搜索候选设施和规划疏散路线

1. 先完成管理员授权，并确认运行配置已启用高德服务。
2. 在“疏散调度”中点击“起点”或“终点”模式后点击地图，或手动填写 WGS84 经度和纬度。
3. 输入框中的灰色坐标只是示例占位符，不是实际值；必须点击地图或真正输入坐标，否则会提示“坐标不完整”。
4. 搜索设施时选择设施类型和半径，点击“搜索候选设施”。服务端调用高德 V5 周边搜索，并排除位于已知风险区内的候选点。
5. 可把安全候选设施设为终点，也可手动填写终点坐标。
6. 选择驾车、步行或公交后点击“规划候选路线”。公交模式还必须填写起点和终点的高德数字 `citycode`。
7. 查看保留路线、排除路线、风险命中、预计时间、距离和来源。

浏览器只提交 WGS84 坐标，服务端在高德适配器边界完成 GCJ-02 转换。高德返回的是候选设施和候选路线，不代表道路已由交管部门确认开放，也不证明交通工具或运力可用。

### 4. 运行损失评估

1. 在“智能评估”中选择“损失评估”。
2. 使用风险地图提供的受约束 `snapshotId`，或手动输入有效快照标识。
3. 点击计算后，服务端按快照读取同一空间分析、去重暴露投影和基线；浏览器不能自行填写人口、道路、设施数量或单价。
4. 查看低、中、高条件情景金额、影响范围、置信度、基线层级、来源和限制。

损失服务按以下顺序运行：

1. 在当前快照中按风险等级、面积和稳定 ID 选择最高风险种子，以其表面点构造 `0.05° × 0.05°` WGS84 局部窗口；最多裁剪 10 个相交风险区，避免全国快照一次加载数万区。
2. 使用 WorldPop、OpenStreetMap 和 geoBoundaries 在同一局部窗口生成去重人口、道路和设施暴露。
3. 当前风险和暴露投影有效，且数据库存在完整、已批准基线时，按正式确定性公式计算道路和设施直接物理损失，状态为 `available`。
4. 当前投影有效但正式基线明确不存在时，使用版本化的西藏吉隆藏布流域道路案例与历史 ECB 汇率生成道路研究参考区间，状态为 `reference_only`；数据库故障、超时或损坏不会被该降级掩盖。
5. 风险或暴露投影刚过期但不超过 72 小时时，只能读取最后一次成功投影并强制使用研究参考基线，结果保持 `reference_only`、`very_low`，置信度不高于 `0.24`；页面明确显示“最后成功数据研究参考区间”，不得把它当作实时结果。超过 72 小时后停止自动绑定并返回数据不足。

`reference_only` 结果会以琥珀状态显示；当前投影下置信度不高于 `0.49`，最后成功数据降级不高于 `0.24`。金额只覆盖局部窗口内的道路，人口和设施仅作暴露背景，不货币化，也不得解释为全国损失、法定灾损或现场核定金额。空间并集面积校验允许 1% 的投影/舍入容差。完整口径、公式和来源见 [局部热点道路损失参考方法](docs/loss-reference-v1.md)。

### 5. 运行历史生还回放

1. 选择“历史生还回放”。
2. 从公开案例目录选择案例，查看来源、事件限制、合成匿名场景和模型卡。
3. 点击“运行历史回放”，查看确定性辅助评分、概率宽区间、搜救优先级、因素和人工复核要求。

该功能只用于公开历史案例和合成匿名场景回放，不提供真实失联人员录入入口，也不能作为个体生还概率或现场搜救决策。

### 6. 生成 AI 解释报告

1. 先完成一项风险、路线、损失或历史回放的确定性计算。
2. 在“AI 解释报告”中选择服务端已保存的权威引用。
3. 点击“生成非权威解释”，查看确定性卡片、搜索证据、解释文本、来源和降级状态。

浏览器不能提交任意权威数值或自由搜索词。博查或 LLM 未配置、超时或返回坏数据时，系统保留确定性 Authority 并明确显示降级；LLM 不得修改风险等级、路线排名、金额或生还评分。

## 系统架构

```text
浏览器
  -> Go HTTP / 模板 / 少量 JavaScript
  -> 应用用例与确定性领域规则
  -> PostgreSQL + PostGIS / Redis / GDAL
  -> Earthdata / Open-Meteo / geoBoundaries / WorldPop / Overpass
  -> 高德 Web 服务 / 博查搜索 / OpenAI 兼容 LLM
```

领域层和应用层通过小粒度 ports 隔离数据库、地图和 AI 供应商。空间数据统一保存为 WGS84；GCJ-02 转换只发生在高德适配器边界。完整设计见 [架构说明](docs/architecture.md)。

## 部署前提

正式发布包面向 Linux AMD64 Docker 环境。部署主机需要：

- Docker Engine 和 Docker Compose v2，或 Windows Docker Desktop 的 Linux containers 模式。
- 建议至少 4 GiB 内存，并为发布归档、三张原始镜像和持久卷预留足够磁盘空间。
- 对外开放选定的 HTTP 端口，默认 TCP `8080`；云服务器还需配置安全组。
- 能访问互联网供应商。离线包只消除镜像仓库依赖，实时业务仍需要访问 Earthdata、Open-Meteo、WorldPop、Overpass、geoBoundaries、高德、博查或 LLM。
- Linux 部署需要 `sha256sum`、`curl`、`docker` 和 Compose v2。

PostgreSQL 和 Redis 只连接内部 Docker 网络，不映射宿主机端口。

## Linux 一键部署

### 1. 下载并校验发布包

从 [GitHub Release v0.1.0](https://github.com/Requim/AI-GDM/releases/tag/v0.1.0) 下载：

- `ai-gdm-v0.1.0-linux-amd64.tar.gz`
- `ai-gdm-v0.1.0-linux-amd64.tar.gz.sha256`

两个文件放在同一目录后执行：

```sh
sha256sum --strict -c ai-gdm-v0.1.0-linux-amd64.tar.gz.sha256
tar -xzf ai-gdm-v0.1.0-linux-amd64.tar.gz
cd ai-gdm-v0.1.0-linux-amd64
```

### 2. 无供应商 Key 的最简部署

```sh
sudo install -d -m 0700 /etc/ai-gdm
sudo env \
  AI_GDM_RUNTIME_ENV_FILE=/etc/ai-gdm/runtime.env \
  AI_GDM_PROJECT_NAME=ai-gdm \
  AI_GDM_BIND_ADDRESS=0.0.0.0 \
  AI_GDM_HTTP_PORT=8080 \
  REFRESH_ENABLED=true \
  sh deploy/deploy.sh
```

当 `/etc/ai-gdm/runtime.env` 不存在时，脚本会原子生成互不相同的 PostgreSQL、Redis 和管理员密钥，创建匹配的 `DATABASE_URL`，将文件权限设为 `0600`，加载三张原始镜像并启动服务。

该方式可运行风险采集、地图和历史回放基础链路，但高德、博查和 LLM 默认关闭。

### 3. 首次部署时启用高德、博查或 LLM

先创建仅 root 可读的供应商配置片段：

```sh
sudo install -d -m 0700 /etc/ai-gdm
sudo install -m 0600 /dev/null /etc/ai-gdm/providers.env
sudoedit /etc/ai-gdm/providers.env
```

按需填写以下内容；不使用的 Key 保持空值。高德必须使用控制台创建的“Web 服务”Key，普通 Web 服务 Key 不需要 `AMAP_JSCODE`。

```dotenv
AMAP_API_KEY=
BOCHA_API_KEY=
LLM_API_KEY=
LLM_BASE_URL=https://jojocode.com/v1/chat/completions
LLM_MODEL=gpt-5.6-terra
```

然后执行下面的命令，并把 `/opt/ai-gdm/current` 替换为解压后的发布包绝对路径：

```sh
sudo sh -c '
set -eu
cd /opt/ai-gdm/current
set -a
. /etc/ai-gdm/providers.env
set +a
AI_GDM_RUNTIME_ENV_FILE=/etc/ai-gdm/runtime.env \
AI_GDM_PROJECT_NAME=ai-gdm \
AI_GDM_BIND_ADDRESS=0.0.0.0 \
AI_GDM_HTTP_PORT=8080 \
REFRESH_ENABLED=true \
sh deploy/deploy.sh
'
```

首次生成配置时，非空 `AMAP_API_KEY`、`BOCHA_API_KEY` 和 `LLM_API_KEY` 会分别令 `AMAP_ENABLED`、`BOCHA_ENABLED` 和 `LLM_ENABLED` 自动写为 `true`。

### 4. 开放端口并验证

在腾讯云等平台的安全组中放行入站 TCP `8080`。如果主机启用了 UFW，还需按实际策略放行：

```sh
sudo ufw allow 8080/tcp
```

验证服务：

```sh
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS -o /dev/null http://服务器公网地址:8080/
```

## 修改已有部署配置

部署脚本只在 `AI_GDM_RUNTIME_ENV_FILE` 指向的文件不存在时生成配置。文件一旦存在，后续进程环境不会合并或覆盖它。

因此，首次部署后再增加 `AMAP_API_KEY` 不会自动生效。应直接编辑既有私有配置，并在发布包根目录重新运行脚本：

```sh
sudoedit /etc/ai-gdm/runtime.env
sudo chmod 0600 /etc/ai-gdm/runtime.env
sudo env AI_GDM_RUNTIME_ENV_FILE=/etc/ai-gdm/runtime.env sh deploy/deploy.sh
```

例如后续启用高德，需要同时设置：

```dotenv
AMAP_ENABLED=true
AMAP_API_KEY=
```

编辑器中应把 `AMAP_API_KEY` 的空值替换为私有 Web 服务 Key。不要在命令行参数、README、Git、聊天或日志中写入真实值。

注意：

- 不要随意修改已运行数据卷对应的 `POSTGRES_PASSWORD`、`REDIS_PASSWORD` 或 `DATABASE_URL`，否则现有数据库或缓存可能无法连接。
- `runtime.env` 每行使用 `KEY=value`，不要添加 `export`，不要在等号两侧加空格。
- 重复执行部署脚本会复用原配置和命名卷，不会重置密码或数据。
- 不要执行 `docker compose down -v`，除非明确要删除全部数据库、缓存和 LHASA 持久化数据。

## Windows Docker Desktop 部署

Windows 需要切换到 Linux containers，并在解压后的发布包根目录运行 PowerShell。供应商 Key 可通过交互输入进入当前进程，避免把值写进命令历史：

```powershell
$env:AI_GDM_RUNTIME_ENV_FILE = "$PWD\deploy\runtime.env"
$env:AI_GDM_PROJECT_NAME = 'ai-gdm'
$env:AI_GDM_BIND_ADDRESS = '0.0.0.0'
$env:AI_GDM_HTTP_PORT = '8080'
$env:REFRESH_ENABLED = 'true'

# 按需启用；不需要时不要执行对应行。
$amapKey = Read-Host '输入高德 Web 服务 Key'
$bochaKey = Read-Host '输入博查 API Key'
$llmKey = Read-Host '输入 LLM API Key'
$env:AMAP_API_KEY = $amapKey
$env:BOCHA_API_KEY = $bochaKey
$env:LLM_API_KEY = $llmKey
Remove-Variable amapKey, bochaKey, llmKey

powershell -ExecutionPolicy Bypass -File .\deploy\deploy.ps1 `
  -RuntimeEnvFile $env:AI_GDM_RUNTIME_ENV_FILE `
  -WaitSeconds 300
```

部署完成后可清理当前 PowerShell 进程中的 Key：

```powershell
Remove-Item Env:\AMAP_API_KEY -ErrorAction SilentlyContinue
Remove-Item Env:\BOCHA_API_KEY -ErrorAction SilentlyContinue
Remove-Item Env:\LLM_API_KEY -ErrorAction SilentlyContinue
```

PowerShell 脚本会对新运行配置收紧当前用户 ACL。Linux 空镜像缓存端到端已在腾讯 Ubuntu 验证；Windows Docker Desktop 已完成脚本、格式和配置合同检查，但未执行同等空缓存 E2E。

## 环境变量说明

### 部署控制变量

| 变量 | 默认值 | 是否需要设置 | 说明 |
| --- | --- | --- | --- |
| `AI_GDM_RUNTIME_ENV_FILE` | 发布包内 `deploy/runtime.env` | 推荐 | 私有运行配置绝对路径；推荐放在包目录外，例如 `/etc/ai-gdm/runtime.env`。相对路径按发布包根目录解析。 |
| `AI_GDM_PROJECT_NAME` | `ai-gdm` | 否 | Compose 项目名，只允许小写字母、数字、连字符和下划线。首次生成配置时写入。 |
| `AI_GDM_BIND_ADDRESS` | `0.0.0.0` | 否 | 宿主机监听地址；只允许本机访问时可设为 `127.0.0.1`。首次生成配置时写入。 |
| `AI_GDM_HTTP_PORT` | `8080` | 否 | 宿主机 HTTP 端口，范围 `1-65535`。首次生成配置时写入。 |
| `AI_GDM_DEPLOY_WAIT_SECONDS` | `300` | 否 | Linux 部署等待秒数，范围 `30-1800`；PowerShell 使用 `-WaitSeconds`。不写入运行配置。 |

### 核心运行变量

| 变量 | 默认值 | 是否需要设置 | 说明 |
| --- | --- | --- | --- |
| `POSTGRES_PASSWORD` | 首次部署随机生成 | 是 | PostgreSQL 密码。已有数据卷下不要随意修改。 |
| `REDIS_PASSWORD` | 首次部署随机生成 | 是 | Redis 密码。已有数据卷下不要随意修改。 |
| `DATABASE_URL` | 首次部署按 PostgreSQL 密码生成 | 是 | Compose 内部连接通常为 `postgresql://ai_gdm:...@postgres:5432/ai_gdm?sslmode=disable`。 |
| `APP_ENV` | 一键部署写入 `production` | 是 | 可选值为 `development`、`test`、`production`；生产环境必须有管理员令牌。 |
| `APP_LOG_LEVEL` | `info` | 否 | 结构化日志级别。 |
| `APP_SHUTDOWN_TIMEOUT` | `30s` | 否 | 优雅停机等待时间。 |
| `APP_ADMIN_TOKEN` | 首次部署随机生成 | 是 | 管理员 Bearer Token，必须为 32-256 字节的不可预测可见 ASCII，不能与其他密钥复用。 |
| `APP_RATE_LIMIT_PER_MINUTE` | `120` | 否 | 同一来源每分钟请求预算。 |
| `APP_RATE_LIMIT_BURST` | `30` | 否 | 突发预算，必须不大于每分钟预算。 |

`APP_HTTP_ADDR`、`REDIS_ADDR`、`REDIS_DB`、`LHASA_DATA_DIR`、`GDAL_BINARY` 和 `GDAL_TEMP_DIR` 已由 Compose 设置，正常一键部署不需要修改。

### 实时采集与时效变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `REFRESH_ENABLED` | `true`（一键部署） | 是否启动后台采集调度器。 |
| `REFRESH_INTERVAL` | `30m` | 调度检查间隔。 |
| `REFRESH_TIMEOUT` | `10m` | 单次刷新总超时，必须小于刷新间隔。 |
| `OPEN_METEO_POINTS` | 成都、昆明两个 `经度,纬度` 点 | 分号分隔的 WGS84 监测点，例如 `104.066500,30.572300;102.712300,25.040600`。 |
| `OPEN_METEO_PAST_HOURS` | `72` | 历史小时数。 |
| `OPEN_METEO_FORECAST_HOURS` | `24` | 预报小时数。 |
| `OPEN_METEO_FALLBACK_MAX_AGE` | `6h` | Open-Meteo 最后成功数据的最大回退时间。 |
| `OPEN_METEO_MAX_POINTS_PER_REQUEST` | `25` | 单次请求最大点数，代码上限为 25。 |
| `OPEN_METEO_BASE_URL` | `https://api.open-meteo.com/v1/forecast` | Open-Meteo HTTPS 端点。 |
| `OPEN_METEO_API_KEY` | 空 | 默认免费端点无需 Key；仅在所选端点要求时填写。 |
| `LHASA_STALE_AFTER` | `12h` | LHASA 组合修订超过该时间后标记过期。 |
| `LHASA_EARTHDATA_URL` | NASA Earthdata GIS `LHASA_Hazard_Today` | LHASA ArcGIS ImageServer 地址，通常不修改。 |

geoBoundaries、WorldPop 和 Overpass 使用服务内置的受控公开端点，不需要 API Key。其网络失败会被记录为数据不足或降级，不会用测试数据替代。

### 高德地图变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `AMAP_ENABLED` | `false` | 设为 `true` 才启用候选设施和路线供应商。 |
| `AMAP_API_KEY` | 空 | 必须是高德“Web 服务”Key，只保存在服务端。 |
| `AMAP_BASE_URL` | `https://restapi.amap.com` | 高德 Web 服务基地址。 |
| `AMAP_TIMEOUT` | `15s` | 单次高德调用超时。 |
| `AMAP_JSCODE` | 空 | 可选兼容安全密钥；普通 Web 服务 Key 可留空。 |

当前适配器调用高德 V5 `/place/around`、`/direction/driving`、`/direction/walking` 和 `/direction/transit/integrated`，不是地理/逆地理编码接口。Key 不会下发到浏览器。

### 博查搜索变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BOCHA_ENABLED` | `false` | 是否启用实时证据搜索。 |
| `BOCHA_API_KEY` | 空 | 博查服务端 Key。 |
| `BOCHA_BASE_URL` | `https://api.bocha.cn/v1/web-search` | 搜索 HTTPS 端点。 |
| `BOCHA_MAX_RESULTS` | `10` | 单次结果上限，范围 `1-50`。 |
| `BOCHA_MAX_AGE` | `72h` | 本地证据有效窗口。 |
| `BOCHA_TRUSTED_DOMAINS` | `gov.cn,mnr.gov.cn,mem.gov.cn,cma.cn,earthdata.nasa.gov` | 逗号分隔的可信来源基域。 |

### LLM 变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LLM_ENABLED` | `false` | 是否启用非权威解释报告。 |
| `LLM_PROVIDER_NAME` | 一键部署写入 `OpenAI-compatible` | 写入来源审计的供应商名称。 |
| `LLM_BASE_URL` | `https://jojocode.com/v1/chat/completions` | OpenAI 兼容聊天补全 HTTPS 端点。 |
| `LLM_API_KEY` | 空 | 只允许通过服务端私有环境注入。 |
| `LLM_MODEL` | `gpt-5.6-terra` | 模型名。 |
| `LLM_MAX_COMPLETION_TOKENS` | `1200` | 输出上限，范围 `1-4096`。 |
| `LLM_OUTPUT_ATTEMPTS` | `2` | 仅结构输出无效时的尝试次数，范围 `1-3`。 |

## 从源码设置环境变量

Go 服务只读取进程环境，不会自动加载 `.env`。开发环境可复制空模板到 Git 忽略文件，再由 shell 或 Compose 加载：

```sh
cp deploy/runtime.env.example deploy/runtime.env
chmod 0600 deploy/runtime.env
```

复制后的文件仍需填写全部必填项；由于文件已经存在，部署脚本不会自动补齐其中的空值。

Linux Bash 临时设置敏感值时可使用隐藏输入，避免值进入命令历史：

```bash
read -r -s -p '管理员令牌: ' SECRET_VALUE
printf '\n'
export APP_ADMIN_TOKEN="$SECRET_VALUE"
unset SECRET_VALUE
export APP_ENV=development
```

PowerShell 当前进程设置方式：

```powershell
$secretValue = Read-Host '输入管理员令牌'
$env:APP_ADMIN_TOKEN = $secretValue
Remove-Variable secretValue
$env:APP_ENV = 'development'
```

源码容器启动示例见 [容器部署说明](docs/deployment-v1.md)。容器、PostGIS、Redis、完整构建和浏览器验收应在指定腾讯 Ubuntu 环境执行。

## 更新、停止与数据保留

- 升级时解压新发布包，继续使用同一个绝对 `AI_GDM_RUNTIME_ENV_FILE` 和 `AI_GDM_PROJECT_NAME`，再运行新包的部署脚本。
- 查看运行状态：在发布包根目录按相同项目名、运行配置和两个 Compose 文件执行 `docker compose ps`。
- 停止但保留数据：使用相同参数执行 `docker compose down`。
- 删除全部数据：只有明确确认后才执行 `docker compose down -v`；该操作会删除 PostgreSQL、Redis 和 LHASA 命名卷。
- 单节点 Compose 当前没有自动备份、高可用或迁移回滚，正式长期使用前需要单独补齐。

完整停止命令示例：

```sh
sudo env AI_GDM_RUNTIME_ENV_FILE=/etc/ai-gdm/runtime.env \
docker compose \
  --project-name ai-gdm \
  --project-directory "$PWD" \
  --env-file /etc/ai-gdm/runtime.env \
  --env-file deploy/release-images.env \
  -f compose.yaml \
  -f deploy/compose.offline.yaml \
  down
```

## 开发与验证

```sh
go test ./...
go vet ./...
go build ./...
```

主要容器门禁：

```sh
sudo sh scripts/validate-go.sh
sudo sh scripts/validate-package.sh
sudo sh scripts/validate-deploy.sh
```

正式交付验证覆盖系统清单 `16/16`、风险地图 Chromium `26/26`、疏散 Chromium `42/42`、评估 Chromium `126/126`，并在空镜像缓存的 Docker-in-Docker 中验证三个镜像加载、重复部署和 PostgreSQL/Redis/LHASA 持久化。

正式发布包必须从最终 Git commit 构建，并令 manifest 中的 `sourceCommit`、`sourceTree` 与源码摘要保持一致。Git 日志作为 Release 独立附件导出，不提交静态副本。

## 交付文档

- [数据来源与降级口径](docs/data-sources-v1.md)
- [确定性模型和 AI 边界](docs/model-cards-v1.md)
- [MVP 已知限制](docs/limitations-v1.md)
- [容器部署说明](docs/deployment-v1.md)
- [发布包说明](docs/package-v1.md)
- [v0.1.0 发布说明](docs/release-v0.1.0.md)
- [开发进度账本](PROGRESS.md)
