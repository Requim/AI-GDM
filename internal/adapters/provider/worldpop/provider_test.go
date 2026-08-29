package worldpop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
)

const testTaskID = "123e4567-e89b-42d3-a456-426614174000"

func TestPopulationPollsFixedSameOriginPath(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	requests := make([]string, 0, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/population":
			assertPopulationRequest(t, request)
			writeJSON(t, writer, map[string]any{"task_id": testTaskID, "status": "pending",
				"message": "ok", "check_url": "https://attacker.test/tasks/stolen"})
		case "/tasks/" + testTaskID:
			writeJSON(t, writer, successTask())
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	provider := testProvider(t, server, now)
	result, err := provider.Population(context.Background(), testPopulationQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID != testTaskID || result.Total != 1234.5 ||
		len(requests) != 2 || requests[1] != "/tasks/"+testTaskID || len(result.Limitations) != 1 ||
		!strings.Contains(result.Limitations[0], defaultDatasetID) ||
		!strings.Contains(result.Limitations[0], "data_source") {
		t.Fatalf("Population()=%+v requests=%v", result, requests)
	}
}

func TestDecodeTaskRejectsDuplicateUnknownAndContradictoryWire(t *testing.T) {
	result := `{"total_population":1234.5,"area_km2":0.0001,"data_year":2026,` +
		`"data_source":"WorldPop Global 2 Population Data","population_density":12345000,` +
		`"processing_time_ms":1850}`
	prefix := `{"task_id":"` + testTaskID + `",`
	cases := map[string]string{
		"duplicate-status":       prefix + `"status":"failure","status":"success","stage":"Completed","result":` + result + `,"error":null}`,
		"case-variant-status":    prefix + `"status":"failure","Status":"success","result":null,"error":"failed"}`,
		"duplicate-result":       prefix + `"status":"success","stage":"Completed","result":null,"result":` + result + `,"error":null}`,
		"duplicate-error":        prefix + `"status":"failure","result":null,"error":"first","error":"second"}`,
		"unknown-field":          prefix + `"status":"success","stage":"Completed","result":` + result + `,"error":null,"debug":true}`,
		"success-with-error":     prefix + `"status":"success","stage":"Completed","result":` + result + `,"error":"partial"}`,
		"failure-with-result":    prefix + `"status":"failure","result":` + result + `,"error":"failed"}`,
		"pending-with-result":    prefix + `"status":"pending","result":` + result + `,"error":null}`,
		"success-without-result": prefix + `"status":"success","stage":"Completed","result":null,"error":null}`,
		"failure-without-error":  prefix + `"status":"failure","result":null,"error":null}`,
		"duplicate-result-field": prefix + `"status":"success","stage":"Completed","result":{"total_population":1,` +
			`"total_population":2,"area_km2":0.0001,"data_year":2026,"data_source":"WorldPop",` +
			`"population_density":10000,"processing_time_ms":1},"error":null}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeTask([]byte(payload), testTaskID)
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("decodeTask() error=%v", err)
			}
		})
	}
}

func TestDecodeTaskAcceptsCurrentProductionSuccessFixture(t *testing.T) {
	payload, err := os.ReadFile("testdata/population_success.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeTask(payload, testTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Stage == nil || *value.Stage != "Completed" || value.Result == nil ||
		value.Result.DataSource != "WorldPop Global 2 Population Data" ||
		value.Result.PopulationDensity == nil || *value.Result.PopulationDensity != 8223.45 ||
		value.Result.ProcessingTimeMS == nil || *value.Result.ProcessingTimeMS != 1850 {
		t.Fatalf("decodeTask()=%+v", value)
	}
}

func TestDecodeTaskRejectsInvalidCurrentProductionFields(t *testing.T) {
	prefix := `{"task_id":"` + testTaskID + `","status":"success",`
	resultPrefix := `"result":{"total_population":1234.5,"area_km2":0.0001,"data_year":2026,` +
		`"data_source":"WorldPop Global 2 Population Data",`
	cases := map[string]string{
		"missing-stage": prefix + `"result":{"total_population":1234.5,"area_km2":0.0001,"data_year":2026,` +
			`"data_source":"WorldPop","population_density":1,"processing_time_ms":1},"error":null}`,
		"stage-type": prefix + `"stage":1,` + resultPrefix +
			`"population_density":1,"processing_time_ms":1},"error":null}`,
		"blank-stage": prefix + `"stage":" ",` + resultPrefix +
			`"population_density":1,"processing_time_ms":1},"error":null}`,
		"density-type": prefix + `"stage":"Completed",` + resultPrefix +
			`"population_density":"1","processing_time_ms":1},"error":null}`,
		"density-negative": prefix + `"stage":"Completed",` + resultPrefix +
			`"population_density":-1,"processing_time_ms":1},"error":null}`,
		"processing-type": prefix + `"stage":"Completed",` + resultPrefix +
			`"population_density":1,"processing_time_ms":"1"},"error":null}`,
		"processing-negative": prefix + `"stage":"Completed",` + resultPrefix +
			`"population_density":1,"processing_time_ms":-1},"error":null}`,
		"duplicate-stage": prefix + `"stage":"Completed","stage":"Completed",` + resultPrefix +
			`"population_density":1,"processing_time_ms":1},"error":null}`,
		"duplicate-density": prefix + `"stage":"Completed",` + resultPrefix +
			`"population_density":1,"population_density":2,"processing_time_ms":1},"error":null}`,
		"duplicate-processing": prefix + `"stage":"Completed",` + resultPrefix +
			`"population_density":1,"processing_time_ms":1,"processing_time_ms":2},"error":null}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeTask([]byte(payload), testTaskID)
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("decodeTask() error=%v", err)
			}
		})
	}
}

func TestDecodeTaskRejectsMissingTotalPopulation(t *testing.T) {
	payload := `{"task_id":"` + testTaskID + `","status":"success","stage":"Completed","result":` +
		`{"area_km2":1,"data_year":2026,"data_source":"WorldPop Global 2 Population Data",` +
		`"population_density":0,"processing_time_ms":1},"error":null}`
	if _, err := decodeTask([]byte(payload), testTaskID); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("decodeTask() error=%v", err)
	}
}

func TestDecodeTaskRejectsNullTotalPopulation(t *testing.T) {
	payload := `{"task_id":"` + testTaskID + `","status":"success","stage":"Completed","result":` +
		`{"total_population":null,"area_km2":1,"data_year":2026,` +
		`"data_source":"WorldPop Global 2 Population Data","population_density":0,` +
		`"processing_time_ms":1},"error":null}`
	if _, err := decodeTask([]byte(payload), testTaskID); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("decodeTask() error=%v", err)
	}
}

func TestPopulationAcceptsExplicitZeroTotalPopulation(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/population" {
			writeJSON(t, writer, map[string]any{"task_id": testTaskID, "status": "pending"})
			return
		}
		value := successTask()
		value["result"].(map[string]any)["total_population"] = 0
		value["result"].(map[string]any)["population_density"] = 0
		writeJSON(t, writer, value)
	}))
	defer server.Close()
	result, err := testProvider(t, server, now).Population(context.Background(), testPopulationQuery())
	if err != nil || result.Total != 0 {
		t.Fatalf("Population()=%+v error=%v", result, err)
	}
}

func TestDecodeSubmissionRejectsDuplicateAndUnknownFields(t *testing.T) {
	for name, payload := range map[string]string{
		"duplicate": `{"task_id":"` + testTaskID + `","status":"pending","status":"received"}`,
		"unknown":   `{"task_id":"` + testTaskID + `","status":"pending","debug":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSubmission([]byte(payload))
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("decodeSubmission() error=%v", err)
			}
		})
	}
}

func TestPopulationRejectsInvalidTaskID(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(t, writer, map[string]any{"task_id": "../../secret", "status": "pending",
			"message": "ok", "check_url": "/tasks/bad"})
	}))
	defer server.Close()
	provider := testProvider(t, server, now)
	if _, err := provider.Population(context.Background(), testPopulationQuery()); err == nil {
		t.Fatal("Population() 未拒绝非法 task_id")
	}
}

func TestPopulationStopsAfterBoundedPolls(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/population" {
			writeJSON(t, writer, map[string]any{"task_id": testTaskID, "status": "pending",
				"message": "ok", "check_url": "/ignored"})
			return
		}
		writeJSON(t, writer, map[string]any{"task_id": testTaskID, "status": "progress", "result": nil})
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 1,
		Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client, BaseURL: server.URL, MaxPolls: 2,
		PollInterval: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Population(context.Background(), testPopulationQuery()); err == nil {
		t.Fatal("Population() 未在固定轮询次数后失败")
	}
}

func TestPopulationAcceptsOfficialAndMachineDataSourceLabels(t *testing.T) {
	for _, source := range []string{"worldpop_R2025A_2026_100m", "WorldPop Global 100m population 2026"} {
		t.Run(source, func(t *testing.T) {
			now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/population" {
					writeJSON(t, writer, map[string]any{"task_id": testTaskID, "status": "pending"})
					return
				}
				value := successTask()
				value["result"].(map[string]any)["data_source"] = source
				writeJSON(t, writer, value)
			}))
			defer server.Close()
			result, err := testProvider(t, server, now).Population(context.Background(), testPopulationQuery())
			if err != nil || result.DataSource != source || result.DatasetIdentity != defaultDatasetID ||
				len(result.Limitations) != 1 || !strings.Contains(result.Limitations[0], source) {
				t.Fatalf("Population()=%+v error=%v", result, err)
			}
		})
	}
}

func TestPopulationPostIsNeverRetried(t *testing.T) {
	attempts := 0
	sentinel := errors.New("connection dropped after write")
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, sentinel
	})}, MaxAttempts: 3, Sleep: func(context.Context, time.Duration) error { return nil }})
	provider, err := New(Options{Client: client, BaseURL: "https://worldpop.example/v2",
		MaxPolls: 2, PollInterval: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Population(context.Background(), testPopulationQuery())
	if !errors.Is(err, sentinel) || attempts != 1 {
		t.Fatalf("Population() error=%v attempts=%d", err, attempts)
	}
}

func TestPopulationTaskGetMayRetry(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	posts, gets := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/population" {
			posts++
			writeJSON(t, writer, map[string]any{"task_id": testTaskID, "status": "pending"})
			return
		}
		gets++
		if gets == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(t, writer, successTask())
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 2,
		BaseBackoff: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil },
		Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client, BaseURL: server.URL, MaxPolls: 1,
		PollInterval: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Population(context.Background(), testPopulationQuery()); err != nil || posts != 1 || gets != 2 {
		t.Fatalf("Population() error=%v posts=%d gets=%d", err, posts, gets)
	}
}

func TestPopulationPostRejectsSameOriginRedirectWithoutReplay(t *testing.T) {
	statuses := []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests++
				writer.Header().Set("Location", "/population-second")
				writer.WriteHeader(status)
			}))
			defer server.Close()
			_, err := testProvider(t, server, now).Population(context.Background(), testPopulationQuery())
			if err == nil || requests != 1 {
				t.Fatalf("Population() error=%v requests=%d", err, requests)
			}
		})
	}
}

func testProvider(t *testing.T, server *httptest.Server, now time.Time) *Provider {
	t.Helper()
	client := httpclient.New(httpclient.Options{HTTPClient: server.Client(), MaxAttempts: 1,
		Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client, BaseURL: server.URL, MaxPolls: 2,
		PollInterval: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func testPopulationQuery() exposurecollection.PopulationQuery {
	return exposurecollection.PopulationQuery{Year: 2026, ExpectedAreaSquareMeter: 100,
		Geometry: json.RawMessage(`{"type":"Polygon","coordinates":[[[116,39],[116.01,39],[116.01,39.01],[116,39]]]}`)}
}

func successTask() map[string]any {
	return map[string]any{"task_id": testTaskID, "status": "success", "progress": 100, "stage": "Completed",
		"result": map[string]any{"total_population": 1234.5, "area_km2": 0.0001,
			"data_year": 2026, "data_source": "worldpop_R2025A_2026_100m",
			"population_density": 12345000, "processing_time_ms": 1850}, "error": nil}
}

func assertPopulationRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request=%s content-type=%q", request.Method, request.Header.Get("Content-Type"))
	}
	payload, err := io.ReadAll(request.Body)
	if err != nil || !strings.Contains(string(payload), `"year":2026`) {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
