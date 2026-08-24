package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestWeatherPointSetUsesSortedSixDecimalKeys(t *testing.T) {
	points := []spatial.Point{
		{Longitude: 116.1234564, Latitude: 39.0000004},
		{Longitude: 110.5, Latitude: -0.0000001},
	}
	forward, err := newWeatherPointSet(points)
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := newWeatherPointSet([]spatial.Point{points[1], points[0]})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"116.123456,39.000000", "110.500000,0.000000"}
	if !reflect.DeepEqual(forward.keys, wantKeys) {
		t.Fatalf("keys = %#v", forward.keys)
	}
	if forward.hash != reverse.hash || len(forward.hash) != sha256HexLength {
		t.Fatalf("hashes = %q, %q", forward.hash, reverse.hash)
	}
}

func TestWeatherPointSetRejectsInvalidPoints(t *testing.T) {
	tests := map[string][]spatial.Point{
		"empty":     nil,
		"duplicate": {{Longitude: 116.0000001, Latitude: 39}, {Longitude: 116.0000004, Latitude: 39}},
		"nan":       {{Longitude: math.NaN(), Latitude: 39}},
		"infinite":  {{Longitude: 116, Latitude: math.Inf(1)}},
		"range":     {{Longitude: 181, Latitude: 39}},
	}
	for name, points := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newWeatherPointSet(points)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestWeatherRepositorySaveBatchCommitsAllPoints(t *testing.T) {
	tx := &fakeWeatherTransaction{batchID: 41}
	store := &fakeWeatherStore{tx: tx}
	repository := &WeatherRepository{store: store}
	snapshots := []hazard.WeatherSnapshot{
		weatherFixture(spatial.Point{Longitude: 116, Latitude: 39}, 1),
		weatherFixture(spatial.Point{Longitude: 117, Latitude: 40}, 2),
	}
	if err := repository.SaveBatch(context.Background(), snapshots); err != nil {
		t.Fatal(err)
	}
	if !tx.committed || tx.execCount != len(snapshots) {
		t.Fatalf("committed=%v execCount=%d", tx.committed, tx.execCount)
	}
	if got := tx.batchArgs[1]; got != len(snapshots) {
		t.Fatalf("pointCount = %v", got)
	}
}

func TestWeatherRepositorySaveBatchRollsBackAndPreservesError(t *testing.T) {
	sentinel := errors.New("insert failed")
	tx := &fakeWeatherTransaction{batchID: 42, execErrAt: 2, execErr: sentinel}
	repository := &WeatherRepository{store: &fakeWeatherStore{tx: tx}}
	snapshots := []hazard.WeatherSnapshot{
		weatherFixture(spatial.Point{Longitude: 116, Latitude: 39}, 1),
		weatherFixture(spatial.Point{Longitude: 117, Latitude: 40}, 2),
	}
	err := repository.SaveBatch(context.Background(), snapshots)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
}

func TestWeatherRepositoryLatestKeepsRequestedOrder(t *testing.T) {
	points := []spatial.Point{{Longitude: 116, Latitude: 39}, {Longitude: 117, Latitude: 40}}
	rows := &fakeWeatherRows{values: []weatherRowValue{
		weatherRow(t, weatherFixture(points[0], 10)),
		weatherRow(t, weatherFixture(points[1], 20)),
	}}
	store := &fakeWeatherStore{rows: rows}
	repository := &WeatherRepository{store: store}
	values, err := repository.Latest(context.Background(), points)
	if err != nil {
		t.Fatal(err)
	}
	if values[0].Hourly[0].RainMM != 10 || values[1].Hourly[0].RainMM != 20 {
		t.Fatalf("Latest() = %+v", values)
	}
	wantKeys := []string{"116.000000,39.000000", "117.000000,40.000000"}
	if got := store.queryArgs[1]; !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("query keys = %#v", got)
	}
}

func TestWeatherRepositoryLatestReportsDomainErrors(t *testing.T) {
	points := []spatial.Point{{Longitude: 116, Latitude: 39}, {Longitude: 117, Latitude: 40}}
	tests := []struct {
		name string
		rows []weatherRowValue
		want error
	}{
		{name: "missing", want: domain.ErrNotFound},
		{name: "incomplete", rows: []weatherRowValue{weatherRow(t, weatherFixture(points[0], 1))}, want: domain.ErrInsufficientData},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &WeatherRepository{store: &fakeWeatherStore{rows: &fakeWeatherRows{values: test.rows}}}
			_, err := repository.Latest(context.Background(), points)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestWeatherRepositorySQLContracts(t *testing.T) {
	checks := map[string]bool{
		"PostGIS WGS84 Point": strings.Contains(insertWeatherSnapshotSQL, "ST_SetSRID(ST_MakePoint($3,$4),4326)"),
		"caller order":        strings.Contains(selectLatestWeatherSQL, "WITH ORDINALITY"),
		"complete batch":      strings.Count(selectLatestWeatherSQL, "COUNT(*)") == 2,
		"JSONB hourly":        strings.Contains(weatherMigrationSQL(t), "hourly JSONB NOT NULL"),
		"JSONB source":        strings.Contains(weatherMigrationSQL(t), "source JSONB NOT NULL"),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("missing SQL contract: %s", name)
		}
	}
}

func TestWeatherRepositoryIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	runWeatherRepositoryIntegration(t, ctx, pool)
}

func runWeatherRepositoryIntegration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	points := []spatial.Point{{Longitude: 116.123001, Latitude: 39.123001}, {Longitude: 117.123002, Latitude: 40.123002}}
	repository := NewWeatherRepository(pool)
	pointSet, err := newWeatherPointSet(points)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM weather_snapshot_batches WHERE point_set_hash=$1`, pointSet.hash)
	})
	if err = repository.SaveBatch(ctx, weatherBatchFixtures(points, 1, 2)); err != nil {
		t.Fatal(err)
	}
	if err = repository.SaveBatch(ctx, weatherBatchFixtures([]spatial.Point{points[1], points[0]}, 20, 10)); err != nil {
		t.Fatal(err)
	}
	otherPoints := []spatial.Point{
		{Longitude: 118.123003, Latitude: 41.123003},
		{Longitude: 119.123004, Latitude: 42.123004},
	}
	otherSet, err := newWeatherPointSet(otherPoints)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM weather_snapshot_batches WHERE point_set_hash=$1`, otherSet.hash)
	})
	if err = repository.SaveBatch(ctx, weatherBatchFixtures(otherPoints, 88, 89)); err != nil {
		t.Fatal(err)
	}
	insertIncompleteWeatherBatch(t, ctx, pool, pointSet, weatherFixture(points[0], 99))
	assertLatestWeather(t, ctx, repository, points, 10, 20)
	assertLatestWeather(t, ctx, repository, []spatial.Point{points[1], points[0]}, 20, 10)
}

func insertIncompleteWeatherBatch(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, pointSet weatherPointSet, snapshot hazard.WeatherSnapshot,
) {
	t.Helper()
	var batchID int64
	if err := pool.QueryRow(ctx, insertWeatherBatchSQL, pointSet.hash, len(pointSet.keys)).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	record, err := encodeWeatherRecord(snapshot, pointSet.keys[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, insertWeatherSnapshotSQL, batchID, record.pointKey,
		record.location.Longitude, record.location.Latitude, record.hourly, record.source)
	if err != nil {
		t.Fatal(err)
	}
}

func assertLatestWeather(
	t *testing.T, ctx context.Context, repository *WeatherRepository, points []spatial.Point, firstRain, secondRain float64,
) {
	t.Helper()
	values, err := repository.Latest(ctx, points)
	if err != nil {
		t.Fatal(err)
	}
	if values[0].Hourly[0].RainMM != firstRain || values[1].Hourly[0].RainMM != secondRain {
		t.Fatalf("Latest() rain = %v, %v", values[0].Hourly[0].RainMM, values[1].Hourly[0].RainMM)
	}
}

func weatherBatchFixtures(points []spatial.Point, rains ...float64) []hazard.WeatherSnapshot {
	values := make([]hazard.WeatherSnapshot, len(points))
	for index, point := range points {
		values[index] = weatherFixture(point, rains[index])
	}
	return values
}

func weatherFixture(point spatial.Point, rain float64) hazard.WeatherSnapshot {
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	return hazard.WeatherSnapshot{
		Location: point,
		Hourly:   []hazard.WeatherPoint{{Time: now, PrecipitationMM: rain, RainMM: rain, SoilMoistureByLayer: []float64{0.1, 0.2, 0.3, 0.4, 0.5}}},
		Source: provenance.Provenance{
			Provider: "test", Dataset: "weather", SourceURI: "https://example.test/weather",
			DataKind: provenance.DataKindForecast, FetchedAt: now,
		},
	}
}

func weatherRow(t *testing.T, snapshot hazard.WeatherSnapshot) weatherRowValue {
	t.Helper()
	hourly, err := json.Marshal(snapshot.Hourly)
	if err != nil {
		t.Fatal(err)
	}
	source, err := json.Marshal(snapshot.Source)
	if err != nil {
		t.Fatal(err)
	}
	return weatherRowValue{longitude: snapshot.Location.Longitude, latitude: snapshot.Location.Latitude, hourly: hourly, source: source}
}

func weatherMigrationSQL(t *testing.T) string {
	t.Helper()
	content, err := migrationFiles.ReadFile("migrations/002_weather_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

type fakeWeatherStore struct {
	tx        *fakeWeatherTransaction
	beginErr  error
	rows      weatherRows
	queryErr  error
	queryArgs []any
}

func (s *fakeWeatherStore) Begin(context.Context) (weatherTransaction, error) {
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return s.tx, nil
}

func (s *fakeWeatherStore) Query(_ context.Context, _ string, args ...any) (weatherRows, error) {
	s.queryArgs = args
	return s.rows, s.queryErr
}

type fakeWeatherTransaction struct {
	batchID    int64
	batchErr   error
	batchArgs  []any
	execErrAt  int
	execErr    error
	execCount  int
	committed  bool
	rolledBack bool
}

func (t *fakeWeatherTransaction) QueryRow(_ context.Context, _ string, args ...any) rowScanner {
	t.batchArgs = args
	return fakeWeatherRow{id: t.batchID, err: t.batchErr}
}

func (t *fakeWeatherTransaction) Exec(context.Context, string, ...any) error {
	t.execCount++
	if t.execCount == t.execErrAt {
		return t.execErr
	}
	return nil
}

func (t *fakeWeatherTransaction) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *fakeWeatherTransaction) Rollback(context.Context) error {
	t.rolledBack = true
	return nil
}

type fakeWeatherRow struct {
	id  int64
	err error
}

func (r fakeWeatherRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	value, ok := dest[0].(*int64)
	if !ok {
		return fmt.Errorf("unexpected batch destination %T", dest[0])
	}
	*value = r.id
	return nil
}

type weatherRowValue struct {
	longitude float64
	latitude  float64
	hourly    []byte
	source    []byte
}

type fakeWeatherRows struct {
	values []weatherRowValue
	index  int
	err    error
	closed bool
}

func (r *fakeWeatherRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *fakeWeatherRows) Scan(dest ...any) error {
	value := r.values[r.index-1]
	*(dest[0].(*float64)) = value.longitude
	*(dest[1].(*float64)) = value.latitude
	*(dest[2].(*[]byte)) = value.hourly
	*(dest[3].(*[]byte)) = value.source
	return nil
}

func (r *fakeWeatherRows) Close() {
	r.closed = true
}

func (r *fakeWeatherRows) Err() error {
	return r.err
}

const sha256HexLength = 64
