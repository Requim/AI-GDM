# 风险预警 API v1

## 作用范围

P3.3 提供风险查询、快照详情和受控刷新接口。HTTP 适配器只负责路径、参数和错误映射；风险等级、数据时效、置信度和触发因素由 `internal/domain/risk` 的确定性引擎生成。

当前只注册 `landslide`（NASA Earthdata GIS LHASA）能力。`flood`、`debris_flow` 等未注册灾种会返回 `hazard_not_supported`，不得把滑坡结果改名为其他灾种。

## 路由

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/hazards/{hazardType}/risks/latest` | 返回该灾种最新的完整风险分析 |
| `GET` | `/api/v1/hazards/{hazardType}/risks/{snapshotID}` | 返回指定完整快照及风险区 |
| `POST` | `/api/v1/hazards/{hazardType}/refresh` | 触发该灾种一次刷新并返回结果 |

`hazardType` 使用小写字母、数字和下划线；快照标识只接受安全的 ASCII 标识。刷新请求复用后台调度器使用的同一 `HazardProvider`，同一灾种的人工与定时刷新会串行执行。

## 成功响应

```json
{
  "data": {
    "snapshot": {
      "id": "lhasa-...",
      "hazardType": "landslide",
      "status": "available",
      "source": {
        "provider": "NASA Earthdata GIS",
        "dataset": "LHASA Hazard Today",
        "fetchedAt": "2026-08-25T04:00:00Z",
        "validTo": "2026-08-25T16:00:00Z"
      }
    },
    "zones": [],
    "assessment": {
      "status": "available",
      "dataStatus": "current",
      "confidence": {"level": "high"},
      "decision": {"level": "low", "zoneCount": 0}
    }
  },
  "requestId": "..."
}
```

实际响应还会包含模型版本、阈值、输入引用、限制以及风险区的面积计算标记。聚合面积、人口、道路、POI 和行政区分析由独立空间分析用例维护，后续提供专用查询接口；本 API 不把缺失的聚合分析伪装成实时字段。`zones` 始终编码为空数组而不是 `null`。

## 错误与降级

| HTTP | `code` | 含义 |
| --- | --- | --- |
| `400` | `invalid_request` | 路径参数或应用输入不合法 |
| `404` | `hazard_not_supported` | 灾种尚未注册实时能力 |
| `404` | `risk_not_found` | 没有可读取的完整快照 |
| `503` | `provider_unavailable` | 外部数据源或 GDAL 暂不可用 |
| `503` | `insufficient_data` | 数据已过期或不足以形成研判 |
| `504` | `request_timeout` | 上游或处理超时 |

所有错误响应均包含 `requestId`，内部错误细节只写结构化日志，不返回给浏览器。数据源失败时只能返回明确的过期/不可用状态或最后一次满足时效边界的真实结果，不生成模拟数据。

## 扩展方式

新增灾种时实现专用 `HazardRefresher` 和 `RiskEvaluator`，创建 `HazardProvider` 后注册到 `Registry`。应用服务和 HTTP 路由不需要增加灾种分支；模型能力声明必须与快照的灾种、模型名和数据集完全匹配。
