package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// WeatherRepository 使用 PostGIS 持久化完整监测点天气批次。
type WeatherRepository struct {
	store weatherStore
}

// NewWeatherRepository 创建天气批次仓储适配器。
func NewWeatherRepository(pool *pgxpool.Pool) *WeatherRepository {
	return &WeatherRepository{store: pgxWeatherStore{pool: pool}}
}

// SaveBatch 在同一事务中保存完整监测点批次。
func (r *WeatherRepository) SaveBatch(ctx context.Context, snapshots []hazard.WeatherSnapshot) error {
	batch, err := prepareWeatherBatch(snapshots)
	if err != nil {
		return err
	}
	tx, err := r.store.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始保存天气批次事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	batchID, err := createWeatherBatch(ctx, tx, batch.pointSetHash, len(batch.records))
	if err != nil {
		return err
	}
	for _, record := range batch.records {
		if err = saveWeatherRecord(ctx, tx, batchID, record); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交天气批次事务: %w", err)
	}
	return nil
}

// Latest 返回同一监测点集最近的完整批次，并恢复调用方点位顺序。
func (r *WeatherRepository) Latest(ctx context.Context, points []spatial.Point) ([]hazard.WeatherSnapshot, error) {
	pointSet, err := newWeatherPointSet(points)
	if err != nil {
		return nil, err
	}
	rows, err := r.store.Query(ctx, selectLatestWeatherSQL, pointSet.hash, pointSet.keys, len(pointSet.keys))
	if err != nil {
		return nil, fmt.Errorf("查询最新天气批次: %w", err)
	}
	values, err := scanWeatherSnapshots(rows)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, domain.ErrNotFound
	}
	if len(values) != len(points) {
		return nil, fmt.Errorf("%w: 天气批次点数不完整", domain.ErrInsufficientData)
	}
	return values, nil
}

type weatherBatch struct {
	pointSetHash string
	records      []weatherRecord
}

type weatherRecord struct {
	pointKey string
	location spatial.Point
	hourly   []byte
	source   []byte
}

func prepareWeatherBatch(snapshots []hazard.WeatherSnapshot) (weatherBatch, error) {
	points := make([]spatial.Point, len(snapshots))
	for index := range snapshots {
		points[index] = snapshots[index].Location
	}
	pointSet, err := newWeatherPointSet(points)
	if err != nil {
		return weatherBatch{}, err
	}
	records := make([]weatherRecord, len(snapshots))
	for index, snapshot := range snapshots {
		records[index], err = encodeWeatherRecord(snapshot, pointSet.keys[index])
		if err != nil {
			return weatherBatch{}, err
		}
	}
	return weatherBatch{pointSetHash: pointSet.hash, records: records}, nil
}

func encodeWeatherRecord(snapshot hazard.WeatherSnapshot, pointKey string) (weatherRecord, error) {
	if len(snapshot.Hourly) == 0 {
		return weatherRecord{}, fmt.Errorf("%w: 天气点 %s 缺少逐小时数据", domain.ErrInvalidInput, pointKey)
	}
	if err := snapshot.Source.Validate(); err != nil {
		return weatherRecord{}, fmt.Errorf("校验天气点 %s 来源: %w", pointKey, err)
	}
	hourly, err := json.Marshal(snapshot.Hourly)
	if err != nil {
		return weatherRecord{}, fmt.Errorf("编码天气点 %s 逐小时数据: %w", pointKey, err)
	}
	source, err := json.Marshal(snapshot.Source)
	if err != nil {
		return weatherRecord{}, fmt.Errorf("编码天气点 %s 来源: %w", pointKey, err)
	}
	return weatherRecord{pointKey: pointKey, location: snapshot.Location, hourly: hourly, source: source}, nil
}

type weatherPointSet struct {
	keys []string
	hash string
}

func newWeatherPointSet(points []spatial.Point) (weatherPointSet, error) {
	if len(points) == 0 {
		return weatherPointSet{}, fmt.Errorf("%w: 天气点集不能为空", domain.ErrInvalidInput)
	}
	keys := make([]string, len(points))
	seen := make(map[string]struct{}, len(points))
	for index, point := range points {
		key, err := weatherPointKey(point)
		if err != nil {
			return weatherPointSet{}, fmt.Errorf("校验天气点 %d: %w", index, err)
		}
		if _, exists := seen[key]; exists {
			return weatherPointSet{}, fmt.Errorf("%w: 天气点键 %s 重复", domain.ErrInvalidInput, key)
		}
		seen[key] = struct{}{}
		keys[index] = key
	}
	sortedKeys := append([]string(nil), keys...)
	sort.Strings(sortedKeys)
	sum := sha256.Sum256([]byte(strings.Join(sortedKeys, "\n")))
	return weatherPointSet{keys: keys, hash: hex.EncodeToString(sum[:])}, nil
}

func weatherPointKey(point spatial.Point) (string, error) {
	if !finiteCoordinate(point.Longitude) || !finiteCoordinate(point.Latitude) {
		return "", fmt.Errorf("%w: 坐标必须是有限数", domain.ErrInvalidInput)
	}
	if err := point.Validate(); err != nil {
		return "", err
	}
	longitude := canonicalCoordinate(point.Longitude)
	latitude := canonicalCoordinate(point.Latitude)
	return fmt.Sprintf("%.6f,%.6f", longitude, latitude), nil
}

func finiteCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func canonicalCoordinate(value float64) float64 {
	value = math.Round(value*1_000_000) / 1_000_000
	if value == 0 {
		return 0
	}
	return value
}

func createWeatherBatch(ctx context.Context, tx weatherTransaction, pointSetHash string, pointCount int) (int64, error) {
	var batchID int64
	if err := tx.QueryRow(ctx, insertWeatherBatchSQL, pointSetHash, pointCount).Scan(&batchID); err != nil {
		return 0, fmt.Errorf("建立天气批次: %w", err)
	}
	return batchID, nil
}

func saveWeatherRecord(ctx context.Context, tx weatherTransaction, batchID int64, record weatherRecord) error {
	err := tx.Exec(ctx, insertWeatherSnapshotSQL, batchID, record.pointKey,
		record.location.Longitude, record.location.Latitude, record.hourly, record.source)
	if err != nil {
		return fmt.Errorf("保存天气点 %s: %w", record.pointKey, err)
	}
	return nil
}

func scanWeatherSnapshots(rows weatherRows) ([]hazard.WeatherSnapshot, error) {
	defer rows.Close()
	values := make([]hazard.WeatherSnapshot, 0)
	for rows.Next() {
		value, err := scanWeatherSnapshot(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历天气批次: %w", err)
	}
	return values, nil
}

func scanWeatherSnapshot(row rowScanner) (hazard.WeatherSnapshot, error) {
	var value hazard.WeatherSnapshot
	var hourly, source []byte
	if err := row.Scan(&value.Location.Longitude, &value.Location.Latitude, &hourly, &source); err != nil {
		return hazard.WeatherSnapshot{}, fmt.Errorf("扫描天气点: %w", err)
	}
	if err := json.Unmarshal(hourly, &value.Hourly); err != nil {
		return hazard.WeatherSnapshot{}, fmt.Errorf("解码逐小时天气: %w", err)
	}
	if err := json.Unmarshal(source, &value.Source); err != nil {
		return hazard.WeatherSnapshot{}, fmt.Errorf("解码天气来源: %w", err)
	}
	return value, nil
}

type weatherStore interface {
	Begin(ctx context.Context) (weatherTransaction, error)
	Query(ctx context.Context, sql string, args ...any) (weatherRows, error)
}

type weatherTransaction interface {
	QueryRow(ctx context.Context, sql string, args ...any) rowScanner
	Exec(ctx context.Context, sql string, args ...any) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type weatherRows interface {
	rowScanner
	Close()
	Next() bool
	Err() error
}

type pgxWeatherStore struct {
	pool *pgxpool.Pool
}

func (s pgxWeatherStore) Begin(ctx context.Context) (weatherTransaction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgxWeatherTransaction{tx: tx}, nil
}

func (s pgxWeatherStore) Query(ctx context.Context, sql string, args ...any) (weatherRows, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

type pgxWeatherTransaction struct {
	tx pgx.Tx
}

func (t pgxWeatherTransaction) QueryRow(ctx context.Context, sql string, args ...any) rowScanner {
	return t.tx.QueryRow(ctx, sql, args...)
}

func (t pgxWeatherTransaction) Exec(ctx context.Context, sql string, args ...any) error {
	_, err := t.tx.Exec(ctx, sql, args...)
	return err
}

func (t pgxWeatherTransaction) Commit(ctx context.Context) error {
	return t.tx.Commit(ctx)
}

func (t pgxWeatherTransaction) Rollback(ctx context.Context) error {
	return t.tx.Rollback(ctx)
}

const insertWeatherBatchSQL = `INSERT INTO weather_snapshot_batches (point_set_hash, point_count)
    VALUES ($1,$2) RETURNING id`

const insertWeatherSnapshotSQL = `INSERT INTO weather_snapshots (
    batch_id,point_key,location,hourly,source
) VALUES ($1,$2,ST_SetSRID(ST_MakePoint($3,$4),4326),$5,$6)`

const selectLatestWeatherSQL = `WITH requested(point_key, position) AS (
    SELECT point_key, position FROM unnest($2::text[]) WITH ORDINALITY AS input(point_key, position)
), latest_batch AS (
    SELECT batch.id
    FROM weather_snapshot_batches batch
    WHERE batch.point_set_hash=$1 AND batch.point_count=$3
      AND (SELECT COUNT(*) FROM weather_snapshots stored WHERE stored.batch_id=batch.id)=batch.point_count
      AND (SELECT COUNT(*) FROM weather_snapshots stored
           JOIN requested ON requested.point_key=stored.point_key
           WHERE stored.batch_id=batch.id)=batch.point_count
    ORDER BY batch.created_at DESC, batch.id DESC
    LIMIT 1
)
SELECT ST_X(snapshot.location),ST_Y(snapshot.location),snapshot.hourly,snapshot.source
FROM latest_batch
JOIN weather_snapshots snapshot ON snapshot.batch_id=latest_batch.id
JOIN requested ON requested.point_key=snapshot.point_key
ORDER BY requested.position`
