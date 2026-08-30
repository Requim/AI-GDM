# 地图服务集成 v1

## 配置

高德服务端代理默认关闭。启用时只从服务端环境变量读取配置：

| 环境变量 | 必填 | 说明 |
| --- | --- | --- |
| `AMAP_ENABLED` | 否 | 是否创建高德适配器，默认 `false` |
| `AMAP_BASE_URL` | 否 | 高德 Web 服务基地址，默认 `https://restapi.amap.com` |
| `AMAP_API_KEY` | 启用时是 | 高德 Web 服务 `key`，不写入日志或响应 |
| `AMAP_JSCODE` | 否 | 可选兼容安全密钥；配置时作为 `jscode` 注入请求，普通 Web服务 Key 可留空 |
| `AMAP_TIMEOUT` | 否 | 单次请求超时，默认 `15s` |

`cmd/server` 只负责创建受控 HTTP 客户端、风险仓储和应用服务并注入密钥。候选设施由 `application/evacuation` 读取最新完整风险分析后筛选，驾车和步行路线依赖 `ports.RoutePlanner`，公交路线依赖独立的 `ports.TransitRoutePlanner`。客户端请求不能覆盖 `key`、可选 `jscode` 或供应商地址。

## 服务端代理接口

地图代理挂载在 `/api/v1/map`，浏览器只提交领域层的 WGS84 坐标，不提交高德密钥：

| 方法 | 路径 | 请求字段 | 说明 |
| --- | --- | --- | --- |
| `POST` | `/api/v1/map/places/nearby` | `hazardType`、`center`、`kind`、`radiusMeters` | 搜索并过滤避难场所、医院或交通设施 |
| `POST` | `/api/v1/map/routes` | `hazardType`、`origin`、`destination`、`mode`；公交另需 `originCity`、`destinationCity` | 获取已执行风险区过滤和安全排序的路线 |

设施搜索的 `hazardType` 可省略，当前默认且只支持 `landslide`；其他灾种返回 `hazard_not_supported`，不会复用滑坡风险区冒充其他灾种。响应包含使用的风险快照、安全候选、被排除设施、命中的风险区标识和 `candidateCount/allowedCount/excludedCount` 统计。

风险快照不存在、不可用或风险区集合不完整时，设施接口返回 `503 insufficient_data`，不得绕过筛选返回高德原始候选。完整分析明确包含零个风险区时才允许全部候选通过。

路线接口返回 `evacuation.RouteSearchResult`：`routes` 是可调度候选，`excluded` 保留被风险区排除的路线和命中的风险区标识。驾车和步行调用高德对应 V5 路径，公交调用 `/v5/direction/transit/integrated`，并要求起终点为数字 citycode；代理会把城市字段交给独立的公交端口，不伪造公交结果。路线来源、请求 ID、响应哈希、分段说明和 WGS84 折线会保留在领域结果中。候选按风险分数、预计时长、距离和路线标识稳定排序并写入 `rank`；没有完整风险区数据时返回 `503 insufficient_data`，不返回未经筛选的候选。

路线相交判定使用 WGS84 LineString 与 Polygon/MultiPolygon 的确定性几何计算：端点落入风险区、折线穿越边界或贴着边界均排除；洞环内部视为非风险区但洞环边界仍按风险处理。路线的 `riskScore` 仅使用供应商已提供的 0-100 分数；未提供时保持 0 并明确返回限制说明。该判定仅覆盖已知风险区，不等同于道路实时封闭或交管部门确认。

## 坐标边界

领域模型、PostGIS 和应用端口统一使用 WGS84。地图适配器调用高德前把中国境内坐标转换为 GCJ-02，解析响应后立即转换回 WGS84；境外坐标不执行偏移并保留限制说明。GCJ-02 不得出现在领域实体、数据库字段或浏览器接口合同中。

高德路线是候选路线，不能代表道路已开放，也不能直接作为灾害预警结论。P4.2 已完成 POI 点位风险区过滤，P4.4 完成路线几何过滤和安全排序。
