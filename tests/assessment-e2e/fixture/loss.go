package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/http/lossapi"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
)

const lossSnapshotID = "snapshot-e2e-20260828"

type fixtureLossEstimator struct{ scenarios *scenarioStore }

type fixtureLossClock struct{ now time.Time }

func (c fixtureLossClock) Now() time.Time { return c.now }

type fixtureLossProjectionReader struct {
	value applicationloss.LossInputProjection
}

func (r fixtureLossProjectionReader) ReadLossInput(_ context.Context, snapshotID string,
	_ time.Time, _ applicationloss.RiskProjectionLimits,
) (applicationloss.LossInputProjection, error) {
	if snapshotID != r.value.Snapshot.ID {
		return applicationloss.LossInputProjection{}, fmt.Errorf("%w: 风险快照不存在", domain.ErrNotFound)
	}
	return r.value, nil
}

type fixtureBaselineReader struct{ value lossdomain.BaselineSet }

func (r fixtureBaselineReader) BaselineSet(_ context.Context,
	_ applicationloss.BaselineQuery,
) (lossdomain.BaselineSet, error) {
	return r.value, nil
}

func (e *fixtureLossEstimator) Estimate(ctx context.Context,
	input applicationloss.EstimateInput,
) (lossdomain.Assessment, error) {
	name, now := e.scenarios.currentScenario(), fixtureServiceNow()
	projection, err := fixtureLossProjection(now, name)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	service, err := applicationloss.NewService(
		fixtureLossProjectionReader{value: projection},
		fixtureBaselineReader{value: fixtureBaselineSet(now, name)}, fixtureLossClock{now: now},
	)
	if err != nil {
		return lossdomain.Assessment{}, err
	}
	value, err := service.Estimate(ctx, input)
	if err != nil || name != "loss_many_limitations" {
		return value, err
	}
	for index := 1; index <= 40; index++ {
		value.Limitations = append(value.Limitations, fmt.Sprintf("补充审计限制 %02d", index))
	}
	sort.Strings(value.Limitations)
	return lossdomain.BindAssessmentIdentity(value)
}

type fixtureLossStore struct {
	mu     sync.Mutex
	values map[string]lossdomain.Assessment
}

func newFixtureLossStore() *fixtureLossStore {
	return &fixtureLossStore{values: map[string]lossdomain.Assessment{}}
}

func (s *fixtureLossStore) SaveAssessment(_ context.Context, value lossdomain.Assessment) error {
	s.mu.Lock()
	s.values[value.ID] = value
	s.mu.Unlock()
	return nil
}

func (s *fixtureLossStore) GetAssessment(_ context.Context, id string) (lossdomain.Assessment, error) {
	s.mu.Lock()
	value, exists := s.values[id]
	s.mu.Unlock()
	if !exists {
		return lossdomain.Assessment{}, domain.ErrNotFound
	}
	return value, nil
}

func newLossHandler(scenarios *scenarioStore, logger *slog.Logger) (
	http.Handler, *fixtureLossStore, error,
) {
	store := newFixtureLossStore()
	handler, err := lossapi.New(&fixtureLossEstimator{scenarios: scenarios}, store, store, "/api/v1/loss", logger)
	if err != nil {
		return nil, nil, fmt.Errorf("创建真实损失评估 HTTP fixture: %w", err)
	}
	return handler, store, nil
}

func (s *scenarioStore) useLossHandler(handler http.Handler, store *fixtureLossStore) {
	s.mu.Lock()
	s.lossHandler, s.lossStore = handler, store
	s.mu.Unlock()
}

func (s *scenarioStore) currentScenario() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

func (s *scenarioStore) serveLoss(w http.ResponseWriter, r *http.Request) {
	operation := lossOperation(r)
	name, call := s.next(operation)
	if operation == "loss_post" {
		payload, err := s.record(operation, r)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求参数无效")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(payload))
	}
	if handleLossTransportScenario(w, r, name, operation, call) {
		return
	}
	s.serveRealLoss(w, r, name, operation)
}

func lossOperation(r *http.Request) string {
	if r.Method == http.MethodPost && r.URL.Path == "/assessments" {
		return "loss_post"
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sources") {
		return "loss_sources"
	}
	return "loss_get"
}

func handleLossTransportScenario(w http.ResponseWriter, r *http.Request, name, operation string, call int) bool {
	if (name == "loss_get_503" && operation == "loss_get") ||
		(name == "loss_sources_503" && operation == "loss_sources") {
		writeAPIError(w, http.StatusServiceUnavailable, "provider_unavailable", "损失评估依赖暂时不可用")
		return true
	}
	if lossOversizedScenario(name, operation) {
		writeOversizedResponse(w, strings.Contains(name, "chunked"))
		return true
	}
	if operation != "loss_post" {
		return false
	}
	switch {
	case name == "loss_success_then_503" && call > 1:
		writeAPIError(w, http.StatusServiceUnavailable, "provider_unavailable", "损失评估依赖暂时不可用")
	case name == "loss_timeout":
		<-r.Context().Done()
	case name == "loss_delayed":
		return !waitForRequest(r, 800*time.Millisecond)
	default:
		return false
	}
	return true
}

func lossOversizedScenario(name, operation string) bool {
	wanted := map[string]string{
		"loss_content_length_oversized":         "loss_post",
		"loss_chunked_oversized":                "loss_post",
		"loss_get_content_length_oversized":     "loss_get",
		"loss_get_chunked_oversized":            "loss_get",
		"loss_sources_content_length_oversized": "loss_sources",
		"loss_sources_chunked_oversized":        "loss_sources",
	}
	return wanted[name] == operation
}

func (s *scenarioStore) serveRealLoss(w http.ResponseWriter, r *http.Request, name, operation string) {
	s.mu.Lock()
	handler := s.lossHandler
	s.mu.Unlock()
	if handler == nil {
		writeAPIError(w, http.StatusInternalServerError, "fixture_unavailable", "损失 fixture 未初始化")
		return
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	mutateLossResponse(name, operation, recorder)
	copyRecordedResponse(w, recorder)
}

func mutateLossResponse(name, operation string, recorder *httptest.ResponseRecorder) {
	if operation == "loss_post" {
		mutateLossLocation(name, recorder)
		return
	}
	if recorder.Code != http.StatusOK {
		return
	}
	if operation == "loss_sources" {
		mutateLossAuditResponse(name, recorder)
		return
	}
	if operation != "loss_get" {
		return
	}
	if name == "loss_bad_wire" {
		recorder.Body = bytes.NewBuffer(append(
			[]byte(`{"data":{"status":"available"},"requestId":"fixture"}`), '\n',
		))
		return
	}
	payload, err := mutatedLossPayload(name, recorder.Body.Bytes())
	if err == nil && payload != nil {
		recorder.Body = bytes.NewBuffer(payload)
	}
}

func mutateLossAuditResponse(name string, recorder *httptest.ResponseRecorder) {
	if name != "loss_audit_admin_boundary_mismatch" &&
		name != "loss_audit_projection_limitations_mismatch" {
		return
	}
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		return
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return
	}
	if name == "loss_audit_admin_boundary_mismatch" {
		data["adminBoundaryId"] = "CHN-ADM0-tampered"
	} else {
		data["projectionLimitations"] = []any{"审计侧伪造的投影限制"}
	}
	if payload, err := json.Marshal(envelope); err == nil {
		recorder.Body = bytes.NewBuffer(payload)
	}
}

func mutateLossLocation(name string, recorder *httptest.ResponseRecorder) {
	location := recorder.Header().Get("Location")
	switch name {
	case "loss_location_missing":
		recorder.Header().Del("Location")
	case "loss_location_wrong_id":
		recorder.Header().Set("Location", strings.Replace(location, recorderLocationID(location),
			"loss-"+strings.Repeat("0", 64), 1))
	case "loss_location_query":
		recorder.Header().Set("Location", location+"?download=1")
	case "loss_location_hash":
		recorder.Header().Set("Location", location+"#fragment")
	case "loss_location_extra_path":
		recorder.Header().Set("Location", location+"/extra")
	case "loss_location_encoded":
		recorder.Header().Set("Location", strings.Replace(location, "/assessments/", "/assessments/%2F", 1))
	case "loss_location_cross_origin":
		recorder.Header().Set("Location", "https://evil.example.invalid"+location)
	}
}

func recorderLocationID(location string) string {
	index := strings.LastIndex(location, "/")
	if index < 0 {
		return ""
	}
	return location[index+1:]
}

func mutatedLossPayload(name string, payload []byte) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok || !applyLossMutation(name, data) {
		return nil, nil
	}
	return json.Marshal(envelope)
}

func applyLossMutation(name string, data map[string]any) bool {
	switch name {
	case "loss_included_assets_mismatch":
		data["includedAssets"] = append(data["includedAssets"].([]any), "building")
	case "loss_cost_unit_mismatch":
		lossEvidenceArray(data, "costBaselines")[0].(map[string]any)["unit"] = "meters"
	case "loss_input_reference_mismatch":
		data["inputReferences"] = []any{"https://data.mnr.gov.cn/tampered-reference"}
	case "loss_bad_time_order":
		data["calculatedAt"] = "2026-08-27T10:00:00Z"
	case "loss_snapshot_expired_at_assessment":
		lossSnapshotEvidence(data)["validTo"] = data["calculatedAt"]
	case "loss_spatial_after_assessment":
		lossSpatialEvidence(data)["calculatedAt"] = "2026-08-27T12:00:01Z"
	case "loss_projection_collected_after_assessment":
		lossSpatialEvidence(data)["projectionCollectedAt"] = "2026-08-27T12:00:01Z"
	case "loss_projection_expired_at_assessment":
		lossSpatialEvidence(data)["projectionValidTo"] = data["calculatedAt"]
	case "loss_projection_invalid_window":
		lossSpatialEvidence(data)["projectionValidFrom"] = "2026-08-28T02:00:00Z"
	case "loss_projection_limitations_missing":
		delete(lossSpatialEvidence(data), "projectionLimitations")
	case "loss_admin_boundary_bad_digest":
		lossSpatialEvidence(data)["adminBoundaryDigest"] = "not-a-sha256"
	case "loss_source_fetched_after_assessment":
		lossSnapshotSource(data)["fetchedAt"] = "2026-08-27T12:00:01Z"
	case "loss_source_valid_from_after_assessment":
		lossSnapshotSource(data)["validFrom"] = "2026-08-27T12:00:01Z"
	case "loss_source_observed_after_assessment":
		lossSnapshotSource(data)["observedAt"] = "2026-08-27T12:00:01Z"
	case "loss_source_published_after_assessment":
		lossSnapshotSource(data)["publishedAt"] = "2026-08-27T12:00:01Z"
	case "loss_source_revision_seen_after_assessment":
		lossSnapshotSource(data)["revisionFirstSeenAt"] = "2026-08-27T12:00:01Z"
	case "loss_cost_price_after_assessment":
		lossEvidenceArray(data, "costBaselines")[0].(map[string]any)["priceBaseDate"] = "2026-08-27T12:00:01Z"
	case "loss_baseline_fetched_after_assessment":
		lossEvidenceArray(data, "costBaselines")[0].(map[string]any)["source"].(map[string]any)["fetchedAt"] =
			"2026-08-27T12:00:01Z"
	case "loss_vulnerability_fetched_after_assessment":
		lossEvidenceArray(data, "vulnerabilities")[0].(map[string]any)["source"].(map[string]any)["fetchedAt"] =
			"2026-08-27T12:00:01Z"
	case "loss_private_source":
		lossSnapshotSource(data)["sourceUri"] = "https://127.0.0.1/private"
	case "loss_localhost_source":
		lossSnapshotSource(data)["sourceUri"] = "https://localhost/private"
	case "loss_ipv6_source":
		lossSnapshotSource(data)["sourceUri"] = "https://[::1]/private"
	case "loss_ipv4_mapped_source":
		lossSnapshotSource(data)["sourceUri"] = "https://[::ffff:127.0.0.1]/private"
	case "loss_local_source":
		lossSnapshotSource(data)["sourceUri"] = "https://metadata.local/private"
	case "loss_internal_source":
		lossSnapshotSource(data)["sourceUri"] = "https://metadata.internal/private"
	default:
		return applyLossSemanticMutation(name, data)
	}
	return true
}

func applyLossSemanticMutation(name string, data map[string]any) bool {
	if strings.HasPrefix(name, "loss_cost_") {
		return mutateSemanticList(data, "costBaselines", name, "cost-building-extra")
	}
	if strings.HasPrefix(name, "loss_vulnerability_") {
		return mutateSemanticList(data, "vulnerabilities", name, "vulnerability-building-extra")
	}
	return false
}

func mutateSemanticList(data map[string]any, field, name, extraID string) bool {
	evidence := data["evidence"].(map[string]any)
	values := evidence[field].([]any)
	switch {
	case strings.Contains(name, "missing"):
		values = values[1:]
	case strings.Contains(name, "duplicate"):
		copyValue := cloneJSONObject(values[0])
		copyValue["id"] = extraID + "-duplicate"
		values = append(values, copyValue)
	case strings.Contains(name, "extra"):
		copyValue := cloneJSONObject(values[0])
		copyValue["id"], copyValue["assetType"] = extraID, "building"
		values = append(values, copyValue)
	default:
		return false
	}
	evidence[field] = values
	return true
}

func lossEvidenceArray(data map[string]any, name string) []any {
	evidence := data["evidence"].(map[string]any)
	return evidence[name].([]any)
}

func lossSnapshotSource(data map[string]any) map[string]any {
	return lossSnapshotEvidence(data)["source"].(map[string]any)
}

func lossSnapshotEvidence(data map[string]any) map[string]any {
	evidence := data["evidence"].(map[string]any)
	return evidence["snapshot"].(map[string]any)
}

func lossSpatialEvidence(data map[string]any) map[string]any {
	evidence := data["evidence"].(map[string]any)
	return evidence["spatialAnalysis"].(map[string]any)
}

func cloneJSONObject(value any) map[string]any {
	payload, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(payload, &result)
	return result
}

func copyRecordedResponse(w http.ResponseWriter, recorder *httptest.ResponseRecorder) {
	for key, values := range recorder.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(recorder.Code)
	_, _ = w.Write(recorder.Body.Bytes())
}

func fixtureServiceNow() time.Time {
	return time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
}

func fixtureLossProjection(now time.Time, name string) (applicationloss.LossInputProjection, error) {
	calculatedAt := now.Add(-12 * time.Hour)
	snapshot := fixtureLossSnapshot(calculatedAt, now)
	zones := []applicationloss.LossRiskZone{
		{ID: "zone-low", SnapshotID: lossSnapshotID, Level: hazarddomain.RiskLow,
			AreaSquareM: 700.5, AreaCalculated: true, AdminCodes: []string{"510100", "CN"}},
		{ID: "zone-very-high", SnapshotID: lossSnapshotID, Level: hazarddomain.RiskVeryHigh,
			AreaSquareM: 500, AreaCalculated: true, AdminCodes: []string{"510100", "CN"}},
	}
	features := fixtureLossFeatures(name)
	projectionLimitations := []string{}
	if name == "loss_projection_limitation" {
		projectionLimitations = []string{"跳过非闭合设施 way 42，设施数量可能低估"}
	}
	analysis := applicationloss.LossSpatialProjection{
		ID: "spatial-analysis-e2e-v1", Version: "spatial-v2", Digest: strings.Repeat("b", 64),
		ProjectionCollectedAt: calculatedAt, ProjectionValidFrom: calculatedAt.Add(-time.Hour),
		ProjectionValidTo: now.Add(time.Hour),
		AdminBoundaryID:   "CHN-ADM0-2026", AdminBoundaryDigest: strings.Repeat("e", 64),
		AdminBoundaryReference: "https://data.mnr.gov.cn/admin-boundary/CHN-ADM0-2026",
		SnapshotID:             lossSnapshotID, Status: spatialdomain.AnalysisAvailable, RegionCode: "CN",
		TotalAreaSquareMeters: 1200.5, CalculatedAt: calculatedAt,
		ProjectionLimitations: projectionLimitations,
		InputReferences:       []string{"https://data.mnr.gov.cn/spatial/input"},
		DatasetReferences:     []string{"https://data.mnr.gov.cn/spatial/dataset"}, Features: features,
	}
	stats := applicationloss.RiskProjectionStats{ZoneCount: len(zones), MaxGeometryPoints: 5,
		MaxGeometryBytes: 100, TotalGeometryPoints: 10, TotalGeometryBytes: 200, SpatialJSONBytes: 400,
		FeatureCount: len(features), ReferenceCount: 9, UniqueReferenceCount: 7, ProjectionBytes: 4096,
		AnalysisID: analysis.ID, AnalysisDigest: analysis.Digest}
	result := applicationloss.LossInputProjection{Snapshot: snapshot, Zones: zones, Analysis: analysis, Stats: stats}
	if err := applicationloss.BindRiskProjectionIdentity(&result); err != nil {
		return applicationloss.LossInputProjection{}, err
	}
	return result, nil
}

func fixtureLossFeatures(name string) []applicationloss.LossExposureFeature {
	facilities, roads := 2.0, 10.0
	if name == "loss_big_integer" {
		facilities, roads = 1, 0
	}
	available := spatialdomain.MetricAvailable
	zoneIDs := []string{"zone-low", "zone-very-high"}
	return []applicationloss.LossExposureFeature{
		{FeatureID: "facility-shared", Kind: applicationloss.LossFeatureFacility, ZoneIDs: zoneIDs,
			Quantity: facilities, Unit: "count", CoverageRatio: 1, Status: available, Provided: true,
			InputReferences: []string{"https://data.mnr.gov.cn/facility/shared"}},
		{FeatureID: "population-shared", Kind: applicationloss.LossFeaturePopulation, ZoneIDs: zoneIDs,
			Quantity: 50, Unit: "people", CoverageRatio: 1, Status: available, Provided: true,
			InputReferences: []string{"https://data.mnr.gov.cn/population/shared"}},
		{FeatureID: "road-shared", Kind: applicationloss.LossFeatureRoad, ZoneIDs: zoneIDs,
			Quantity: roads, Unit: "meters", CoverageRatio: 1, Status: available, Provided: true,
			InputReferences: []string{"https://data.mnr.gov.cn/road/shared"}},
	}
}

func fixtureLossSnapshot(calculatedAt, now time.Time) hazarddomain.Snapshot {
	source := fixtureProvenance(calculatedAt, now, false)
	return hazarddomain.Snapshot{ID: lossSnapshotID, HazardType: hazarddomain.TypeLandslide,
		ModelName: "NASA LHASA", ModelVersion: "2.1.1", RunAt: calculatedAt.Add(-time.Hour),
		ValidFrom: calculatedAt.Add(-2 * time.Hour), ValidTo: now.Add(12 * time.Hour),
		RasterReference: source.SourceURI, ProbabilitySemantics: "模型概率",
		Thresholds: []hazarddomain.RiskThreshold{{Level: hazarddomain.RiskLow, Minimum: 0, Maximum: 1}},
		Status:     hazarddomain.SnapshotAvailable, Source: source, Limitations: []string{"辅助研判"}}
}

func fixtureBaselineSet(now time.Time, name string) lossdomain.BaselineSet {
	calculatedAt, version := now.Add(-12*time.Hour), "2026.08"
	source := fixtureProvenance(calculatedAt, now, true)
	region := "CN"
	return lossdomain.BaselineSet{Version: version,
		Population: []lossdomain.ExposureBaseline{fixtureExposureBaseline("population-510100",
			lossdomain.ExposurePopulation, "people", calculatedAt, source)},
		Roads: []lossdomain.ExposureBaseline{fixtureExposureBaseline("road-510100",
			lossdomain.ExposureRoad, "meters", calculatedAt, source)},
		Costs:           fixtureCosts(name, region, calculatedAt, source),
		Vulnerabilities: fixtureVulnerabilities(region, source)}
}

func fixtureExposureBaseline(id string, kind lossdomain.ExposureKind, unit string, calculatedAt time.Time,
	source provenance.Provenance,
) lossdomain.ExposureBaseline {
	return lossdomain.ExposureBaseline{ID: id, RegionCode: "510100", Kind: kind, Quantity: 1,
		Unit: unit, DataYear: 2026, CoverageRatio: 1, Source: source}
}

func fixtureCosts(name, region string, calculatedAt time.Time,
	source provenance.Provenance,
) []lossdomain.CostBaseline {
	facility := [3]int64{1000, 2000, 3000}
	road := [3]int64{100, 200, 300}
	if name == "loss_big_integer" {
		facility = [3]int64{9007199254740992, 9007199254740993, 9007199254740994}
		road = [3]int64{0, 0, 0}
	}
	return []lossdomain.CostBaseline{
		fixtureCost("cost-facility", lossdomain.AssetFacility, region, "count", facility, calculatedAt, source),
		fixtureCost("cost-road", lossdomain.AssetRoad, region, "meters", road, calculatedAt, source),
	}
}

func fixtureCost(id string, asset lossdomain.AssetType, region, unit string, values [3]int64,
	calculatedAt time.Time, source provenance.Provenance,
) lossdomain.CostBaseline {
	return lossdomain.CostBaseline{ID: id, AssetType: asset, RegionCode: region, Unit: unit,
		LowCents: values[0], CentralCents: values[1], HighCents: values[2], Currency: "CNY",
		PriceBaseDate: calculatedAt.Add(-30 * 24 * time.Hour), Status: lossdomain.BaselineApproved,
		ApprovedBy: "公开数据审核组", Source: source}
}

func fixtureVulnerabilities(region string, source provenance.Provenance) []lossdomain.Vulnerability {
	return []lossdomain.Vulnerability{
		fixtureVulnerability("vulnerability-facility", lossdomain.AssetFacility, region, source),
		fixtureVulnerability("vulnerability-road", lossdomain.AssetRoad, region, source),
	}
}

func fixtureVulnerability(id string, asset lossdomain.AssetType, region string,
	source provenance.Provenance,
) lossdomain.Vulnerability {
	return lossdomain.Vulnerability{ID: id, AssetType: asset, HazardType: "landslide", IntensityBand: "very_high",
		ImpactFractionLow: 1, ImpactFractionMid: 1, ImpactFractionHigh: 1,
		DamageRatioLow: 1, DamageRatioMid: 1, DamageRatioHigh: 1,
		CalibrationRegion: region, Status: lossdomain.BaselineApproved,
		ApprovedBy: "公开数据审核组", Source: source}
}

func fixtureProvenance(calculatedAt, now time.Time, baseline bool) provenance.Provenance {
	kind, provider, dataset, version := provenance.DataKindNowcast, "NASA", "LHASA", "2.1.1"
	validFrom, validTo := calculatedAt.Add(-3*time.Hour), now.Add(24*time.Hour)
	if baseline {
		kind, provider, dataset, version = provenance.DataKindBaseline, "公开基线联合目录", "AI-GDM 评估基线", "2026.08"
		validFrom, validTo = calculatedAt.Add(-30*24*time.Hour), now.Add(365*24*time.Hour)
	}
	part := provenance.SourcePart{Reference: signedFixtureURL(), Revision: "part-v1", SizeBytes: 1024,
		SHA256: strings.Repeat("d", 64)}
	value := provenance.Provenance{Provider: provider, Dataset: dataset, DatasetVersion: version,
		SourceURI: signedFixtureURL(), Citation: dataset + " 公开来源", License: "公开数据许可", DataKind: kind,
		ObservedAt: calculatedAt.Add(-2 * time.Hour), PublishedAt: calculatedAt.Add(-90 * time.Minute),
		RevisionFirstSeenAt: calculatedAt.Add(-75 * time.Minute), FetchedAt: calculatedAt.Add(-time.Hour),
		ValidFrom: validFrom, ValidTo: validTo, CRS: "EPSG:4326", SHA256: strings.Repeat("c", 64),
		TransformVersion: "fixture-v1", Stale: false, QualityFlags: []string{"checked"},
		SourceParts: []provenance.SourcePart{part}}
	value.SourceRevision = provenance.CompositeSourceRevision(value.SourceParts)
	return value
}

func signedFixtureURL() string {
	return "https://user:pass@data.mnr.gov.cn/assessment-e2e" +
		"?revision=7&password=secret&session=secret&X-Amz-Signature=secret#fragment"
}

func responseEnvelope(data any) map[string]any {
	return map[string]any{"data": data, "requestId": "fixture-request"}
}
