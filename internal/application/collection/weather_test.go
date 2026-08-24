package collection

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestWeatherCollectorSavesCompleteLiveBatch(t *testing.T) {
	now, points := weatherTestInput()
	live := weatherFixtures(points, now.Add(-time.Minute))
	store := &weatherStoreStub{}
	collector := newWeatherCollectorForTest(t, weatherProviderStub{values: live}, store, now)

	got, err := collector.Collect(context.Background(), points, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || store.saveCalls != 1 || len(store.saved) != 2 {
		t.Fatalf("Collect() = %d, saveCalls = %d", len(got), store.saveCalls)
	}
	if got[0].Source.Stale {
		t.Fatal("实时成功批次不应标记为回退")
	}
}

func TestWeatherCollectorFallsBackToLastSuccessfulBatch(t *testing.T) {
	now, points := weatherTestInput()
	store := &weatherStoreStub{latest: weatherFixtures(points, now.Add(-2*time.Hour))}
	provider := weatherProviderStub{err: domain.ErrProviderUnavailable}
	collector := newWeatherCollectorForTest(t, provider, store, now)

	got, err := collector.Collect(context.Background(), points, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if store.saveCalls != 0 || !got[0].Source.Stale || !hasString(got[0].Source.QualityFlags, fallbackQualityFlag) {
		t.Fatalf("Collect() fallback = %+v", got[0].Source)
	}
	got[0].Hourly[0].SoilMoistureByLayer[0] = 0.99
	if store.latest[0].Hourly[0].SoilMoistureByLayer[0] == 0.99 {
		t.Fatal("回退结果共享了仓储土壤湿度切片")
	}
}

func TestWeatherCollectorFallsBackAfterSaveFailure(t *testing.T) {
	now, points := weatherTestInput()
	store := &weatherStoreStub{
		saveErr: errors.New("database unavailable"),
		latest:  weatherFixtures(points, now.Add(-time.Hour)),
	}
	collector := newWeatherCollectorForTest(t,
		weatherProviderStub{values: weatherFixtures(points, now)}, store, now)

	got, err := collector.Collect(context.Background(), points, 0, 1)
	if err != nil || !got[0].Source.Stale || store.saveCalls != 1 {
		t.Fatalf("Collect() = %+v, %v, saveCalls=%d", got, err, store.saveCalls)
	}
}

func TestWeatherCollectorRejectsExpiredFallback(t *testing.T) {
	now, points := weatherTestInput()
	store := &weatherStoreStub{latest: weatherFixtures(points, now.Add(-7*time.Hour))}
	collector := newWeatherCollectorForTest(t,
		weatherProviderStub{err: errors.New("network error")}, store, now)

	_, err := collector.Collect(context.Background(), points, 0, 1)
	if !errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("Collect() error = %v", err)
	}
}

func TestWeatherCollectorRejectsIncompleteLiveAndFallback(t *testing.T) {
	now, points := weatherTestInput()
	store := &weatherStoreStub{latest: weatherFixtures(points[:1], now.Add(-time.Hour))}
	collector := newWeatherCollectorForTest(t,
		weatherProviderStub{values: weatherFixtures(points[:1], now)}, store, now)

	_, err := collector.Collect(context.Background(), points, 0, 1)
	if !errors.Is(err, domain.ErrInsufficientData) || store.saveCalls != 0 {
		t.Fatalf("Collect() error = %v, saveCalls=%d", err, store.saveCalls)
	}
}

func TestNewWeatherCollectorRejectsInvalidDependencies(t *testing.T) {
	_, err := NewWeatherCollector(nil, nil, nil, nil, 0)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("NewWeatherCollector() error = %v", err)
	}
}

func TestWeatherCollectorRejectsMismatchedTimeline(t *testing.T) {
	now, points := weatherTestInput()
	live := weatherFixtures(points, now)
	live[1].Hourly[0].Time = live[1].Hourly[0].Time.Add(time.Hour)
	store := &weatherStoreStub{}
	collector := newWeatherCollectorForTest(t, weatherProviderStub{values: live}, store, now)

	_, err := collector.Collect(context.Background(), points, 0, 1)
	if !errors.Is(err, domain.ErrInsufficientData) || store.saveCalls != 0 {
		t.Fatalf("Collect() error = %v, saveCalls=%d", err, store.saveCalls)
	}
}

func TestWeatherCollectorRejectsNonUTCSource(t *testing.T) {
	now, points := weatherTestInput()
	live := weatherFixtures(points, now)
	live[0].Source.FetchedAt = now.In(time.FixedZone("CST", 8*60*60))
	store := &weatherStoreStub{}
	collector := newWeatherCollectorForTest(t, weatherProviderStub{values: live}, store, now)

	_, err := collector.Collect(context.Background(), points, 0, 1)
	if !errors.Is(err, domain.ErrInsufficientData) || store.saveCalls != 0 {
		t.Fatalf("Collect() error = %v, saveCalls=%d", err, store.saveCalls)
	}
}

func TestWeatherCollectorRejectsDuplicateRequestPoint(t *testing.T) {
	now, points := weatherTestInput()
	points[1] = spatial.Point{Longitude: points[0].Longitude + 0.0000001, Latitude: points[0].Latitude}
	store := &weatherStoreStub{}
	collector := newWeatherCollectorForTest(t, weatherProviderStub{}, store, now)

	_, err := collector.Collect(context.Background(), points, 0, 1)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Collect() error = %v", err)
	}
}

func TestWeatherCollectorTreatsSignedZeroAsDuplicate(t *testing.T) {
	now, _ := weatherTestInput()
	points := []spatial.Point{
		{Longitude: 0, Latitude: 30},
		{Longitude: math.Copysign(0, -1), Latitude: 30},
	}
	store := &weatherStoreStub{}
	collector := newWeatherCollectorForTest(t, weatherProviderStub{}, store, now)

	_, err := collector.Collect(context.Background(), points, 0, 1)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Collect() error = %v", err)
	}
}

type weatherProviderStub struct {
	values []hazard.WeatherSnapshot
	err    error
}

func (s weatherProviderStub) Forecast(context.Context, []spatial.Point, int, int) ([]hazard.WeatherSnapshot, error) {
	return s.values, s.err
}

type weatherStoreStub struct {
	saved     []hazard.WeatherSnapshot
	latest    []hazard.WeatherSnapshot
	saveErr   error
	latestErr error
	saveCalls int
}

func (s *weatherStoreStub) SaveBatch(_ context.Context, values []hazard.WeatherSnapshot) error {
	s.saveCalls++
	s.saved = values
	return s.saveErr
}

func (s *weatherStoreStub) Latest(context.Context, []spatial.Point) ([]hazard.WeatherSnapshot, error) {
	return s.latest, s.latestErr
}

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

func newWeatherCollectorForTest(t *testing.T, provider weatherProviderStub,
	store *weatherStoreStub, now time.Time,
) *WeatherCollector {
	t.Helper()
	collector, err := NewWeatherCollector(provider, store, store, fixedClock{value: now}, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return collector
}

func weatherTestInput() (time.Time, []spatial.Point) {
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	return now, []spatial.Point{
		{Longitude: 104.0665, Latitude: 30.5723},
		{Longitude: 102.7123, Latitude: 25.0406},
	}
}

func weatherFixtures(points []spatial.Point, fetchedAt time.Time) []hazard.WeatherSnapshot {
	values := make([]hazard.WeatherSnapshot, 0, len(points))
	for _, point := range points {
		values = append(values, hazard.WeatherSnapshot{
			Location: point,
			Hourly: []hazard.WeatherPoint{{
				Time: fetchedAt.Truncate(time.Hour).UTC(), PrecipitationMM: 1.2, RainMM: 1,
				ShowersMM: 0.2, SoilMoistureByLayer: []float64{0.1, 0.2, 0.3, 0.4, 0.5},
			}},
			Source: provenance.Provenance{
				Provider: "Open-Meteo", Dataset: "forecast", SourceURI: "https://api.open-meteo.com/v1/forecast",
				DataKind: provenance.DataKindForecast, FetchedAt: fetchedAt.UTC(),
			},
		})
	}
	return values
}

func hasString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
