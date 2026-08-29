package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Requim/AI-GDM/internal/adapters/http/lossapi"
	"github.com/Requim/AI-GDM/internal/adapters/provider/geoboundaries"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/overpass"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

const (
	overpassOpenFacilityLimitation = "Overpass 返回的非闭合或不足四点设施 way 已跳过，未作为设施计数（1 条）"
	geoBoundaryMetadataURL         = "https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM0/"
	geoBoundarySourceURL           = "https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson"
	geoBoundaryMediaURL            = "https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/9469f09/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson"
	geoBoundaryID                  = "CHN-ADM0-351020"
	geoBoundaryShapeID             = "351020B83567386155957"
	geoBoundaryYear                = "2019"
	geoBoundarySource              = "geoBoundaries, Wikimedia Commons"
	geoBoundaryLicense             = "Public Domain"
)

func TestExposureOverpassLimitationSurvivesPostgresLossHTTPChain(t *testing.T) {
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, projection, requests := collectOverpassChainProjection(t, ctx, repository, now)
	cleanup := overpassChainCleanupKey{snapshotID: snapshot.ID, analysisID: projection.Input.Analysis.ID,
		projectionID: projection.Input.Analysis.ProjectionID}
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			if err := cleanupCompletedOverpassChain(context.Background(), repository, cleanup); err != nil {
				t.Errorf("清理 Overpass 成功全链: %v", err)
			}
		}
	})
	stored, err := repository.ReadLossInput(ctx, snapshot.ID, now, productionLossProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertOverpassProjectionIdentity(t, projection.Input, stored)

	handler := newOverpassChainLossHandler(t, ctx, repository, now)
	created := performOverpassChainJSON(handler, http.MethodPost, "/api/v1/loss/assessments",
		fmt.Sprintf(`{"snapshotId":%q}`, snapshot.ID))
	if created.Code != http.StatusCreated {
		t.Fatalf("损失评估 POST 状态=%d body=%s", created.Code, created.Body.String())
	}
	assertOverpassAssessmentResponse(t, created.Body.Bytes(), projection.Input.Analysis.ProjectionDigest)
	location := created.Header().Get("Location")
	sources := performOverpassChainJSON(handler, http.MethodGet, location+"/sources", "")
	if sources.Code != http.StatusOK {
		t.Fatalf("损失来源审计状态=%d body=%s", sources.Code, sources.Body.String())
	}
	assertOverpassSourceAudit(t, sources.Body.Bytes(), projection.Input.Analysis.ProjectionDigest)
	if requests.Load() != 1 {
		t.Fatalf("Overpass 创建查询次数=%d want=1", requests.Load())
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err = cleanupCompletedOverpassChain(context.Background(), repository, cleanup); err != nil {
			t.Fatalf("第 %d 次清理 Overpass 成功全链: %v", attempt, err)
		}
	}
	cleaned = true
}

func TestExposureRejectsMissingOverpassCoordinateBeforePostgresProjection(t *testing.T) {
	assertUnsafeOverpassResponseNotPersisted(t, func(elements []map[string]any) {
		delete(elements[1], "lat")
	})
}

func TestExposureRejectsBadOverpassTagsBeforePostgresProjection(t *testing.T) {
	assertUnsafeOverpassResponseNotPersisted(t, func(elements []map[string]any) {
		elements[1]["tags"] = nil
	})
}

func TestExposureRejectsUnicodeFoldedOverpassTagsBeforePostgresProjection(t *testing.T) {
	assertUnsafeOverpassResponseNotPersisted(t, func(elements []map[string]any) {
		elements[1]["tags"] = nil
		elements[1]["tagſ"] = map[string]string{"amenity": "hospital"}
	})
}

func TestExposureRejectsMismatchedGeoBoundaryShapeBeforePostgresLoss(t *testing.T) {
	assertUnsafeGeoBoundaryResponseNotPersisted(t, geoBoundaryMetadataPayload(),
		mismatchedGeoBoundaryPayload(), 1, 1)
}

func TestExposureRejectsUnicodeFoldedGeoBoundarySourceBeforePostgresLoss(t *testing.T) {
	metadata := strings.Replace(geoBoundaryMetadataPayload(), `"boundarySource"`, `"boundaryſource"`, 1)
	assertUnsafeGeoBoundaryResponseNotPersisted(t, metadata, matchingGeoBoundaryPayload(), 1, 0)
}

func assertUnsafeGeoBoundaryResponseNotPersisted(t *testing.T, metadata, geometry string,
	wantMetadata, wantGeometry int32,
) {
	t.Helper()
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := fmt.Sprintf("geoboundary-shape-%d", time.Now().UnixNano())
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, suffix, 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), suffix, false)
	boundary, requests := geoBoundaryProviderWithResponses(t, now, metadata, geometry)
	infrastructure, overpassRequests := newOverpassChainProvider(t, now)
	collector, err := exposurecollection.New(repository, boundary,
		overpassChainPopulation{now: now}, infrastructure, repository, repository, repository,
		overpassChainClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = collector.Collect(ctx, snapshot.ID, analysis.ID); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Collect() error=%v", err)
	}
	if requests.metadata.Load() != wantMetadata || requests.geometry.Load() != wantGeometry ||
		overpassRequests.Load() != 0 {
		t.Fatalf("坏 geoBoundaries 请求计数 metadata=%d/%d geometry=%d/%d Overpass=%d/0",
			requests.metadata.Load(), wantMetadata, requests.geometry.Load(), wantGeometry,
			overpassRequests.Load())
	}
	assertNoExposureProjectionOrLoss(t, ctx, repository, snapshot.ID, analysis.ID, now)
}

func assertUnsafeOverpassResponseNotPersisted(t *testing.T, mutate func([]map[string]any)) {
	t.Helper()
	ctx, repository := integrationHazardRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := fmt.Sprintf("overpass-coordinate-%d", time.Now().UnixNano())
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, suffix, 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), suffix, false)
	response := overpassChainResponse(now)
	elements := response["elements"].([]map[string]any)
	mutate(elements)
	provider, requests := newOverpassChainProviderWithResponse(t, now, response)
	collector, err := exposurecollection.New(repository, overpassChainBoundary{now: now},
		overpassChainPopulation{now: now}, provider, repository, repository, repository,
		overpassChainClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = collector.Collect(ctx, snapshot.ID, analysis.ID); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Collect() error=%v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("Overpass 坏响应请求次数=%d want=1", requests.Load())
	}
	assertNoExposureProjectionOrLoss(t, ctx, repository, snapshot.ID, analysis.ID, now)
}

func assertNoExposureProjectionOrLoss(t *testing.T, ctx context.Context, repository *HazardRepository,
	snapshotID, analysisID string, now time.Time,
) {
	t.Helper()
	var count int
	if err := repository.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM spatial_exposure_projections WHERE analysis_id=$1`, analysisID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	current, err := repository.HasCurrentExposureProjection(ctx, snapshotID, analysisID, now)
	if err != nil || current || count != 0 {
		t.Fatalf("exposure projection count=%d current=%v error=%v", count, current, err)
	}
	if _, err = repository.ReadLossInput(ctx, snapshotID, now,
		productionLossProjectionLimits()); !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("ReadLossInput() error=%v", err)
	}
	assertLossHTTPFailsClosed(t, ctx, repository, snapshotID, now)
}

func assertLossHTTPFailsClosed(t *testing.T, ctx context.Context, repository *HazardRepository,
	snapshotID string, now time.Time,
) {
	t.Helper()
	handler := newOverpassChainLossHandler(t, ctx, repository, now)
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(),
			`DELETE FROM loss_assessments WHERE snapshot_id=$1`, snapshotID)
	})
	response := performOverpassChainJSON(handler, http.MethodPost, "/api/v1/loss/assessments",
		fmt.Sprintf(`{"snapshotId":%q}`, snapshotID))
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || envelope.Error.Code != "insufficient_data" ||
		response.Header().Get("Location") != "" {
		t.Fatalf("损失评估 fail-closed 状态=%d code=%q location=%q body=%s", response.Code,
			envelope.Error.Code, response.Header().Get("Location"), response.Body.String())
	}
	var count int
	if err := repository.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM loss_assessments WHERE snapshot_id=$1`, snapshotID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("损失评估副作用 count=%d error=%v", count, err)
	}
}

type overpassChainCleanupKey struct {
	snapshotID   string
	analysisID   string
	projectionID string
}

type overpassCleanupStatement struct {
	name  string
	query string
	args  []any
}

type overpassCleanupQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func cleanupCompletedOverpassChain(parent context.Context, repository *HazardRepository,
	key overpassChainCleanupKey,
) (resultErr error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始 Overpass 全链清理事务: %w", err)
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rollbackCancel()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("回滚 Overpass 全链清理事务: %w", rollbackErr))
		}
	}()
	if err = deleteCompletedExposureProjection(ctx, tx, key.projectionID); err != nil {
		return err
	}
	if err = deleteOverpassChainUpstream(ctx, tx, key); err != nil {
		return err
	}
	if err = verifyExposureCleanupTriggers(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交 Overpass 全链清理事务: %w", err)
	}
	finished = true
	return verifyOverpassChainCleanup(ctx, repository, key)
}

func deleteCompletedExposureProjection(ctx context.Context, tx pgx.Tx, projectionID string) error {
	statements := []overpassCleanupStatement{
		{name: "禁用 feature-zone 不可变触发器", query: `ALTER TABLE spatial_exposure_feature_zones DISABLE TRIGGER spatial_exposure_feature_zones_immutable`},
		{name: "禁用 feature 不可变触发器", query: `ALTER TABLE spatial_exposure_features DISABLE TRIGGER spatial_exposure_features_immutable`},
		{name: "禁用 projection-zone 不可变触发器", query: `ALTER TABLE spatial_exposure_projection_zones DISABLE TRIGGER spatial_exposure_projection_zones_immutable`},
		{name: "禁用 projection 不可变触发器", query: `ALTER TABLE spatial_exposure_projections DISABLE TRIGGER spatial_exposure_projections_immutable`},
		{name: "删除 exposure feature-zone", query: `DELETE FROM spatial_exposure_feature_zones WHERE projection_id=$1`, args: []any{projectionID}},
		{name: "删除 exposure feature", query: `DELETE FROM spatial_exposure_features WHERE projection_id=$1`, args: []any{projectionID}},
		{name: "删除 exposure projection-zone", query: `DELETE FROM spatial_exposure_projection_zones WHERE projection_id=$1`, args: []any{projectionID}},
		{name: "删除 completed exposure projection", query: `DELETE FROM spatial_exposure_projections WHERE id=$1`, args: []any{projectionID}},
		{name: "恢复 projection 不可变触发器", query: `ALTER TABLE spatial_exposure_projections ENABLE TRIGGER spatial_exposure_projections_immutable`},
		{name: "恢复 projection-zone 不可变触发器", query: `ALTER TABLE spatial_exposure_projection_zones ENABLE TRIGGER spatial_exposure_projection_zones_immutable`},
		{name: "恢复 feature 不可变触发器", query: `ALTER TABLE spatial_exposure_features ENABLE TRIGGER spatial_exposure_features_immutable`},
		{name: "恢复 feature-zone 不可变触发器", query: `ALTER TABLE spatial_exposure_feature_zones ENABLE TRIGGER spatial_exposure_feature_zones_immutable`},
	}
	return executeOverpassCleanupStatements(ctx, tx, statements)
}

func deleteOverpassChainUpstream(ctx context.Context, tx pgx.Tx, key overpassChainCleanupKey) error {
	statements := []overpassCleanupStatement{
		{name: "删除 loss assessment", query: `DELETE FROM loss_assessments WHERE snapshot_id=$1`, args: []any{key.snapshotID}},
		{name: "删除 spatial zone result", query: `DELETE FROM spatial_zone_results WHERE analysis_id=$1`, args: []any{key.analysisID}},
		{name: "删除 spatial analysis", query: `DELETE FROM spatial_analyses WHERE id=$1`, args: []any{key.analysisID}},
		{name: "删除 risk zone", query: `DELETE FROM risk_zones WHERE snapshot_id=$1`, args: []any{key.snapshotID}},
		{name: "删除 hazard snapshot", query: `DELETE FROM hazard_snapshots WHERE id=$1`, args: []any{key.snapshotID}},
	}
	return executeOverpassCleanupStatements(ctx, tx, statements)
}

func executeOverpassCleanupStatements(ctx context.Context, tx pgx.Tx,
	statements []overpassCleanupStatement,
) error {
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("%s: %w", statement.name, err)
		}
	}
	return nil
}

func verifyOverpassChainCleanup(ctx context.Context, repository *HazardRepository,
	key overpassChainCleanupKey,
) error {
	checks := []overpassCleanupStatement{
		{name: "loss_assessments", query: `SELECT COUNT(*) FROM loss_assessments WHERE snapshot_id=$1`, args: []any{key.snapshotID}},
		{name: "spatial_exposure_feature_zones", query: `SELECT COUNT(*) FROM spatial_exposure_feature_zones WHERE projection_id=$1`, args: []any{key.projectionID}},
		{name: "spatial_exposure_features", query: `SELECT COUNT(*) FROM spatial_exposure_features WHERE projection_id=$1`, args: []any{key.projectionID}},
		{name: "spatial_exposure_projection_zones", query: `SELECT COUNT(*) FROM spatial_exposure_projection_zones WHERE projection_id=$1`, args: []any{key.projectionID}},
		{name: "spatial_exposure_projections", query: `SELECT COUNT(*) FROM spatial_exposure_projections WHERE id=$1`, args: []any{key.projectionID}},
		{name: "spatial_zone_results", query: `SELECT COUNT(*) FROM spatial_zone_results WHERE analysis_id=$1`, args: []any{key.analysisID}},
		{name: "spatial_analyses", query: `SELECT COUNT(*) FROM spatial_analyses WHERE id=$1`, args: []any{key.analysisID}},
		{name: "risk_zones", query: `SELECT COUNT(*) FROM risk_zones WHERE snapshot_id=$1`, args: []any{key.snapshotID}},
		{name: "risk_assessments", query: `SELECT COUNT(*) FROM risk_assessments WHERE snapshot_id=$1`, args: []any{key.snapshotID}},
		{name: "hazard_snapshots", query: `SELECT COUNT(*) FROM hazard_snapshots WHERE id=$1`, args: []any{key.snapshotID}},
	}
	for _, check := range checks {
		var count int
		if err := repository.pool.QueryRow(ctx, check.query, check.args...).Scan(&count); err != nil {
			return fmt.Errorf("检查 %s 清理结果: %w", check.name, err)
		}
		if count != 0 {
			return fmt.Errorf("%s 清理后仍有 %d 行", check.name, count)
		}
	}
	return verifyExposureCleanupTriggers(ctx, repository.pool)
}

func verifyExposureCleanupTriggers(ctx context.Context, queryer overpassCleanupQueryer) error {
	checks := [][2]string{
		{"spatial_exposure_projections", "spatial_exposure_projections_immutable"},
		{"spatial_exposure_projection_zones", "spatial_exposure_projection_zones_immutable"},
		{"spatial_exposure_features", "spatial_exposure_features_immutable"},
		{"spatial_exposure_feature_zones", "spatial_exposure_feature_zones_immutable"},
	}
	for _, check := range checks {
		var enabled string
		err := queryer.QueryRow(ctx, `SELECT tgenabled::TEXT FROM pg_trigger
			WHERE tgrelid=$1::regclass AND tgname=$2`, check[0], check[1]).Scan(&enabled)
		if err != nil {
			return fmt.Errorf("检查 %s 触发器: %w", check[1], err)
		}
		if enabled != "O" {
			return fmt.Errorf("%s 触发器状态=%q", check[1], enabled)
		}
	}
	return nil
}

type geoBoundaryRequestCounters struct {
	metadata atomic.Int32
	geometry atomic.Int32
}

func geoBoundaryProviderWithResponses(t *testing.T, now time.Time, metadata, geometry string,
) (*geoboundaries.Provider, *geoBoundaryRequestCounters) {
	t.Helper()
	requests := &geoBoundaryRequestCounters{}
	transport := geoBoundaryChainRoundTrip(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case geoBoundaryMetadataURL:
			requests.metadata.Add(1)
			return geoBoundaryChainResponse(request, metadata), nil
		case geoBoundaryMediaURL:
			requests.geometry.Add(1)
			return geoBoundaryChainResponse(request, geometry), nil
		default:
			return nil, fmt.Errorf("意外的 geoBoundaries 请求: %s", request.URL)
		}
	})
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Transport: transport},
		MaxAttempts: 1, Now: func() time.Time { return now }})
	provider, err := geoboundaries.New(geoboundaries.Options{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	return provider, requests
}

func geoBoundaryMetadataPayload() string {
	return `{"boundaryID":"CHN-ADM0-351020","boundaryName":"China","boundaryISO":"CHN",` +
		`"boundaryYearRepresented":"2019","boundaryType":"ADM0",` +
		`"boundarySource":"geoBoundaries, Wikimedia Commons","boundaryLicense":"Public Domain",` +
		`"simplifiedGeometryGeoJSON":"` + geoBoundarySourceURL + `"}`
}

func geoBoundaryAuditReference() string {
	query := url.Values{"boundaryID": {geoBoundaryID}, "boundaryYear": {geoBoundaryYear},
		"source": {geoBoundarySource}, "license": {geoBoundaryLicense}, "shapeID": {geoBoundaryShapeID},
		"metadataSha256": {strings.Repeat("a", 64)}, "geometrySha256": {strings.Repeat("b", 64)}}
	return geoBoundaryMediaURL + "?" + query.Encode()
}

func mismatchedGeoBoundaryPayload() string {
	return geoBoundaryPayload("351021B83567386155957")
}

func matchingGeoBoundaryPayload() string {
	return geoBoundaryPayload(geoBoundaryShapeID)
}

func geoBoundaryPayload(shapeID string) string {
	return `{"type":"FeatureCollection","crs":{"type":"name","properties":` +
		`{"name":"urn:ogc:def:crs:OGC:1.3:CRS84"}},"features":[{"type":"Feature",` +
		`"properties":{"shapeID":"` + shapeID + `","shapeName":"China",` +
		`"shapeISO":"CHN","shapeGroup":"CHN","shapeType":"ADM0"},"geometry":` +
		`{"type":"MultiPolygon","coordinates":[[[[116,39],[116.1,39],` +
		`[116.1,39.1],[116,39]]]]}}]}`
}

func geoBoundaryChainResponse(request *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

type geoBoundaryChainRoundTrip func(*http.Request) (*http.Response, error)

func (function geoBoundaryChainRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func collectOverpassChainProjection(t *testing.T, ctx context.Context, repository *HazardRepository,
	now time.Time,
) (hazard.Snapshot, exposurecollection.ExposureProjection, *atomic.Int32) {
	t.Helper()
	suffix := fmt.Sprintf("overpass-chain-%d", time.Now().UnixNano())
	snapshot, zones := saveLossProjectionRisk(t, ctx, repository, now, suffix, 1)
	analysis := insertLossSpatialAnalysis(t, ctx, repository, snapshot, zones,
		now.Add(-5*time.Minute), suffix, false)
	provider, requests := newOverpassChainProvider(t, now)
	collector, err := exposurecollection.New(repository, overpassChainBoundary{now: now},
		overpassChainPopulation{now: now}, provider, repository, repository, repository,
		overpassChainClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := collector.Collect(ctx, snapshot.ID, analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, projection, requests
}

func newOverpassChainProvider(t *testing.T, now time.Time) (*overpass.Provider, *atomic.Int32) {
	return newOverpassChainProviderWithResponse(t, now, overpassChainResponse(now))
}

func newOverpassChainProviderWithResponse(t *testing.T, now time.Time,
	response map[string]any,
) (*overpass.Provider, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost {
			t.Errorf("Overpass method=%s", request.Method)
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("编码 Overpass 响应: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 1,
		Now: func() time.Time { return now }})
	provider, err := overpass.New(overpass.Options{Client: client, Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return provider, requests
}

func overpassChainResponse(now time.Time) map[string]any {
	return map[string]any{"version": 0.6, "generator": "Overpass API",
		"osm3s": map[string]any{"timestamp_osm_base": now.Add(-2 * time.Minute).Format(time.RFC3339)},
		"elements": []map[string]any{
			{"type": "way", "id": 11, "tags": map[string]string{"highway": "primary"},
				"geometry": []map[string]float64{{"lat": 39.001, "lon": 116.001}, {"lat": 39.009, "lon": 116.009}}},
			{"type": "node", "id": 22, "lat": 39.005, "lon": 116.005,
				"tags": map[string]string{"amenity": "hospital"}},
			{"type": "way", "id": 33, "tags": map[string]string{"amenity": "clinic"},
				"geometry": []map[string]float64{{"lat": 39.002, "lon": 116.002},
					{"lat": 39.006, "lon": 116.006}, {"lat": 39.008, "lon": 116.003}}},
		}}
}

type overpassChainBoundary struct{ now time.Time }

func (p overpassChainBoundary) Boundary(context.Context) (exposurecollection.AdministrativeBoundary, error) {
	return exposurecollection.AdministrativeBoundary{BoundaryID: geoBoundaryID,
		RegionCode: "CN", BoundaryType: "ADM0", BoundaryYear: geoBoundaryYear, Source: geoBoundarySource,
		License: geoBoundaryLicense, Digest: strings.Repeat("b", 64),
		Reference:       geoBoundarySourceURL,
		Geometry:        json.RawMessage(`{"type":"Polygon","coordinates":[[[115.99,38.99],[116.02,38.99],[116.02,39.02],[115.99,39.02],[115.99,38.99]]]}`),
		CollectedAt:     p.now.Add(-3 * time.Minute),
		InputReferences: []string{geoBoundaryMetadataURL, geoBoundaryAuditReference()}}, nil
}

type overpassChainPopulation struct{ now time.Time }

func (p overpassChainPopulation) Population(_ context.Context,
	query exposurecollection.PopulationQuery,
) (exposurecollection.PopulationResult, error) {
	return exposurecollection.PopulationResult{TaskID: "overpass-chain-population", Total: 25,
		AreaKM2: query.ExpectedAreaSquareMeter / 1_000_000, DataYear: query.Year,
		DataSource:      "WorldPop Global 2 Population Data",
		DatasetIdentity: "urn:worldpop:global2:population:100m",
		CollectedAt:     p.now.Add(-time.Minute), ValidFrom: p.now.Add(-30 * time.Minute),
		ValidTo:         p.now.Add(12 * time.Hour),
		InputReferences: []string{"https://api.worldpop.org/v1/tasks/overpass-chain-population"}}, nil
}

type overpassChainClock struct{ now time.Time }

func (c overpassChainClock) Now() time.Time { return c.now }

func assertOverpassProjectionIdentity(t *testing.T, created, stored applicationloss.LossInputProjection) {
	t.Helper()
	if created.Analysis.ProjectionDigest == "" ||
		created.Analysis.ProjectionDigest != stored.Analysis.ProjectionDigest ||
		!slices.Contains(created.Analysis.ProjectionLimitations, overpassOpenFacilityLimitation) ||
		!slices.Contains(stored.Analysis.ProjectionLimitations, overpassOpenFacilityLimitation) {
		t.Fatalf("Overpass limitation 或投影摘要未完整持久化: created=%+v stored=%+v",
			created.Analysis, stored.Analysis)
	}
	assertGeoBoundaryAuditReference(t, created.Analysis.DatasetReferences)
	assertGeoBoundaryAuditReference(t, stored.Analysis.DatasetReferences)
}

func newOverpassChainLossHandler(t *testing.T, ctx context.Context, repository *HazardRepository,
	now time.Time,
) http.Handler {
	t.Helper()
	version := fmt.Sprintf("integration-loss-overpass-chain-%d", time.Now().UnixNano())
	baselines := NewLossBaselineRepository(repository.pool)
	deleteLossBaselineVersions(t, ctx, repository.pool, version)
	mustReplaceBaselineSet(t, ctx, baselines, lossServiceBaselineSet(version, now.Add(-2*time.Hour), true))
	t.Cleanup(func() { deleteLossBaselineVersions(t, context.Background(), repository.pool, version) })
	service, err := applicationloss.NewService(repository, baselines, overpassChainClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	assessments := NewLossAssessmentRepository(repository.pool)
	handler, err := lossapi.New(service, assessments, assessments, "/api/v1/loss",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	root := http.NewServeMux()
	root.Handle("/api/v1/loss/", http.StripPrefix("/api/v1/loss", handler))
	return root
}

func performOverpassChainJSON(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type overpassAssessmentEnvelope struct {
	Data struct {
		ID              string   `json:"id"`
		Limitations     []string `json:"limitations"`
		InputReferences []string `json:"inputReferences"`
		Evidence        struct {
			SpatialAnalysis struct {
				ProjectionDigest      string   `json:"projectionDigest"`
				ProjectionLimitations []string `json:"projectionLimitations"`
				DatasetReferences     []string `json:"datasetReferences"`
			} `json:"spatialAnalysis"`
		} `json:"evidence"`
	} `json:"data"`
}

func assertOverpassAssessmentResponse(t *testing.T, payload []byte, digest string) {
	t.Helper()
	var envelope overpassAssessmentEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	spatial := envelope.Data.Evidence.SpatialAnalysis
	if envelope.Data.ID == "" || spatial.ProjectionDigest != digest ||
		!slices.Contains(spatial.ProjectionLimitations, overpassOpenFacilityLimitation) ||
		!slices.Contains(envelope.Data.Limitations, overpassOpenFacilityLimitation) {
		t.Fatalf("损失评估未公开真实 Overpass limitation: %+v", envelope.Data)
	}
	assertGeoBoundaryAuditReference(t, envelope.Data.InputReferences)
	assertGeoBoundaryAuditReference(t, spatial.DatasetReferences)
}

type overpassSourceEnvelope struct {
	Data struct {
		ProjectionDigest      string   `json:"projectionDigest"`
		ProjectionLimitations []string `json:"projectionLimitations"`
		InputReferences       []string `json:"inputReferences"`
		Evidence              struct {
			SpatialAnalysis struct {
				ProjectionDigest      string   `json:"projectionDigest"`
				ProjectionLimitations []string `json:"projectionLimitations"`
				InputReferences       []string `json:"inputReferences"`
				DatasetReferences     []string `json:"datasetReferences"`
			} `json:"spatialAnalysis"`
		} `json:"evidence"`
	} `json:"data"`
}

func assertOverpassSourceAudit(t *testing.T, payload []byte, digest string) {
	t.Helper()
	var envelope overpassSourceEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	value, spatial := envelope.Data, envelope.Data.Evidence.SpatialAnalysis
	if value.ProjectionDigest != digest || spatial.ProjectionDigest != digest ||
		!slices.Contains(value.ProjectionLimitations, overpassOpenFacilityLimitation) ||
		!slices.Contains(spatial.ProjectionLimitations, overpassOpenFacilityLimitation) ||
		!slices.Contains(value.InputReferences, "https://www.openstreetmap.org") {
		t.Fatalf("来源审计未绑定 Overpass limitation、摘要或来源: %+v", value)
	}
	assertGeoBoundaryAuditReference(t, value.InputReferences)
	assertGeoBoundaryAuditReference(t, spatial.DatasetReferences)
}

func assertGeoBoundaryAuditReference(t *testing.T, references []string) {
	t.Helper()
	for _, reference := range references {
		parsed, err := url.Parse(reference)
		if err != nil {
			continue
		}
		query := parsed.Query()
		parsed.RawQuery = ""
		if parsed.Fragment == "" && parsed.String() == geoBoundaryMediaURL &&
			query.Get("boundaryID") == geoBoundaryID && query.Get("shapeID") == geoBoundaryShapeID &&
			query.Get("boundaryYear") == geoBoundaryYear && query.Get("source") == geoBoundarySource &&
			query.Get("license") == geoBoundaryLicense &&
			query.Get("metadataSha256") == strings.Repeat("a", 64) &&
			query.Get("geometrySha256") == strings.Repeat("b", 64) {
			return
		}
	}
	t.Fatalf("loss /sources 未保留 geoBoundaries 审计绑定: %v", references)
}
