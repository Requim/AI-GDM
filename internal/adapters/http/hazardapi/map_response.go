package hazardapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	applicationhazard "github.com/Requim/AI-GDM/internal/application/hazard"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

const (
	maxMapZones            = 3000
	maxMapSourceZones      = 100000
	maxMapZoneVertices     = 5000
	maxMapTotalVertices    = 200000
	maxMapGeometryBytes    = 512 << 10
	maxMapZonePayloadBytes = 6 << 20
	maxMapResponseBytes    = 8 << 20
)

type mapRiskResult struct {
	Snapshot                hazard.Snapshot   `json:"snapshot"`
	Zones                   []hazard.RiskZone `json:"zones"`
	Assessment              risk.Assessment   `json:"assessment"`
	TotalZoneCount          int               `json:"totalZoneCount"`
	VisibleZoneCount        int               `json:"visibleZoneCount"`
	OmittedZoneCount        int               `json:"omittedZoneCount"`
	OmittedComplexZoneCount int               `json:"omittedComplexZoneCount"`
	OmittedPayloadZoneCount int               `json:"omittedPayloadZoneCount"`
	Limits                  mapResponseLimits `json:"limits"`
	Limitations             []string          `json:"mapLimitations"`
}

type mapResponseLimits struct {
	MaxZones         int `json:"maxZones"`
	MaxSourceZones   int `json:"maxSourceZones"`
	MaxZoneVertices  int `json:"maxZoneVertices"`
	MaxTotalVertices int `json:"maxTotalVertices"`
	MaxGeometryBytes int `json:"maxGeometryBytes"`
	MaxResponseBytes int `json:"maxResponseBytes"`
}

type mapSuccessResponse struct {
	Data      mapRiskResult `json:"data"`
	RequestID string        `json:"requestId"`
}

func (h *Handler) writeMapResult(w http.ResponseWriter, r *http.Request,
	result applicationhazard.RiskResult, resultErr error,
) {
	if resultErr != nil {
		h.writeError(w, r, resultErr)
		return
	}
	value, err := projectMapRisk(result)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	payload, err := json.Marshal(mapSuccessResponse{Data: value, RequestID: requestID(r)})
	if err != nil || len(payload) > maxMapResponseBytes {
		h.writeError(w, r, fmt.Errorf("%w: 地图风险响应超过安全上限", domain.ErrInsufficientData))
		return
	}
	writeMapPayload(w, r, payload, value)
}

func projectMapRisk(result applicationhazard.RiskResult) (mapRiskResult, error) {
	if result.Snapshot.ValidTo.IsZero() {
		return mapRiskResult{}, fmt.Errorf("%w: 风险快照缺少有效期", domain.ErrInsufficientData)
	}
	if len(result.Zones) > maxMapSourceZones {
		return mapRiskResult{}, fmt.Errorf("%w: 风险区总数超过地图投影上限", domain.ErrInsufficientData)
	}
	zones := append([]hazard.RiskZone(nil), result.Zones...)
	sort.SliceStable(zones, func(left, right int) bool { return higherPriority(zones[left], zones[right]) })
	selected, complexCount, payloadCount, err := selectMapZones(zones)
	if err != nil {
		return mapRiskResult{}, err
	}
	value := mapRiskResult{Snapshot: result.Snapshot, Zones: selected, Assessment: result.Assessment,
		TotalZoneCount: len(zones), VisibleZoneCount: len(selected),
		OmittedZoneCount: len(zones) - len(selected), OmittedComplexZoneCount: complexCount,
		OmittedPayloadZoneCount: payloadCount, Limits: defaultMapLimits()}
	value.Limitations = mapLimitations(value)
	return value, nil
}

func selectMapZones(zones []hazard.RiskZone) ([]hazard.RiskZone, int, int, error) {
	selected := make([]hazard.RiskZone, 0, min(len(zones), maxMapZones))
	vertices, payloadBytes, complexCount, payloadCount := 0, 0, 0, 0
	for index, zone := range zones {
		if len(selected) >= maxMapZones || maxMapTotalVertices-vertices < 4 {
			payloadCount += len(zones) - index
			break
		}
		if len(zone.Geometry.Coordinates) > maxMapGeometryBytes {
			complexCount++
			continue
		}
		if err := zone.Geometry.ValidateArea(); err != nil {
			return nil, 0, 0, fmt.Errorf("%w: 风险区 %s 几何无效", domain.ErrInsufficientData, zone.ID)
		}
		count, err := geometryVertexCount(zone.Geometry)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("%w: 风险区 %s 几何无效", domain.ErrInsufficientData, zone.ID)
		}
		if count > maxMapZoneVertices || vertices+count > maxMapTotalVertices {
			complexCount++
			continue
		}
		encoded, err := json.Marshal(zone)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("编码地图风险区 %s: %w", zone.ID, err)
		}
		if payloadBytes+len(encoded) > maxMapZonePayloadBytes {
			payloadCount++
			continue
		}
		selected = append(selected, zone)
		vertices += count
		payloadBytes += len(encoded)
	}
	return selected, complexCount, payloadCount, nil
}

func geometryVertexCount(geometry spatial.Geometry) (int, error) {
	switch geometry.Type {
	case "Polygon":
		var polygons [][][]float64
		if err := json.Unmarshal(geometry.Coordinates, &polygons); err != nil {
			return 0, err
		}
		return polygonVertexCount(polygons), nil
	case "MultiPolygon":
		var polygons [][][][]float64
		if err := json.Unmarshal(geometry.Coordinates, &polygons); err != nil {
			return 0, err
		}
		count := 0
		for _, polygon := range polygons {
			count += polygonVertexCount(polygon)
		}
		return count, nil
	default:
		return 0, fmt.Errorf("不支持的地图几何 %q", geometry.Type)
	}
}

func polygonVertexCount(polygon [][][]float64) int {
	count := 0
	for _, ring := range polygon {
		count += len(ring)
	}
	return count
}

func higherPriority(left, right hazard.RiskZone) bool {
	if riskRank(left.Level) != riskRank(right.Level) {
		return riskRank(left.Level) > riskRank(right.Level)
	}
	if left.Maximum != right.Maximum {
		return left.Maximum > right.Maximum
	}
	if left.Mean != right.Mean {
		return left.Mean > right.Mean
	}
	return left.ID < right.ID
}

func riskRank(level hazard.RiskLevel) int {
	switch level {
	case hazard.RiskVeryHigh:
		return 4
	case hazard.RiskHigh:
		return 3
	case hazard.RiskModerate:
		return 2
	case hazard.RiskLow:
		return 1
	default:
		return 0
	}
}

func defaultMapLimits() mapResponseLimits {
	return mapResponseLimits{MaxZones: maxMapZones, MaxSourceZones: maxMapSourceZones,
		MaxZoneVertices: maxMapZoneVertices, MaxTotalVertices: maxMapTotalVertices,
		MaxGeometryBytes: maxMapGeometryBytes, MaxResponseBytes: maxMapResponseBytes}
}

func mapLimitations(value mapRiskResult) []string {
	limitations := []string{"地图响应只用于浏览器辅助研判，完整风险记录请查询审计接口"}
	if value.OmittedZoneCount > 0 {
		limitations = append(limitations, "地图已按风险优先级省略部分风险区，省略不表示风险为零")
	}
	if value.OmittedComplexZoneCount > 0 {
		limitations = append(limitations, "部分几何超过浏览器复杂度上限，未进入地图响应")
	}
	return limitations
}

func writeMapPayload(w http.ResponseWriter, r *http.Request, payload []byte, value mapRiskResult) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-AI-GDM-Map-Zone-Total", strconv.Itoa(value.TotalZoneCount))
	w.Header().Set("X-AI-GDM-Map-Zone-Visible", strconv.Itoa(value.VisibleZoneCount))
	if id := requestID(r); id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}
