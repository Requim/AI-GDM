package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestLossAssessmentRepositorySQLContracts(t *testing.T) {
	if !strings.Contains(insertLossAssessmentSQL, "ON CONFLICT (id) DO NOTHING") {
		t.Fatal("评估写入未声明同标识幂等策略")
	}
	if !strings.Contains(selectLossAssessmentSQL, "WHERE id=$1") {
		t.Fatal("评估读取未按标识查询")
	}
	selectSQL := normalizedSQL(selectLossAssessmentSQL)
	for _, fragment := range []string{"assessment_bytes", "source_references_bytes as reference_bytes",
		"case when metadata_within_bounds and assessment_bytes between 1 and $2 and reference_bytes between 1 and $3 then assessment end",
		"case when metadata_within_bounds and assessment_bytes between 1 and $2 and reference_bytes between 1 and $3 then source_references end"} {
		if !strings.Contains(selectSQL, fragment) {
			t.Fatalf("评估读取缺少数据库侧字节门禁 %q", fragment)
		}
	}
	if strings.Contains(strings.ToLower(selectLossAssessmentSQL), "assessment::text") {
		t.Fatal("评估读取仍在查询路径物化 assessment::TEXT")
	}
	for _, column := range []string{"hazard_type", "region_code", "calculated_at", "source_references"} {
		if !strings.Contains(selectLossAssessmentSQL, column) {
			t.Fatalf("评估读取未核对审计列 %s", column)
		}
	}
	content, err := migrationFiles.ReadFile("migrations/006_loss_assessments.sql")
	if err != nil {
		t.Fatal(err)
	}
	integrityMigration, err := migrationFiles.ReadFile("migrations/009_loss_assessment_integrity.sql")
	if err != nil {
		t.Fatal(err)
	}
	integritySQL := normalizedSQL(string(integrityMigration))
	for _, fragment := range []string{"generated always as", "assessment_bytes between 1 and 1048576",
		"check (((assessment ? 'id') and (assessment->>'id' = id)) is true)",
		"check (((assessment ? 'snapshotid') and (assessment->>'snapshotid' = snapshot_id)) is true)",
		"check (((assessment ? 'hazardtype') and (assessment->>'hazardtype' = hazard_type)) is true)",
		"check (((assessment ? 'regioncode') and (assessment->>'regioncode' = region_code)) is true)",
		"check (((assessment ? 'formulaversion') and (assessment->>'formulaversion' = formula_version)) is true)",
		"check (((assessment ? 'status') and (assessment->>'status' = status)) is true)",
		"check (((assessment ? 'calculatedat') and ((assessment->>'calculatedat')::timestamptz = calculated_at)) is true)",
		"check (((assessment ? 'inputreferences') and (assessment->'inputreferences' = source_references)) is true)",
		"octet_length(region_code) between 1 and 128"} {
		if !strings.Contains(integritySQL, fragment) {
			t.Errorf("损失评估完整性迁移缺少契约片段 %q", fragment)
		}
	}
	sql := strings.ToLower(string(content))
	for _, fragment := range []string{
		"create table loss_assessments",
		"assessment jsonb not null",
		"source_references jsonb not null",
		"assessment->>'id' = id",
		"assessment->>'snapshotid' = snapshot_id",
		"loss_assessments_snapshot_idx",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("损失评估迁移缺少契约片段 %q", fragment)
		}
	}
	if strings.Contains(sql, "assessment_bytes") {
		t.Error("已发布的 006 迁移不应被完整性增强改写")
	}
}

func normalizedSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func TestReadStoredLossAssessmentRejectsDatabaseBudgetBeforePayloadDecode(t *testing.T) {
	row := &oversizedLossAssessmentRow{}
	queryer := &lossAssessmentQuerySpy{row: row}
	_, err := readStoredLossAssessment(context.Background(), queryer, "loss-oversized")
	if !errors.Is(err, ports.ErrStoredAssessmentIntegrity) || !errors.Is(err, domain.ErrInvalidInput) ||
		!strings.Contains(err.Error(), "超过边界") {
		t.Fatalf("数据库侧超量未返回完整性错误: %v", err)
	}
	if row.payloadDestinationsWerePopulated || !reflect.DeepEqual(queryer.args,
		[]any{"loss-oversized", maxStoredLossAssessmentBytes, maxStoredLossReferencesBytes,
			maxStoredLossIdentityBytes, maxStoredLossStatusBytes}) {
		t.Fatalf("超量大列进入 Scan 或查询未绑定预算: populated=%v args=%v",
			row.payloadDestinationsWerePopulated, queryer.args)
	}
}

func TestLossAssessmentRepositoryRejectsInvalidInputsBeforeDatabaseAccess(t *testing.T) {
	repository := NewLossAssessmentRepository(nil)
	if err := repository.SaveAssessment(context.Background(), loss.Assessment{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("SaveAssessment() error = %v", err)
	}
	if _, err := repository.GetAssessment(context.Background(), " "); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("GetAssessment() error = %v", err)
	}
}

func TestDecodeStoredLossAssessmentRejectsUnknownTrailingAndOversizedJSON(t *testing.T) {
	value := lossAssessmentFixture(time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	payload, references, err := encodeStoredLossAssessment(value)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		payload []byte
		limit   int
		target  any
	}{
		{"assessment_unknown", append(payload[:len(payload)-1], []byte(`,"unknownField":true}`)...), maxStoredLossAssessmentBytes, &loss.Assessment{}},
		{"assessment_trailing", append(append([]byte(nil), payload...), []byte(` {}`)...), maxStoredLossAssessmentBytes, &loss.Assessment{}},
		{"assessment_oversized", bytes.Repeat([]byte(" "), maxStoredLossAssessmentBytes+1), maxStoredLossAssessmentBytes, &loss.Assessment{}},
		{"references_trailing", append(append([]byte(nil), references...), []byte(` []`)...), maxStoredLossReferencesBytes, &[]string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := decodeStrictStoredJSON(test.payload, test.limit, test.target); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("decodeStrictStoredJSON() error=%v", err)
			}
		})
	}
}

func TestDecodeStoredLossAssessmentRejectsCaseAliases(t *testing.T) {
	value := lossAssessmentFixture(time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	payload, _, err := encodeStoredLossAssessment(value)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"top_level_ID_alias", func(object map[string]any) { renameJSONKey(object, "id", "ID") }},
		{"top_level_ID_and_id", func(object map[string]any) { object["ID"] = object["id"] }},
		{"nested_Snapshot_alias", func(object map[string]any) {
			renameJSONKey(nestedJSONObject(object, "evidence"), "snapshot", "Snapshot")
		}},
		{"nested_ID_and_id", func(object map[string]any) {
			snapshot := nestedJSONObject(nestedJSONObject(object, "evidence"), "snapshot")
			snapshot["ID"] = snapshot["id"]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := mutateStoredJSON(t, payload, test.mutate)
			var decoded loss.Assessment
			if err := decodeStrictStoredJSON(mutated, maxStoredLossAssessmentBytes, &decoded); !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "固定 schema") {
				t.Fatalf("大小写别名未 fail-closed: %v", err)
			}
		})
	}
}

func mutateStoredJSON(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	mutate(object)
	mutated, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

func nestedJSONObject(object map[string]any, key string) map[string]any {
	value, _ := object[key].(map[string]any)
	return value
}

func renameJSONKey(object map[string]any, from, to string) {
	object[to] = object[from]
	delete(object, from)
}

func TestDecodeStrictStoredJSONEnforcesExactRawByteBoundary(t *testing.T) {
	const limit = 64
	for _, size := range []int{limit - 1, limit} {
		payload := append(append([]byte{'"'}, bytes.Repeat([]byte("x"), size-2)...), '"')
		var value string
		if err := decodeStrictStoredJSON(payload, limit, &value); err != nil {
			t.Fatalf("size=%d 应可解码: %v", size, err)
		}
	}
	tooLarge := append(append([]byte{'"'}, bytes.Repeat([]byte("x"), limit-1)...), '"')
	var value string
	if err := decodeStrictStoredJSON(tooLarge, limit, &value); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("limit+1 未 fail-closed: %v", err)
	}
}

func TestLossAssessmentRepositoryIntegration(t *testing.T) {
	ctx, repository, pool := openLossAssessmentIntegration(t)
	value := lossAssessmentFixture(time.Now().UTC())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM loss_assessments WHERE id=$1", value.ID)
	})
	if err := repository.SaveAssessment(ctx, value); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetAssessment(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != value.ID || stored.SnapshotID != value.SnapshotID ||
		stored.ConditionalMidCents != value.ConditionalMidCents {
		t.Fatalf("保存后读取结果不一致: %+v", stored)
	}
	assertLossAssessmentAuditColumnBindings(t, ctx, pool, repository, value)
	if err = repository.SaveAssessment(ctx, value); err != nil {
		t.Fatalf("相同内容重复保存失败: %v", err)
	}
	conflict := value
	conflict.ConditionalMidCents++
	if err = repository.SaveAssessment(ctx, conflict); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("同标识不同内容未被拒绝: %v", err)
	}
	if _, err = repository.GetAssessment(ctx, value.ID+"-missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("缺失评估错误 = %v", err)
	}
}

func TestLossAssessmentRepositoryCanonicalTimeRoundTripIntegration(t *testing.T) {
	ctx, repository, pool := openLossAssessmentIntegration(t)
	location := time.FixedZone("UTC+8", 8*60*60)
	raw := time.Date(2026, 8, 28, 18, 0, 0, 0, location).Add(999 * time.Nanosecond)
	first, equivalent := lossAssessmentFixture(raw), lossAssessmentFixture(raw.UTC())
	if first.ID != equivalent.ID || !first.CalculatedAt.Equal(equivalent.CalculatedAt) {
		t.Fatalf("等价 UTC 输入身份漂移: first=%s second=%s", first.ID, equivalent.ID)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM loss_assessments WHERE id=$1", first.ID) })
	if err := repository.SaveAssessment(ctx, first); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetAssessment(ctx, first.ID)
	if err != nil || !stored.CalculatedAt.Equal(first.CalculatedAt) {
		t.Fatalf("PG 时间往返漂移: value=%s err=%v", stored.CalculatedAt, err)
	}
	if err = repository.SaveAssessment(ctx, equivalent); err != nil {
		t.Fatalf("等价 UTC 内容重复保存失败: %v", err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM loss_assessments WHERE id=$1", first.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("幂等保存行数=%d err=%v", count, err)
	}
}

func TestLossAssessmentRepositoryRollsBackWhenVerificationFailsIntegration(t *testing.T) {
	ctx, repository, pool := openLossAssessmentIntegration(t)
	value := lossAssessmentFixture(time.Now().UTC().Add(2 * time.Second))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM loss_assessments WHERE id=$1", value.ID) })
	var injected *failingLossAssessmentReadTransaction
	repository.begin = func(ctx context.Context, options pgx.TxOptions) (lossAssessmentTransaction, error) {
		tx, err := pool.BeginTx(ctx, options)
		if err != nil {
			return nil, err
		}
		injected = &failingLossAssessmentReadTransaction{Tx: tx, err: errors.New("注入核对读取故障")}
		return injected, nil
	}
	if err := repository.SaveAssessment(ctx, value); err == nil || !strings.Contains(err.Error(), "注入核对读取故障") {
		t.Fatalf("核对读取故障未透传: %v", err)
	}
	if injected == nil || !injected.execSucceeded {
		t.Fatal("测试未在 INSERT 成功后注入核对故障")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM loss_assessments WHERE id=$1", value.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("核对失败后评估未回滚: count=%d err=%v", count, err)
	}
}

func TestLossAssessmentRepositoryRejectsJSONBPollutionIntegration(t *testing.T) {
	ctx, repository, pool := openLossAssessmentIntegration(t)
	value := lossAssessmentFixture(time.Now().UTC().Add(3 * time.Second))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM loss_assessments WHERE id=$1", value.ID) })
	if err := repository.SaveAssessment(ctx, value); err != nil {
		t.Fatal(err)
	}
	payload, _, err := encodeStoredLossAssessment(value)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, update, fragment string }{
		{"unknown", `assessment || '{"unknownField":true}'::jsonb`, "unknown field"},
		{"top_level_ID_and_id", `assessment || jsonb_build_object('ID',assessment->'id')`, "固定 schema"},
		{"nested_Snapshot_alias", `jsonb_set(assessment,'{evidence}'::text[],
			((assessment -> 'evidence'::text) - 'snapshot'::text) ||
			jsonb_build_object('Snapshot'::text,assessment #> '{evidence,snapshot}'::text[]))`, "固定 schema"},
		{"nested_ID_and_id", `jsonb_set(assessment,'{evidence,snapshot}',
			(assessment#>'{evidence,snapshot}') || jsonb_build_object('ID',assessment#>'{evidence,snapshot,id}'))`, "固定 schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, updateErr := pool.Exec(ctx, "UPDATE loss_assessments SET assessment="+test.update+" WHERE id=$1", value.ID); updateErr != nil {
				t.Fatal(updateErr)
			}
			assertStoredLossIntegrityError(t, repository, ctx, value.ID, test.fragment)
			if _, restoreErr := pool.Exec(ctx, "UPDATE loss_assessments SET assessment=$2::jsonb WHERE id=$1", value.ID, payload); restoreErr != nil {
				t.Fatal(restoreErr)
			}
		})
	}
	assertOversizedLossAssessmentWritesRejected(t, ctx, pool, repository, value.ID)
}

func TestLossAssessmentMigrationRequiresExactBindingKeysIntegration(t *testing.T) {
	ctx, repository, pool := openLossAssessmentIntegration(t)
	value := lossAssessmentFixture(time.Now().UTC().Add(4 * time.Second))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM loss_assessments WHERE id=$1", value.ID) })
	if err := repository.SaveAssessment(ctx, value); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "snapshotId", "hazardType", "regionCode", "formulaVersion",
		"status", "calculatedAt", "inputReferences"} {
		t.Run("missing_"+key, func(t *testing.T) {
			assertLossAssessmentUpdateRejected(t, ctx, pool, repository, value.ID,
				"UPDATE loss_assessments SET assessment=assessment-$2::text WHERE id=$1", key)
		})
	}
	assertLossAssessmentUpdateRejected(t, ctx, pool, repository, value.ID,
		`UPDATE loss_assessments SET assessment=jsonb_set(assessment,'{hazardType}','null'::jsonb) WHERE id=$1`, nil)
	assertLossAssessmentUpdateRejected(t, ctx, pool, repository, value.ID,
		`UPDATE loss_assessments SET assessment=(assessment-'id') || jsonb_build_object('ID',assessment->'id') WHERE id=$1`, nil)
}

func assertLossAssessmentUpdateRejected(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	repository *LossAssessmentRepository, id, statement string, argument any,
) {
	t.Helper()
	var err error
	if argument == nil {
		_, err = pool.Exec(ctx, statement, id)
	} else {
		_, err = pool.Exec(ctx, statement, id, argument)
	}
	if err == nil {
		t.Fatalf("损失评估完整性约束未拒绝更新: %s", statement)
	}
	if _, err = repository.GetAssessment(ctx, id); err != nil {
		t.Fatalf("约束失败后原评估不可读: %v", err)
	}
}

func TestLossAssessmentRepositoryPreScanGateIntegration(t *testing.T) {
	ctx, _, pool := openLossAssessmentIntegration(t)
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Release)
	createTemporaryLossAssessmentTable(t, ctx, connection)
	for _, field := range []string{"hazard_type", "region_code", "formula_version", "status"} {
		t.Run(field, func(t *testing.T) {
			insertTemporaryLossAssessment(t, ctx, connection, field, false)
			assertTemporaryLossAssessmentSuppressed(t, ctx, connection, "loss-temp-"+field, false)
		})
	}
	for _, field := range []string{"assessment", "source_references"} {
		t.Run(field, func(t *testing.T) {
			insertTemporaryLossAssessment(t, ctx, connection, field, true)
			assertTemporaryLossAssessmentSuppressed(t, ctx, connection, "loss-temp-"+field, true)
		})
	}
}

func createTemporaryLossAssessmentTable(t *testing.T, ctx context.Context, connection *pgxpool.Conn) {
	t.Helper()
	_, err := connection.Exec(ctx, `CREATE TEMP TABLE loss_assessments (
		id TEXT PRIMARY KEY,snapshot_id TEXT,hazard_type TEXT,region_code TEXT,formula_version TEXT,status TEXT,
		calculated_at TIMESTAMPTZ,assessment JSONB,source_references JSONB,
		assessment_bytes INTEGER,source_references_bytes INTEGER) ON COMMIT PRESERVE ROWS`)
	if err != nil {
		t.Fatal(err)
	}
}

func insertTemporaryLossAssessment(t *testing.T, ctx context.Context, connection *pgxpool.Conn,
	field string, oversized bool,
) {
	t.Helper()
	id := "loss-temp-" + field
	values := []any{id, "snapshot-temp", "landslide", "CN", loss.FormulaVersion,
		string(loss.AssessmentAvailable), time.Now().UTC(), `{}`, `[]`, 2, 2}
	if oversized {
		setOversizedTemporaryJSON(values, field)
	} else {
		setOversizedTemporaryMetadata(values, field)
	}
	_, err := connection.Exec(ctx, `INSERT INTO loss_assessments (
		id,snapshot_id,hazard_type,region_code,formula_version,status,calculated_at,assessment,source_references,
		assessment_bytes,source_references_bytes) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10,$11)`, values...)
	if err != nil {
		t.Fatal(err)
	}
}

func setOversizedTemporaryMetadata(values []any, field string) {
	positions := map[string]int{"hazard_type": 2, "region_code": 3, "formula_version": 4, "status": 5}
	maximum := maxStoredLossIdentityBytes
	if field == "status" {
		maximum = maxStoredLossStatusBytes
	}
	values[positions[field]] = strings.Repeat("x", maximum+1)
}

func setOversizedTemporaryJSON(values []any, field string) {
	payload := `["` + strings.Repeat("x", maxStoredLossAssessmentBytes+1) + `"]`
	if field == "assessment" {
		values[7], values[9] = `{"padding":`+payload+`}`, len(payload)+12
		return
	}
	values[8], values[10] = payload, len(payload)
}

func assertTemporaryLossAssessmentSuppressed(t *testing.T, ctx context.Context, connection *pgxpool.Conn,
	id string, metadataWithinBounds bool,
) {
	t.Helper()
	stored, err := queryStoredLossAssessment(ctx, connection, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.metadataWithinBounds != metadataWithinBounds || len(stored.assessment) != 0 ||
		len(stored.sourceReferences) != 0 {
		t.Fatalf("污染大列未在数据库侧抑制: %+v", stored)
	}
	_, err = readStoredLossAssessment(ctx, connection, id)
	if !errors.Is(err, ports.ErrStoredAssessmentIntegrity) || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("历史污染未返回完整性错误链: %v", err)
	}
}

func assertOversizedLossAssessmentWritesRejected(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	repository *LossAssessmentRepository, id string,
) {
	t.Helper()
	updates := []string{
		`UPDATE loss_assessments SET assessment=assessment || jsonb_build_object('padding',repeat('x',$2)) WHERE id=$1`,
		`UPDATE loss_assessments SET source_references=jsonb_build_array(repeat('x',$2)) WHERE id=$1`,
	}
	for _, update := range updates {
		if _, err := pool.Exec(ctx, update, id, maxStoredLossAssessmentBytes+1); err == nil {
			t.Fatalf("超量 JSONB 写入未被数据库约束拒绝: %s", update)
		}
		if _, err := repository.GetAssessment(ctx, id); err != nil {
			t.Fatalf("超量写入失败后原评估不可读: %v", err)
		}
	}
}

func assertLossAssessmentAuditColumnBindings(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	repository *LossAssessmentRepository, value loss.Assessment,
) {
	t.Helper()
	tests := []struct {
		name, mutate string
	}{
		{"hazard_type", "UPDATE loss_assessments SET hazard_type='tampered' WHERE id=$1"},
		{"region_code", "UPDATE loss_assessments SET region_code='XX' WHERE id=$1"},
		{"formula_version", "UPDATE loss_assessments SET formula_version='tampered' WHERE id=$1"},
		{"status", "UPDATE loss_assessments SET status='insufficient_data' WHERE id=$1"},
		{"calculated_at", "UPDATE loss_assessments SET calculated_at=calculated_at + interval '1 second' WHERE id=$1"},
		{"source_references", "UPDATE loss_assessments SET source_references='[\"https://tampered.test\"]'::jsonb WHERE id=$1"},
	}
	for _, test := range tests {
		if _, err := pool.Exec(ctx, test.mutate, value.ID); err == nil {
			t.Fatalf("审计列 %s 漂移未被数据库约束拒绝", test.name)
		}
		if _, err := repository.GetAssessment(ctx, value.ID); err != nil {
			t.Fatalf("审计列 %s 漂移拒绝后原评估不可读: %v", test.name, err)
		}
	}
}

func openLossAssessmentIntegration(t *testing.T) (context.Context, *LossAssessmentRepository, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, NewLossAssessmentRepository(pool), pool
}

type failingLossAssessmentReadTransaction struct {
	pgx.Tx
	err           error
	execSucceeded bool
}

func (t *failingLossAssessmentReadTransaction) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := t.Tx.Exec(ctx, sql, args...)
	t.execSucceeded = err == nil
	return tag, err
}

func (t *failingLossAssessmentReadTransaction) QueryRow(context.Context, string, ...any) pgx.Row {
	return failingLossAssessmentRow{err: t.err}
}

type failingLossAssessmentRow struct{ err error }

func (r failingLossAssessmentRow) Scan(...any) error { return r.err }

type lossAssessmentQuerySpy struct {
	row  pgx.Row
	args []any
}

func (q *lossAssessmentQuerySpy) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	q.args = append([]any(nil), args...)
	return q.row
}

type oversizedLossAssessmentRow struct{ payloadDestinationsWerePopulated bool }

func (r *oversizedLossAssessmentRow) Scan(destinations ...any) error {
	if len(destinations) != 11 {
		return fmt.Errorf("Scan 目标数量=%d", len(destinations))
	}
	*destinations[0].(*bool) = true
	*destinations[7].(*int64) = maxStoredLossAssessmentBytes + 1
	*destinations[8].(*int64) = maxStoredLossReferencesBytes + 1
	r.payloadDestinationsWerePopulated = len(*destinations[9].(*[]byte)) > 0 || len(*destinations[10].(*[]byte)) > 0
	return nil
}

func assertStoredLossIntegrityError(t *testing.T, repository *LossAssessmentRepository,
	ctx context.Context, id, fragment string,
) {
	t.Helper()
	_, err := repository.GetAssessment(ctx, id)
	if !errors.Is(err, ports.ErrStoredAssessmentIntegrity) || !errors.Is(err, domain.ErrInvalidInput) ||
		!strings.Contains(err.Error(), fragment) {
		t.Fatalf("污染 JSONB 未按预期 fail-closed: %v", err)
	}
}

func lossAssessmentFixture(now time.Time) loss.Assessment {
	now = now.UTC().Truncate(time.Microsecond)
	dynamic := lossAssessmentSource(now, provenance.DataKindNowcast, "risk", now.Add(-2*time.Hour), now.Add(12*time.Hour))
	baseline := lossAssessmentSource(now, provenance.DataKindBaseline, "baseline", now.Add(-30*24*time.Hour), now.Add(300*24*time.Hour))
	projectionDigest := strings.Repeat("c", 64)
	evidence := loss.AssessmentEvidence{Version: loss.EvidenceVersion,
		Snapshot: loss.SnapshotEvidence{ID: "snapshot-integration-loss", HazardType: "landslide",
			ModelName: "LHASA", ModelVersion: "v2", Status: "available", RunAt: now.Add(-time.Hour),
			ValidFrom: now.Add(-2 * time.Hour), ValidTo: now.Add(12 * time.Hour), Source: dynamic},
		SpatialAnalysis: loss.SpatialAnalysisEvidence{ID: "analysis-integration-loss", Version: "spatial-v2",
			Digest: strings.Repeat("b", 64), ProjectionID: "exposure-" + projectionDigest,
			ProjectionVersion: loss.RiskProjectionVersion, ProjectionDigest: projectionDigest,
			ProjectionCollectedAt: now.Add(-20 * time.Minute), ProjectionValidFrom: now.Add(-time.Hour),
			ProjectionValidTo: now.Add(time.Hour), SourceReferenceDigests: []string{strings.Repeat("d", 64)},
			ProjectionLimitations: []string{}, AdminBoundaryID: "CHN-ADM0-integration",
			AdminBoundaryDigest: strings.Repeat("e", 64), Status: "available", RegionCode: "CN",
			TotalAreaSquareM: 100, CalculatedAt: now.Add(-30 * time.Minute),
			InputReferences: []string{"analysis://input"}, DatasetReferences: []string{"analysis://dataset"}},
		BaselineSet: loss.BaselineSetEvidence{Provider: baseline.Provider, Dataset: baseline.Dataset,
			Version: baseline.DatasetVersion}, IntensityBand: "high",
		RiskZones: []loss.RiskZoneEvidence{{ID: "zone-1", Level: "high", AreaSquareMeters: 100,
			AdminCodes: []string{"CN"}}},
		Population: []loss.PopulationEvidence{{FeatureID: "population-1", ZoneID: "zone-1",
			ZoneIDs: []string{"zone-1"}, Quantity: 10, Unit: "people", CoverageRatio: 1, Provided: true,
			MetricStatus: "available", InputReferences: []string{"population://zone-1"}}},
		Exposures: []loss.Exposure{
			lossAssessmentExposure(loss.AssetFacility, "facility-1", "count", 1, "poi://zone-1"),
			lossAssessmentExposure(loss.AssetRoad, "road-1", "meters", 20, "road://zone-1")},
		Costs: []loss.CostBaseline{
			lossAssessmentCost(loss.AssetFacility, "count", baseline),
			lossAssessmentCost(loss.AssetRoad, "meters", baseline)},
		Vulnerabilities: []loss.Vulnerability{
			lossAssessmentVulnerability(loss.AssetFacility, baseline),
			lossAssessmentVulnerability(loss.AssetRoad, baseline)}}
	value := loss.Assessment{SnapshotID: evidence.Snapshot.ID, FormulaVersion: loss.FormulaVersion,
		ScenarioMethod: "集成测试确定性公式", HazardType: "landslide", RegionCode: "CN",
		ConditionalLowCents: 100, ConditionalMidCents: 200, ConditionalHighCents: 300,
		ImpactAreaSquareM: 100, AffectedPopulation: 10, AffectedRoadMeters: 20, AffectedFacilities: 1,
		InputReferences: loss.EvidenceReferences(evidence), IncludedAssets: []loss.AssetType{loss.AssetFacility, loss.AssetRoad},
		Status: loss.AssessmentAvailable, Confidence: 1, ConfidenceBand: "high", CalculatedAt: now, Evidence: evidence}
	bound, err := loss.BindAssessmentIdentity(value)
	if err != nil {
		panic(err)
	}
	return bound
}

func lossAssessmentSource(now time.Time, kind provenance.DataKind, name string,
	validFrom, validTo time.Time,
) provenance.Provenance {
	return provenance.Provenance{Provider: "integration", Dataset: name, DatasetVersion: "v1",
		SourceRevision: "revision-1", SourceURI: "https://example.test/" + name, Citation: "集成测试",
		License: "CC-BY-4.0", DataKind: kind, FetchedAt: now.Add(-24 * time.Hour), ValidFrom: validFrom,
		ValidTo: validTo, SHA256: strings.Repeat("a", 64), TransformVersion: "transform-v1",
		QualityFlags: []string{"approved"}}
}

func lossAssessmentExposure(asset loss.AssetType, id, unit string, quantity float64, reference string) loss.Exposure {
	return loss.Exposure{FeatureID: id, ZoneID: "zone-1", ZoneIDs: []string{"zone-1"}, AssetType: asset,
		Quantity: quantity, Unit: unit, CoverageRatio: 1, Provided: true, MetricStatus: "available",
		IntensityBand: "high", AnalysisID: "analysis-integration-loss", AnalysisVersion: "spatial-v2",
		InputReferences: []string{reference}}
}

func lossAssessmentCost(asset loss.AssetType, unit string, source provenance.Provenance) loss.CostBaseline {
	return loss.CostBaseline{ID: "cost-" + string(asset), AssetType: asset, RegionCode: "CN", Unit: unit,
		LowCents: 10, CentralCents: 20, HighCents: 30, Currency: "CNY", PriceBaseDate: source.ValidFrom,
		Status: loss.BaselineApproved, Provided: true, BaselineLevel: loss.BaselineNational,
		ApprovedBy: "reviewer", Source: source}
}

func lossAssessmentVulnerability(asset loss.AssetType, source provenance.Provenance) loss.Vulnerability {
	return loss.Vulnerability{ID: "vulnerability-" + string(asset), AssetType: asset, HazardType: "landslide",
		IntensityBand: "high", ImpactFractionLow: 0.1, ImpactFractionMid: 0.2, ImpactFractionHigh: 0.3,
		DamageRatioLow: 0.1, DamageRatioMid: 0.2, DamageRatioHigh: 0.3, CalibrationRegion: "CN",
		Status: loss.BaselineApproved, Provided: true, BaselineLevel: loss.BaselineNational,
		ApprovedBy: "reviewer", Source: source}
}
