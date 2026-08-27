# 地图服务集成 v1

## 配置

高德服务端代理默认关闭。启用时只从服务端环境变量读取配置：

| 环境变量 | 必填 | 说明 |
| --- | --- | --- |
| `AMAP_ENABLED` | 否 | 是否创建高德适配器，默认 `false` |
| `AMAP_BASE_URL` | 否 | 高德 Web 服务基地址，默认 `https://restapi.amap.com` |
| `AMAP_API_KEY` | 启用时是 | 高德 Web 服务 `key`，不写入日志或响应 |
| `AMAP_JSCODE` | 启用时是 | 高德服务端安全密钥，作为 `jscode` 注入请求 |
| `AMAP_TIMEOUT` | 否 | 单次请求超时，默认 `15s` |

`cmd/server` 只负责创建受控 HTTP 客户端和注入密钥，后续应用用例依赖 `ports.PlaceFinder` 或 `ports.RoutePlanner`。客户端请求不能覆盖 `key`、`jscode` 或供应商地址。

## 服务端代理接口

地图代理挂载在 `/api/v1/map`，浏览器只提交领域层的 WGS84 坐标，不提交高德密钥：

| 方法 | 路径 | 请求字段 | 说明 |
| --- | --- | --- | --- |
| `POST` | `/api/v1/map/places/nearby` | `center`、`kind`、`radiusMeters` | 搜索避难场所、医院或交通设施 |
| `POST` | `/api/v1/map/routes` | `origin`、`destination`、`mode` | 获取驾车或步行候选路线 |

接口返回标准化的 `evacuation.Facility` 或 `evacuation.Route`，供应商错误只返回通用不可用状态。公交路线需要城市参数，当前代理明确返回参数错误，不伪造公交结果；风险区过滤和路线排序由后续疏散用例负责。

## 坐标边界

领域模型、PostGIS 和应用端口统一使用 WGS84。地图适配器调用高德前把中国境内坐标转换为 GCJ-02，解析响应后立即转换回 WGS84；境外坐标不执行偏移并保留限制说明。GCJ-02 不得出现在领域实体、数据库字段或浏览器接口合同中。

高德路线是候选路线，不能代表道路已开放，也不能直接作为灾害预警结论。POI 和路线的风险区过滤分别由 P4.2-P4.4 完成。
