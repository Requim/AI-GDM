package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/platform/resources"
)

func TestDefaultExposureHTTPPolicies(t *testing.T) {
	policies := defaultExposureHTTPPolicies()
	cases := []struct {
		name string
		got  exposureHTTPPolicy
		want exposureHTTPPolicy
	}{
		{name: "geoBoundaries", got: policies.boundary,
			want: exposureHTTPPolicy{timeout: 30 * time.Second, interval: time.Second, maxAttempts: 2}},
		{name: "WorldPop", got: policies.population,
			want: exposureHTTPPolicy{timeout: 30 * time.Second, interval: 500 * time.Millisecond, maxAttempts: 3}},
		{name: "Overpass", got: policies.infrastructure,
			want: exposureHTTPPolicy{timeout: 40 * time.Second, interval: 2 * time.Second, maxAttempts: 2}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if item.got != item.want {
				t.Fatalf("policy=%+v, want %+v", item.got, item.want)
			}
		})
	}
	clients := newExposureHTTPClients(testRefreshLogger())
	if clients.boundary == nil || clients.population == nil || clients.infrastructure == nil {
		t.Fatal("生产暴露 HTTP 客户端未完整创建")
	}
}

func TestBuildExposureHTTPClientsPreservesProductionPolicyMapping(t *testing.T) {
	cases := []struct {
		name     string
		client   func(exposureHTTPClients) *httpclient.Client
		probe    func(exposurePolicyProbes) *exposurePolicyProbe
		url      string
		timeout  time.Duration
		interval time.Duration
		attempts int
	}{
		{name: "geoBoundaries", client: func(value exposureHTTPClients) *httpclient.Client { return value.boundary },
			probe: func(value exposurePolicyProbes) *exposurePolicyProbe { return value.boundary },
			url:   "https://boundary.example.test/probe", timeout: 30 * time.Second,
			interval: time.Second, attempts: 2},
		{name: "WorldPop", client: func(value exposureHTTPClients) *httpclient.Client { return value.population },
			probe: func(value exposurePolicyProbes) *exposurePolicyProbe { return value.population },
			url:   "https://population.example.test/probe", timeout: 30 * time.Second,
			interval: 500 * time.Millisecond, attempts: 3},
		{name: "Overpass", client: func(value exposureHTTPClients) *httpclient.Client { return value.infrastructure },
			probe: func(value exposurePolicyProbes) *exposurePolicyProbe { return value.infrastructure },
			url:   "https://infrastructure.example.test/probe", timeout: 40 * time.Second,
			interval: 2 * time.Second, attempts: 2},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			assertProductionHTTPPolicy(t, item.client, item.probe, item.url,
				item.timeout, item.interval, item.attempts)
		})
	}
}

func TestNewExposureProvidersKeepsClientPositionsAndEndpoints(t *testing.T) {
	transports := newExposureProviderTransports()
	providers, err := newExposureProviders(exposureHTTPClients{
		boundary:       providerTestClient(transports.boundary),
		population:     providerTestClient(transports.population),
		infrastructure: providerTestClient(transports.infrastructure),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providers.boundary.Boundary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = providers.population.Population(context.Background(), exposurecollection.PopulationQuery{
		Geometry: populationQueryGeometry(), ExpectedAreaSquareMeter: 1_000_000, Year: 2026,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = providers.infrastructure.Infrastructure(context.Background(),
		exposurecollection.InfrastructureQuery{Bounds: exposurecollection.Bounds{
			South: 30, West: 104, North: 30.01, East: 104.01,
		}}); err != nil {
		t.Fatal(err)
	}
	assertProviderRequests(t, transports)
}

func TestWorldPopProductionClientRedirectContracts(t *testing.T) {
	t.Run("创建 POST 禁止重定向重放", testWorldPopCreateRedirectDenied)
	t.Run("轮询 GET 允许同源 HTTPS 重定向", testWorldPopPollRedirectAllowed)
}

func TestOverpassProductionProviderDeniesEveryRedirect(t *testing.T) {
	statuses := []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			transport := &overpassRedirectTransport{redirectStatus: status}
			provider := overpassProviderWithProductionClient(t, transport)
			_, err := provider.Infrastructure(context.Background(), exposurecollection.InfrastructureQuery{
				Bounds: exposurecollection.Bounds{South: 30, West: 104, North: 30.01, East: 104.01},
			})
			want := []string{"POST https://overpass-api.de/api/interpreter"}
			if !errors.Is(err, domain.ErrProviderUnavailable) || !slices.Equal(transport.snapshot(), want) {
				t.Fatalf("Overpass redirect status=%d error=%v requests=%v", status, err,
					transport.snapshot())
			}
		})
	}
}

func TestBuildExposureCollectorInvokesInjectedPorts(t *testing.T) {
	probe := newExposurePortProbe()
	collector, err := buildExposureCollector(exposureCollectorRuntime{
		geometries: probe, administrator: probe, projector: probe, writer: probe, clock: probe,
	}, exposureProviderSet{boundary: probe, population: probe, infrastructure: probe})
	if err != nil {
		t.Fatal(err)
	}
	value, err := collector.Collect(context.Background(), probe.input.Snapshot.ID, probe.input.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"geometry", "boundary", "administrator", "population", "infrastructure", "projector", "writer"}
	if !slices.Equal(probe.calls, wantCalls) || probe.saved.Input.Analysis.ProjectionID == "" ||
		value.Input.Analysis.ProjectionID != probe.saved.Input.Analysis.ProjectionID {
		t.Fatalf("组合根调用=%v, saved=%+v", probe.calls, probe.saved.Input.Analysis)
	}
}

func TestBuildExposureHTTPClientAppliesRetryLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := buildExposureHTTPClient(testRefreshLogger(), exposureHTTPPolicy{
		timeout: time.Second, interval: time.Nanosecond, maxAttempts: 2,
	}, server.Client())
	_, err := client.Do(context.Background(), httpclient.Request{Method: http.MethodGet, URL: server.URL})
	if err == nil || calls.Load() != 2 {
		t.Fatalf("retry error=%v calls=%d", err, calls.Load())
	}
}

func TestBuildExposureHTTPClientAppliesRateLimit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	interval := 60 * time.Millisecond
	client := buildExposureHTTPClient(testRefreshLogger(), exposureHTTPPolicy{
		timeout: time.Second, interval: interval, maxAttempts: 1,
	}, server.Client())
	started := time.Now()
	for range 2 {
		if _, err := client.Do(context.Background(), httpclient.Request{Method: http.MethodGet, URL: server.URL}); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed < interval-10*time.Millisecond {
		t.Fatalf("限速等待=%s, want >=%s", elapsed, interval-10*time.Millisecond)
	}
}

func TestBuildExposureHTTPClientAppliesTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := buildExposureHTTPClient(testRefreshLogger(), exposureHTTPPolicy{
		timeout: 20 * time.Millisecond, interval: time.Nanosecond, maxAttempts: 1,
	}, server.Client())
	if _, err := client.Do(context.Background(), httpclient.Request{Method: http.MethodGet, URL: server.URL}); err == nil {
		t.Fatal("HTTP 超时未生效")
	}
}

func TestNewExposureCollectorWiresProductionAdapters(t *testing.T) {
	pool := &pgxpool.Pool{}
	dependencies := &resources.Resources{Database: pool}
	repository := postgres.NewHazardRepository(pool)
	collector, err := newExposureCollector(dependencies, testRefreshLogger(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if collector == nil {
		t.Fatal("生产暴露 Collector 为空")
	}
	_, err = newExposureCollector(nil, testRefreshLogger(), repository)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil dependencies error=%v", err)
	}
}

type exposurePolicyCall struct {
	at       time.Time
	deadline time.Time
}

type exposurePolicyProbe struct {
	mu    sync.Mutex
	calls []exposurePolicyCall
}

func (p *exposurePolicyProbe) RoundTrip(request *http.Request) (*http.Response, error) {
	deadline, _ := request.Context().Deadline()
	p.mu.Lock()
	p.calls = append(p.calls, exposurePolicyCall{at: time.Now(), deadline: deadline})
	p.mu.Unlock()
	return exposureHTTPResponse(request, http.StatusServiceUnavailable, "temporary", nil), nil
}

func (p *exposurePolicyProbe) snapshot() []exposurePolicyCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]exposurePolicyCall(nil), p.calls...)
}

type exposurePolicyProbes struct {
	boundary       *exposurePolicyProbe
	population     *exposurePolicyProbe
	infrastructure *exposurePolicyProbe
}

func newExposurePolicyProbes() exposurePolicyProbes {
	return exposurePolicyProbes{boundary: &exposurePolicyProbe{}, population: &exposurePolicyProbe{},
		infrastructure: &exposurePolicyProbe{}}
}

func (p exposurePolicyProbes) bases() exposureHTTPBases {
	return exposureHTTPBases{boundary: &http.Client{Transport: p.boundary},
		population:     &http.Client{Transport: p.population},
		infrastructure: &http.Client{Transport: p.infrastructure}}
}

func (p exposurePolicyProbes) all() []*exposurePolicyProbe {
	return []*exposurePolicyProbe{p.boundary, p.population, p.infrastructure}
}

func assertProductionHTTPPolicy(t *testing.T, clientSelector func(exposureHTTPClients) *httpclient.Client,
	probeSelector func(exposurePolicyProbes) *exposurePolicyProbe, rawURL string,
	timeout, interval time.Duration, attempts int,
) {
	t.Helper()
	probes := newExposurePolicyProbes()
	clients := buildExposureHTTPClients(testRefreshLogger(), defaultExposureHTTPPolicies(), probes.bases())
	_, err := clientSelector(clients).Do(context.Background(), httpclient.Request{Method: http.MethodGet, URL: rawURL})
	if err == nil {
		t.Fatal("供应商 503 未返回错误")
	}
	selected := probeSelector(probes)
	calls := selected.snapshot()
	if len(calls) != attempts {
		t.Fatalf("尝试次数=%d, want %d", len(calls), attempts)
	}
	for _, probe := range probes.all() {
		if probe != selected && len(probe.snapshot()) != 0 {
			t.Fatal("供应商 HTTP 底层客户端映射错误")
		}
	}
	assertPolicyDeadlines(t, calls, timeout)
	assertPolicyIntervals(t, calls, interval)
}

func assertPolicyDeadlines(t *testing.T, calls []exposurePolicyCall, timeout time.Duration) {
	t.Helper()
	for _, call := range calls {
		remaining := call.deadline.Sub(call.at)
		if call.deadline.IsZero() || remaining < timeout-2*time.Second || remaining > timeout+time.Second {
			t.Fatalf("HTTP deadline=%s, want timeout %s", remaining, timeout)
		}
	}
}

func assertPolicyIntervals(t *testing.T, calls []exposurePolicyCall, interval time.Duration) {
	t.Helper()
	for index := 1; index < len(calls); index++ {
		if elapsed := calls[index].at.Sub(calls[index-1].at); elapsed+25*time.Millisecond < interval {
			t.Fatalf("请求间隔=%s, want >=%s", elapsed, interval-25*time.Millisecond)
		}
	}
}

type exposureProviderTransports struct {
	boundary       *exposureProviderTransport
	population     *exposureProviderTransport
	infrastructure *exposureProviderTransport
}

func newExposureProviderTransports() exposureProviderTransports {
	return exposureProviderTransports{
		boundary:       &exposureProviderTransport{kind: "boundary"},
		population:     &exposureProviderTransport{kind: "population"},
		infrastructure: &exposureProviderTransport{kind: "infrastructure"},
	}
}

type exposureProviderTransport struct {
	kind     string
	mu       sync.Mutex
	requests []string
}

func (p *exposureProviderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request.Method+" "+request.URL.String())
	p.mu.Unlock()
	switch p.kind {
	case "boundary":
		return boundaryProviderResponse(request)
	case "population":
		return populationProviderResponse(request)
	case "infrastructure":
		return infrastructureProviderResponse(request)
	default:
		return nil, fmt.Errorf("未知 provider transport %q", p.kind)
	}
}

func (p *exposureProviderTransport) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func providerTestClient(transport http.RoundTripper) *httpclient.Client {
	return buildExposureHTTPClient(testRefreshLogger(), exposureHTTPPolicy{
		timeout: time.Second, interval: time.Nanosecond, maxAttempts: 1,
	}, &http.Client{Transport: transport})
}

func boundaryProviderResponse(request *http.Request) (*http.Response, error) {
	switch request.URL.String() {
	case "https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM0/":
		body := `{"boundaryID":"CHN-ADM0-351020","boundaryName":"China","boundaryISO":"CHN",` +
			`"boundaryYearRepresented":"2019","boundaryType":"ADM0",` +
			`"boundarySource":"geoBoundaries, Wikimedia Commons",` +
			`"boundaryLicense":"Public Domain","simplifiedGeometryGeoJSON":` +
			`"https://github.com/wmgeolab/geoBoundaries/raw/abcdef1/releaseData/gbOpen/CHN/ADM0/` +
			`geoBoundaries-CHN-ADM0_simplified.geojson"}`
		return exposureHTTPResponse(request, http.StatusOK, body, nil), nil
	case "https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/abcdef1/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson":
		body := `{"type":"FeatureCollection","crs":{"type":"name","properties":` +
			`{"name":"urn:ogc:def:crs:OGC:1.3:CRS84"}},"features":[{"type":"Feature","properties":` +
			`{"shapeID":"351020B351020","shapeName":"China","shapeISO":"CHN",` +
			`"shapeGroup":"CHN","shapeType":"ADM0"},` +
			`"geometry":{"type":"MultiPolygon","coordinates":[[[[73,18],[135,18],[135,54],[73,54],[73,18]]]]}}]}`
		return exposureHTTPResponse(request, http.StatusOK, body, nil), nil
	default:
		return nil, fmt.Errorf("geoBoundaries 请求端点错误: %s", request.URL)
	}
}

func populationProviderResponse(request *http.Request) (*http.Response, error) {
	const taskID = "123e4567-e89b-42d3-a456-426614174000"
	switch request.Method + " " + request.URL.String() {
	case "POST https://api.worldpop.org/v2/population":
		return exposureHTTPResponse(request, http.StatusOK,
			`{"task_id":"`+taskID+`","status":"pending"}`, nil), nil
	case "GET https://api.worldpop.org/v2/tasks/" + taskID:
		body := `{"task_id":"` + taskID + `","status":"success","progress":100,"stage":"Completed","result":` +
			`{"total_population":1200,"area_km2":1,"data_year":2026,` +
			`"data_source":"WorldPop Global 2 Population Data","population_density":1200,` +
			`"processing_time_ms":1850},"error":null}`
		return exposureHTTPResponse(request, http.StatusOK, body, nil), nil
	default:
		return nil, fmt.Errorf("WorldPop 请求端点错误: %s %s", request.Method, request.URL)
	}
}

func infrastructureProviderResponse(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || request.URL.String() != "https://overpass-api.de/api/interpreter" {
		return nil, fmt.Errorf("Overpass 请求端点错误: %s %s", request.Method, request.URL)
	}
	body := `{"version":0.6,"generator":"Overpass API","osm3s":` +
		`{"timestamp_osm_base":"2020-01-01T00:00:00Z"},"elements":[` +
		`{"type":"way","id":1,"tags":{"highway":"primary"},"geometry":` +
		`[{"lat":30,"lon":104},{"lat":30.001,"lon":104.001}]},` +
		`{"type":"node","id":2,"lat":30,"lon":104,"tags":{"amenity":"hospital"}}]}`
	return exposureHTTPResponse(request, http.StatusOK, body, nil), nil
}

func assertProviderRequests(t *testing.T, values exposureProviderTransports) {
	t.Helper()
	wantBoundary := []string{
		"GET https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM0/",
		"GET https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/abcdef1/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson",
	}
	wantPopulation := []string{"POST https://api.worldpop.org/v2/population",
		"GET https://api.worldpop.org/v2/tasks/123e4567-e89b-42d3-a456-426614174000"}
	wantInfrastructure := []string{"POST https://overpass-api.de/api/interpreter"}
	if !slices.Equal(values.boundary.snapshot(), wantBoundary) ||
		!slices.Equal(values.population.snapshot(), wantPopulation) ||
		!slices.Equal(values.infrastructure.snapshot(), wantInfrastructure) {
		t.Fatalf("provider 请求映射 boundary=%v population=%v infrastructure=%v",
			values.boundary.snapshot(), values.population.snapshot(), values.infrastructure.snapshot())
	}
}

func exposureHTTPResponse(request *http.Request, status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)),
		Request: request}
}

func populationQueryGeometry() json.RawMessage {
	return json.RawMessage(`{"type":"Polygon","coordinates":[[[104,30],[104.01,30],` +
		`[104.01,30.01],[104,30.01],[104,30]]]}`)
}

type worldPopRedirectTransport struct {
	mode           string
	redirectStatus int
	mu             sync.Mutex
	requests       []string
}

func (p *worldPopRedirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request.Method+" "+request.URL.String())
	p.mu.Unlock()
	if p.mode == "create" {
		return p.createResponse(request)
	}
	return p.pollResponse(request)
}

func (p *worldPopRedirectTransport) createResponse(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || request.URL.Path != "/v2/population" {
		return nil, fmt.Errorf("WorldPop 创建请求被重放: %s %s", request.Method, request.URL)
	}
	header := make(http.Header)
	header.Set("Location", "https://api.worldpop.org/v2/population-v2")
	return exposureHTTPResponse(request, p.redirectStatus, "", header), nil
}

func (p *worldPopRedirectTransport) pollResponse(request *http.Request) (*http.Response, error) {
	const taskID = "123e4567-e89b-42d3-a456-426614174000"
	switch request.Method + " " + request.URL.Path {
	case "POST /v2/population":
		return exposureHTTPResponse(request, http.StatusOK,
			`{"task_id":"`+taskID+`","status":"pending"}`, nil), nil
	case "GET /v2/tasks/" + taskID:
		header := make(http.Header)
		header.Set("Location", "https://api.worldpop.org/v2/tasks/"+taskID+"/result")
		return exposureHTTPResponse(request, http.StatusTemporaryRedirect, "", header), nil
	case "GET /v2/tasks/" + taskID + "/result":
		body := `{"task_id":"` + taskID + `","status":"success","progress":100,"stage":"Completed","result":` +
			`{"total_population":1200,"area_km2":1,"data_year":2026,` +
			`"data_source":"WorldPop Global 2 Population Data","population_density":1200,` +
			`"processing_time_ms":1850},"error":null}`
		return exposureHTTPResponse(request, http.StatusOK, body, nil), nil
	default:
		return nil, fmt.Errorf("WorldPop 轮询请求错误: %s %s", request.Method, request.URL)
	}
}

func (p *worldPopRedirectTransport) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func testWorldPopCreateRedirectDenied(t *testing.T) {
	statuses := []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			transport := &worldPopRedirectTransport{mode: "create", redirectStatus: status}
			provider := worldPopProviderWithProductionClient(t, transport)
			_, err := provider.Population(context.Background(), exposurecollection.PopulationQuery{
				Geometry: populationQueryGeometry(), ExpectedAreaSquareMeter: 1_000_000, Year: 2026,
			})
			want := []string{"POST https://api.worldpop.org/v2/population"}
			if !errors.Is(err, domain.ErrProviderUnavailable) || !slices.Equal(transport.snapshot(), want) {
				t.Fatalf("创建重定向 status=%d error=%v requests=%v", status, err, transport.snapshot())
			}
		})
	}
}

func testWorldPopPollRedirectAllowed(t *testing.T) {
	transport := &worldPopRedirectTransport{mode: "poll"}
	provider := worldPopProviderWithProductionClient(t, transport)
	value, err := provider.Population(context.Background(), exposurecollection.PopulationQuery{
		Geometry: populationQueryGeometry(), ExpectedAreaSquareMeter: 1_000_000, Year: 2026,
	})
	const taskID = "123e4567-e89b-42d3-a456-426614174000"
	want := []string{"POST https://api.worldpop.org/v2/population",
		"GET https://api.worldpop.org/v2/tasks/" + taskID,
		"GET https://api.worldpop.org/v2/tasks/" + taskID + "/result"}
	if err != nil || value.TaskID != taskID || !slices.Equal(transport.snapshot(), want) {
		t.Fatalf("轮询重定向 value=%+v error=%v requests=%v", value, err, transport.snapshot())
	}
}

func worldPopProviderWithProductionClient(t *testing.T,
	transport http.RoundTripper,
) exposurecollection.PopulationProvider {
	t.Helper()
	base := &http.Client{Transport: transport}
	clients := buildExposureHTTPClients(testRefreshLogger(), defaultExposureHTTPPolicies(), exposureHTTPBases{
		boundary: base, population: base, infrastructure: base,
	})
	providers, err := newExposureProviders(clients)
	if err != nil {
		t.Fatal(err)
	}
	return providers.population
}

type overpassRedirectTransport struct {
	redirectStatus int
	mu             sync.Mutex
	requests       []string
}

func (p *overpassRedirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request.Method+" "+request.URL.String())
	p.mu.Unlock()
	if request.URL.Path == "/api/interpreter" {
		header := make(http.Header)
		header.Set("Location", "https://overpass-api.de/sink")
		return exposureHTTPResponse(request, p.redirectStatus, "", header), nil
	}
	if request.URL.Path == "/sink" {
		return infrastructureProviderResponseAtSink(request), nil
	}
	return nil, fmt.Errorf("Overpass 重定向请求端点错误: %s %s", request.Method, request.URL)
}

func (p *overpassRedirectTransport) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func infrastructureProviderResponseAtSink(request *http.Request) *http.Response {
	body := `{"version":0.6,"generator":"Overpass API","osm3s":` +
		`{"timestamp_osm_base":"2020-01-01T00:00:00Z"},"elements":[` +
		`{"type":"node","id":2,"lat":30,"lon":104,"tags":{"amenity":"hospital"}}]}`
	return exposureHTTPResponse(request, http.StatusOK, body, nil)
}

func overpassProviderWithProductionClient(t *testing.T,
	transport http.RoundTripper,
) exposurecollection.InfrastructureProvider {
	t.Helper()
	base := &http.Client{Transport: transport}
	clients := buildExposureHTTPClients(testRefreshLogger(), defaultExposureHTTPPolicies(), exposureHTTPBases{
		boundary: base, population: base, infrastructure: base,
	})
	providers, err := newExposureProviders(clients)
	if err != nil {
		t.Fatal(err)
	}
	return providers.infrastructure
}

type exposurePortProbe struct {
	now            time.Time
	input          exposurecollection.GeometryInput
	boundary       exposurecollection.AdministrativeBoundary
	administration exposurecollection.AdministrativeProjection
	population     exposurecollection.PopulationResult
	infrastructure exposurecollection.InfrastructureResult
	projected      []applicationloss.LossExposureFeature
	calls          []string
	saved          exposurecollection.ExposureProjection
}

func newExposurePortProbe() *exposurePortProbe {
	now := time.Date(2026, 8, 28, 12, 5, 0, 0, time.UTC)
	input := exposureGeometryFixture(now)
	return &exposurePortProbe{now: now, input: input, boundary: exposureBoundaryFixture(now),
		administration: exposureAdministrationFixture(input), population: exposurePopulationFixture(now),
		infrastructure: exposureInfrastructureFixture(now), projected: exposureProjectedFeatures(input.Zones)}
}

func (p *exposurePortProbe) ReadExposureGeometry(context.Context,
	string, string,
) (exposurecollection.GeometryInput, error) {
	p.calls = append(p.calls, "geometry")
	return p.input, nil
}

func (p *exposurePortProbe) Boundary(context.Context) (exposurecollection.AdministrativeBoundary, error) {
	p.calls = append(p.calls, "boundary")
	return p.boundary, nil
}

func (p *exposurePortProbe) ProjectAdministration(context.Context, exposurecollection.GeometryInput,
	exposurecollection.AdministrativeBoundary, exposurecollection.GeometryProjectionLimits,
) (exposurecollection.AdministrativeProjection, error) {
	p.calls = append(p.calls, "administrator")
	return p.administration, nil
}

func (p *exposurePortProbe) Population(context.Context,
	exposurecollection.PopulationQuery,
) (exposurecollection.PopulationResult, error) {
	p.calls = append(p.calls, "population")
	return p.population, nil
}

func (p *exposurePortProbe) Infrastructure(context.Context,
	exposurecollection.InfrastructureQuery,
) (exposurecollection.InfrastructureResult, error) {
	p.calls = append(p.calls, "infrastructure")
	return p.infrastructure, nil
}

func (p *exposurePortProbe) ProjectInfrastructure(context.Context,
	exposurecollection.AdministrativeProjection, []exposurecollection.RawInfrastructureFeature,
	exposurecollection.GeometryProjectionLimits,
) ([]applicationloss.LossExposureFeature, error) {
	p.calls = append(p.calls, "projector")
	return append([]applicationloss.LossExposureFeature(nil), p.projected...), nil
}

func (p *exposurePortProbe) SaveExposureProjection(_ context.Context,
	value exposurecollection.ExposureProjection,
) error {
	p.calls = append(p.calls, "writer")
	p.saved = value
	return nil
}

func (p *exposurePortProbe) Now() time.Time { return p.now }

func exposureGeometryFixture(now time.Time) exposurecollection.GeometryInput {
	geometry := json.RawMessage(`{"type":"Polygon","coordinates":` +
		`[[[116,39],[116.1,39],[116.1,39.1],[116,39]]]}`)
	snapshot := hazard.Snapshot{ID: "snapshot-composition", HazardType: hazard.TypeLandslide,
		ModelName: "LHASA", ModelVersion: "2", RunAt: now.Add(-time.Hour),
		ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour), Status: hazard.SnapshotAvailable,
		Source: provenance.Provenance{Provider: "NASA", Dataset: "LHASA",
			SourceURI: "https://example.test/lhasa", DataKind: provenance.DataKindNowcast,
			FetchedAt: now.Add(-time.Hour), ValidFrom: now.Add(-time.Hour), ValidTo: now.Add(time.Hour)}}
	zones := []applicationloss.LossRiskZone{{ID: "zone-composition", SnapshotID: snapshot.ID,
		Level: hazard.RiskHigh, AreaSquareM: 1_000_000, AreaCalculated: true}}
	analysis := applicationloss.LossSpatialProjection{ID: "spatial-" + strings.Repeat("a", 64),
		Version: "spatial-v1", Digest: strings.Repeat("a", 64), SnapshotID: snapshot.ID,
		Status: spatialanalysis.AnalysisAreaOnly, TotalAreaSquareMeters: 1_000_000,
		CalculatedAt: now.Add(-30 * time.Minute), InputReferences: []string{"risk-zone:zone-composition"},
		DatasetReferences: []string{"https://example.test/lhasa"}}
	return exposurecollection.GeometryInput{Snapshot: snapshot, Zones: zones, Analysis: analysis,
		UnionGeometry: geometry, Bounds: exposurecollection.Bounds{South: 39, West: 116, North: 39.1, East: 116.1},
		Stats: exposurecollection.GeometryStats{ZoneCount: 1, UnionGeometryBytes: int64(len(geometry)),
			MaxZonePoints: 4, TotalZonePoints: 4}}
}

func exposureBoundaryFixture(now time.Time) exposurecollection.AdministrativeBoundary {
	geometry := json.RawMessage(`{"type":"MultiPolygon","coordinates":` +
		`[[[[116,39],[116.1,39],[116.1,39.1],[116,39]]]]}`)
	return exposurecollection.AdministrativeBoundary{BoundaryID: "CHN-ADM0-351020", RegionCode: "CN",
		BoundaryType: "ADM0", BoundaryYear: "2019", Source: "geoBoundaries, Wikimedia Commons",
		License: "Public Domain",
		Digest:  strings.Repeat("b", 64), Reference: "https://github.com/wmgeolab/boundary.geojson",
		Geometry: geometry, CollectedAt: now.Add(-time.Minute),
		InputReferences: []string{"https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM0/"}}
}

func exposureAdministrationFixture(input exposurecollection.GeometryInput) exposurecollection.AdministrativeProjection {
	zones := append([]applicationloss.LossRiskZone(nil), input.Zones...)
	zones[0].AreaSquareM, zones[0].AdminCodes = 900_000, []string{"CN"}
	boundary := exposureBoundaryFixture(input.Snapshot.ValidFrom.Add(time.Hour))
	return exposurecollection.AdministrativeProjection{AnalysisID: input.Analysis.ID,
		SnapshotID: input.Snapshot.ID, RegionCode: "CN", BoundaryID: boundary.BoundaryID,
		BoundaryDigest: boundary.Digest, BoundaryReference: boundary.Reference,
		BoundaryGeometry: boundary.Geometry, UnionGeometry: input.UnionGeometry, Bounds: input.Bounds,
		TotalAreaSquareMeters: 900_000, Zones: zones}
}

func exposurePopulationFixture(now time.Time) exposurecollection.PopulationResult {
	return exposurecollection.PopulationResult{TaskID: "123e4567-e89b-42d3-a456-426614174000",
		Total: 1234.5, AreaKM2: 0.9, DataYear: 2026, DataSource: "WorldPop Global 2 Population Data",
		DatasetIdentity: "urn:worldpop:global-annual-population:100m:v2", CollectedAt: now.Add(-time.Minute),
		ValidFrom:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ValidTo:         time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		InputReferences: []string{"https://api.worldpop.org/v2/tasks/123e4567-e89b-42d3-a456-426614174000"}}
}

func exposureInfrastructureFixture(now time.Time) exposurecollection.InfrastructureResult {
	return exposurecollection.InfrastructureResult{OSMBaseTimestamp: now.Add(-2 * time.Minute),
		CollectedAt: now.Add(-time.Minute), ValidFrom: now.Add(-2 * time.Minute), ValidTo: now.Add(24 * time.Hour),
		InputReferences: []string{"https://www.openstreetmap.org", "urn:openstreetmap:osm-base:test"},
		Features: []exposurecollection.RawInfrastructureFeature{{FeatureID: "osm-road-way-1",
			Kind:            applicationloss.LossFeatureRoad,
			Geometry:        json.RawMessage(`{"type":"LineString","coordinates":[[116,39],[116.1,39.1]]}`),
			InputReferences: []string{"https://www.openstreetmap.org/way/1"}}}}
}

func exposureProjectedFeatures(zones []applicationloss.LossRiskZone) []applicationloss.LossExposureFeature {
	zoneIDs := []string{zones[0].ID}
	return []applicationloss.LossExposureFeature{
		{FeatureID: "osm-facility-node-2", Kind: applicationloss.LossFeatureFacility,
			ZoneIDs: zoneIDs, Quantity: 1, Unit: "count", CoverageRatio: 1,
			Status: spatialanalysis.MetricAvailable, Provided: true,
			InputReferences: []string{"https://www.openstreetmap.org/node/2"}},
		{FeatureID: "osm-road-way-1", Kind: applicationloss.LossFeatureRoad,
			ZoneIDs: zoneIDs, Quantity: 120, Unit: "meters", CoverageRatio: 1,
			Status: spatialanalysis.MetricAvailable, Provided: true,
			InputReferences: []string{"https://www.openstreetmap.org/way/1"}},
	}
}
