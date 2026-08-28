package hazardapi

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	applicationhazard "github.com/Requim/AI-GDM/internal/application/hazard"
	domainhazard "github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/risk"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestLatestMapSortsLimitsAndReportsTotal(t *testing.T) {
	zones := make([]domainhazard.RiskZone, 3005)
	for index := range zones {
		zones[index] = mapZone(index, domainhazard.RiskLow, simplePolygon())
	}
	zones[len(zones)-1] = mapZone(9999, domainhazard.RiskVeryHigh, simplePolygon())
	result := mapRiskFixture(zones)
	service := &riskServiceStub{mapResult: &result}
	response := request(t, service, http.MethodGet, "/hazards/landslide/risks/latest/map")

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.operation != "latest-map" || service.mapMaxZones != maxMapSourceZones {
		t.Fatalf("地图接口未使用专用有界读取: %+v", service)
	}
	var payload mapSuccessResponse
	decodeResponse(t, response, &payload)
	if payload.Data.TotalZoneCount != 3005 || payload.Data.VisibleZoneCount != maxMapZones ||
		payload.Data.OmittedZoneCount != 5 ||
		payload.Data.OmittedComplexZoneCount+payload.Data.OmittedPayloadZoneCount != payload.Data.OmittedZoneCount {
		t.Fatalf("地图计数无效: %+v", payload.Data)
	}
	if payload.Data.Zones[0].Level != domainhazard.RiskVeryHigh {
		t.Fatalf("风险区未按等级排序: %+v", payload.Data.Zones[0])
	}
	if response.Header().Get("X-AI-GDM-Map-Zone-Total") != "3005" ||
		response.Header().Get("X-AI-GDM-Map-Zone-Visible") != "3000" {
		t.Fatalf("地图计数响应头无效: %v", response.Header())
	}
}

func TestLatestMapRejectsInvalidAreaGeometry(t *testing.T) {
	invalid := json.RawMessage(`[[[100,30],[101,30],[101,31],[100,31]]]`)
	result := mapRiskFixture([]domainhazard.RiskZone{mapZone(1, domainhazard.RiskHigh, invalid)})
	response := request(t, &riskServiceStub{mapResult: &result},
		http.MethodGet, "/hazards/landslide/risks/latest/map")
	assertAPIError(t, response, http.StatusServiceUnavailable, "insufficient_data")
}

func TestLatestMapOmitsComplexGeometry(t *testing.T) {
	zones := []domainhazard.RiskZone{
		mapZone(1, domainhazard.RiskVeryHigh, polygonWithVertices(maxMapZoneVertices+1)),
		mapZone(2, domainhazard.RiskHigh, simplePolygon()),
	}
	result := mapRiskFixture(zones)
	response := request(t, &riskServiceStub{mapResult: &result},
		http.MethodGet, "/hazards/landslide/risks/latest/map")

	var payload mapSuccessResponse
	decodeResponse(t, response, &payload)
	if response.Code != http.StatusOK || payload.Data.VisibleZoneCount != 1 ||
		payload.Data.OmittedComplexZoneCount != 1 || payload.Data.Zones[0].ID != "zone-2" {
		t.Fatalf("复杂几何限制无效: status=%d data=%+v", response.Code, payload.Data)
	}
}

func TestLatestMapReportsWhenAllRiskZonesAreOmitted(t *testing.T) {
	result := mapRiskFixture([]domainhazard.RiskZone{
		mapZone(1, domainhazard.RiskVeryHigh, polygonWithVertices(maxMapZoneVertices+1)),
	})
	response := request(t, &riskServiceStub{mapResult: &result},
		http.MethodGet, "/hazards/landslide/risks/latest/map")

	var payload mapSuccessResponse
	decodeResponse(t, response, &payload)
	if response.Code != http.StatusOK || payload.Data.TotalZoneCount != 1 ||
		payload.Data.VisibleZoneCount != 0 || payload.Data.OmittedZoneCount != 1 ||
		payload.Data.OmittedComplexZoneCount != 1 || len(payload.Data.Zones) != 0 {
		t.Fatalf("全省略地图合同无效: status=%d data=%+v", response.Code, payload.Data)
	}
	if !strings.Contains(strings.Join(payload.Data.Limitations, " "), "省略不表示风险为零") {
		t.Fatalf("全省略地图合同缺少风险提示: %+v", payload.Data.Limitations)
	}
}

func TestLatestMapBoundsEncodedPayload(t *testing.T) {
	zones := make([]domainhazard.RiskZone, 100)
	largeText := strings.Repeat("x", 100<<10)
	for index := range zones {
		zones[index] = mapZone(index, domainhazard.RiskHigh, simplePolygon())
		zones[index].Limitations = []string{largeText}
	}
	result := mapRiskFixture(zones)
	response := request(t, &riskServiceStub{mapResult: &result},
		http.MethodGet, "/hazards/landslide/risks/latest/map")

	responseBytes := response.Body.Len()
	var payload mapSuccessResponse
	decodeResponse(t, response, &payload)
	if response.Code != http.StatusOK || responseBytes > maxMapResponseBytes ||
		payload.Data.VisibleZoneCount >= len(zones) || payload.Data.OmittedPayloadZoneCount == 0 {
		t.Fatalf("地图响应字节限制无效: status=%d bytes=%d data=%+v",
			response.Code, responseBytes, payload.Data)
	}
}

func TestLatestMapRejectsMissingValidity(t *testing.T) {
	result := mapRiskFixture([]domainhazard.RiskZone{mapZone(1, domainhazard.RiskHigh, simplePolygon())})
	result.Snapshot.ValidTo = time.Time{}
	response := request(t, &riskServiceStub{mapResult: &result},
		http.MethodGet, "/hazards/landslide/risks/latest/map")
	assertAPIError(t, response, http.StatusServiceUnavailable, "insufficient_data")
}

func TestProjectMapRiskRejectsSourceZoneOverflow(t *testing.T) {
	result := mapRiskFixture(make([]domainhazard.RiskZone, maxMapSourceZones+1))
	if _, err := projectMapRisk(result); err == nil {
		t.Fatal("projectMapRisk() 未拒绝超出源风险区总数上限的数据")
	}
}

func mapRiskFixture(zones []domainhazard.RiskZone) applicationhazard.MapRiskResult {
	validTo := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	return applicationhazard.MapRiskResult{
		RiskResult: applicationhazard.RiskResult{
			Snapshot: domainhazard.Snapshot{ID: "snapshot-map", HazardType: domainhazard.TypeLandslide,
				ValidTo: validTo, Status: domainhazard.SnapshotAvailable},
			Zones: zones,
			Assessment: risk.Assessment{ID: "risk-map", SnapshotID: "snapshot-map",
				DataStatus: risk.DataCurrent, Status: risk.AssessmentAvailable},
		},
		TotalZoneCount: len(zones),
	}
}

func mapZone(index int, level domainhazard.RiskLevel, coordinates json.RawMessage) domainhazard.RiskZone {
	return domainhazard.RiskZone{ID: "zone-" + strconv.Itoa(index), SnapshotID: "snapshot-map",
		Geometry: spatial.Geometry{Type: "Polygon", Coordinates: coordinates},
		Level:    level, Minimum: .5, Mean: .7, Maximum: .9}
}

func simplePolygon() json.RawMessage {
	return json.RawMessage(`[[[100,30],[101,30],[101,31],[100,30]]]`)
}

func polygonWithVertices(count int) json.RawMessage {
	ring := make([][]float64, count)
	for index := 0; index < count-1; index++ {
		angle := 2 * math.Pi * float64(index) / float64(count-1)
		ring[index] = []float64{105 + math.Cos(angle), 30 + math.Sin(angle)}
	}
	ring[count-1] = append([]float64(nil), ring[0]...)
	payload, _ := json.Marshal([][][]float64{ring})
	return payload
}
