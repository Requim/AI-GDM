package lossapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	spatialdomain "github.com/Requim/AI-GDM/internal/domain/spatialanalysis"
	"github.com/Requim/AI-GDM/internal/ports"
)

type estimatorStub struct {
	value  lossdomain.Assessment
	err    error
	inputs []applicationloss.EstimateInput
}

func (s *estimatorStub) Estimate(_ context.Context, input applicationloss.EstimateInput) (lossdomain.Assessment, error) {
	s.inputs = append(s.inputs, input)
	return s.value, s.err
}

type assessmentStoreStub struct {
	value   lossdomain.Assessment
	saveErr error
	getErr  error
	saved   []lossdomain.Assessment
}

type sizedJSON int

func (value sizedJSON) MarshalJSON() ([]byte, error) {
	size := int(value)
	return append(append([]byte{'"'}, bytes.Repeat([]byte("x"), size-2)...), '"'), nil
}

func (s *assessmentStoreStub) SaveAssessment(_ context.Context, value lossdomain.Assessment) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.value = value
	s.saved = append(s.saved, value)
	return nil
}

func (s *assessmentStoreStub) GetAssessment(_ context.Context, _ string) (lossdomain.Assessment, error) {
	if s.getErr != nil {
		return lossdomain.Assessment{}, s.getErr
	}
	return s.value, nil
}

func TestAssessmentLifecycleUsesSnapshotOnlyAndSafeMoneyWire(t *testing.T) {
	value := validHTTPAssessment(t)
	api, estimator, store := newTestAPI(t, value, nil)
	created := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-1"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST 状态=%d body=%s", created.Code, created.Body.String())
	}
	location := created.Header().Get("Location")
	if location != "/api/v1/loss/assessments/"+value.ID {
		t.Fatalf("Location=%q", location)
	}
	assertCapturedSnapshot(t, estimator)
	createdValue := decodeAssessmentEnvelope(t, created.Body.Bytes())
	assertMoneyWire(t, created.Body.String(), createdValue)

	loaded := performJSON(t, api, http.MethodGet, location, "")
	if loaded.Code != http.StatusOK {
		t.Fatalf("GET Location 状态=%d body=%s", loaded.Code, loaded.Body.String())
	}
	loadedValue := decodeAssessmentEnvelope(t, loaded.Body.Bytes())
	if !reflect.DeepEqual(createdValue, loadedValue) || len(store.saved) != 1 {
		t.Fatal("POST 与 GET 未返回同一份已保存评估")
	}
	assertEvidenceWire(t, loadedValue)
}

func TestCreateSupportsDeduplicatedMultiZoneServicePath(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	api, store := newIntegratedLossAPI(t, now)
	response := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-multi"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("双区 POST 状态=%d body=%s", response.Code, response.Body.String())
	}
	value := decodeAssessmentEnvelope(t, response.Body.Bytes())
	if value.AffectedPopulation != 50 || value.AffectedRoadMeters != 10 || value.AffectedFacilities != 2 ||
		value.ConditionalLowCents != "3000" || value.ConditionalMidCents != "6000" || value.ConditionalHighCents != "9000" {
		t.Fatalf("双区共享 feature 被重复计费或统计错误: %+v", value)
	}
	if len(value.Evidence.RiskZones) != 2 || len(value.Evidence.Population[0].ZoneIDs) != 2 || len(store.saved) != 1 {
		t.Fatalf("公开双区证据链不完整: evidence=%+v saved=%d", value.Evidence, len(store.saved))
	}
	loaded := performJSON(t, api, http.MethodGet, response.Header().Get("Location"), "")
	if loaded.Code != http.StatusOK || !reflect.DeepEqual(value, decodeAssessmentEnvelope(t, loaded.Body.Bytes())) {
		t.Fatalf("双区 POST/GET 线协议不一致: status=%d body=%s", loaded.Code, loaded.Body.String())
	}
}

func TestCreateRejectsDuplicateMultiZoneFeatureThroughServicePath(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	projection := integratedProjection(now)
	projection.Analysis.Features[1].FeatureID = projection.Analysis.Features[0].FeatureID
	api, store := newIntegratedLossAPIWithProjection(t, now, projection)
	response := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-multi"}`)
	assertAPIError(t, response, http.StatusServiceUnavailable, "insufficient_data")
	if len(store.saved) != 0 {
		t.Fatal("全局 featureId 重复时不得保存评估")
	}
}

func TestSourceAuditBindsAnalysisAndSanitizesSignedURLs(t *testing.T) {
	value := validHTTPAssessment(t)
	value.ID, value.InputDigest = "", ""
	value.Evidence.SpatialAnalysis.ProjectionLimitations = []string{"跳过非闭合设施 way 42"}
	value.Confidence, value.ConfidenceBand = 0.79, "moderate"
	var err error
	value, err = lossdomain.BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	api, _, _ := newTestAPI(t, value, nil)
	response := performJSON(t, api, http.MethodGet, "/api/v1/loss/assessments/"+value.ID+"/sources", "")
	if response.Code != http.StatusOK {
		t.Fatalf("来源审计状态=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data sourceAudit `json:"data"`
	}
	decodeJSON(t, response.Body.Bytes(), &envelope)
	if envelope.Data.AnalysisID != "analysis-1" || envelope.Data.AnalysisVersion != "analysis-v1" ||
		envelope.Data.AnalysisDigest != strings.Repeat("b", 64) ||
		envelope.Data.ProjectionID != value.Evidence.SpatialAnalysis.ProjectionID ||
		envelope.Data.ProjectionVersion != lossdomain.RiskProjectionVersion ||
		envelope.Data.ProjectionDigest != value.Evidence.SpatialAnalysis.ProjectionDigest ||
		!envelope.Data.ProjectionCollectedAt.Equal(value.Evidence.SpatialAnalysis.ProjectionCollectedAt) ||
		!envelope.Data.ProjectionValidFrom.Equal(value.Evidence.SpatialAnalysis.ProjectionValidFrom) ||
		!envelope.Data.ProjectionValidTo.Equal(value.Evidence.SpatialAnalysis.ProjectionValidTo) ||
		!reflect.DeepEqual(envelope.Data.ProjectionLimitations,
			value.Evidence.SpatialAnalysis.ProjectionLimitations) ||
		!reflect.DeepEqual(envelope.Data.SourceReferenceDigests,
			value.Evidence.SpatialAnalysis.SourceReferenceDigests) ||
		envelope.Data.AdminBoundaryID != value.Evidence.SpatialAnalysis.AdminBoundaryID ||
		envelope.Data.AdminBoundaryDigest != value.Evidence.SpatialAnalysis.AdminBoundaryDigest ||
		envelope.Data.InputDigest != value.InputDigest {
		t.Fatalf("审计身份绑定缺失: %+v", envelope.Data)
	}
	if err := envelope.Data.Evidence.Snapshot.Source.Validate(); err != nil {
		t.Fatalf("脱敏后 SourceParts 与组合修订不一致: %v", err)
	}
	wire := strings.ToLower(response.Body.String())
	for _, secret := range []string{"user:pass", "password", "passwd", "session", "x-amz-signature", "x-amz-credential", "x-amz-security-token", "#fragment"} {
		if strings.Contains(wire, secret) {
			t.Fatalf("来源审计泄露敏感 URL 成分 %q", secret)
		}
	}
	if !strings.Contains(wire, "revision=7") || !strings.Contains(wire, "baseline-part") {
		t.Fatal("来源脱敏不应删除非敏感修订参数")
	}
}

func TestAssessmentAndSourcesReplacePrivateReferences(t *testing.T) {
	value := validHTTPAssessment(t)
	value.Evidence.Snapshot.Source.SourceURI = "https://127.0.0.1/private"
	value.Evidence.Snapshot.Source.SourceParts[0].Reference = "https://metadata.internal/part"
	value.Evidence.Snapshot.Source.SourceRevision = provenance.CompositeSourceRevision(
		value.Evidence.Snapshot.Source.SourceParts)
	value.Evidence.Population[0].InputReferences = []string{"https://localhost/population"}
	value.Evidence.Exposures[0].InputReferences = []string{"https://[::1]/road"}
	value.InputReferences = lossdomain.EvidenceReferences(value.Evidence)
	value.ID, value.InputDigest = "", ""
	var err error
	value, err = lossdomain.BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	api, _, _ := newTestAPI(t, value, nil)
	created := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-1"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST 状态=%d body=%s", created.Code, created.Body.String())
	}
	sources := performJSON(t, api, http.MethodGet, created.Header().Get("Location")+"/sources", "")
	if sources.Code != http.StatusOK {
		t.Fatalf("sources 状态=%d body=%s", sources.Code, sources.Body.String())
	}
	wire := strings.ToLower(created.Body.String() + sources.Body.String())
	for _, private := range []string{"127.0.0.1", "metadata.internal", "localhost", "::1"} {
		if strings.Contains(wire, private) {
			t.Fatalf("响应泄漏内部来源 %q", private)
		}
	}
	if !strings.Contains(wire, `"unavailable"`) {
		t.Fatal("内部来源未显式替换为 unavailable")
	}
}

func TestCreateRejectsClientSuppliedDerivedFields(t *testing.T) {
	unknown := []string{"exposures", "intensityBand", "regionCode", "hazardType"}
	for _, field := range unknown {
		t.Run(field, func(t *testing.T) {
			api, estimator, _ := newTestAPI(t, validHTTPAssessment(t), nil)
			body := fmt.Sprintf(`{"snapshotId":"snapshot-1",%q:"forged"}`, field)
			response := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", body)
			if response.Code != http.StatusBadRequest || len(estimator.inputs) != 0 {
				t.Fatalf("未知字段 %s 未在进入用例前拒绝: status=%d", field, response.Code)
			}
		})
	}
}

func TestCreateRejectsOversizedRequestIDBeforePersistence(t *testing.T) {
	api, estimator, store := newTestAPI(t, validHTTPAssessment(t), nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/loss/assessments",
		strings.NewReader(`{"snapshotId":"snapshot-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", strings.Repeat("r", maxRequestIDBytes+1))
	response := httptest.NewRecorder()
	middleware.RequestID(api).ServeHTTP(response, request)
	assertAPIError(t, response, http.StatusBadRequest, "invalid_request")
	if len(estimator.inputs) != 0 || len(store.saved) != 0 {
		t.Fatalf("超长 requestID 进入用例或落库: inputs=%d saved=%d", len(estimator.inputs), len(store.saved))
	}
	if response.Header().Get("Location") != "" || response.Header().Get("X-Request-ID") != "" {
		t.Fatalf("失败响应泄露 Location 或非法 requestID: headers=%v", response.Header())
	}
}

func TestCreateNormalizesGeneratedRequestIDBeforePersistence(t *testing.T) {
	api, _, store := newTestAPI(t, validHTTPAssessment(t), nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/loss/assessments",
		strings.NewReader(`{"snapshotId":"snapshot-1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	middleware.RequestID(api).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(store.saved) != 1 {
		t.Fatalf("生成 requestID 的正常请求失败: status=%d saved=%d body=%s", response.Code, len(store.saved), response.Body.String())
	}
	responseID := response.Header().Get("X-Request-ID")
	if responseID == "" || strings.Contains(responseID, "/") || len(responseID) > maxRequestIDBytes {
		t.Fatalf("生成 requestID 未规范化: %q", responseID)
	}
	var envelope successResponse
	decodeJSON(t, response.Body.Bytes(), &envelope)
	if envelope.RequestID != responseID {
		t.Fatalf("响应头与封包 requestID 不一致: header=%q body=%q", responseID, envelope.RequestID)
	}
}

func TestCreateNormalizesSuppliedRequestIDInHeaderAndEnvelope(t *testing.T) {
	api, _, store := newTestAPI(t, validHTTPAssessment(t), nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/loss/assessments",
		strings.NewReader(`{"snapshotId":"snapshot-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", " client/request id:42 ")
	response := httptest.NewRecorder()
	middleware.RequestID(api).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(store.saved) != 1 {
		t.Fatalf("规范化 requestID 的正常请求失败: status=%d saved=%d", response.Code, len(store.saved))
	}
	var envelope successResponse
	decodeJSON(t, response.Body.Bytes(), &envelope)
	if envelope.RequestID != "clientrequestid:42" || response.Header().Get("X-Request-ID") != envelope.RequestID {
		t.Fatalf("requestID 规范化或封包绑定错误: header=%q body=%q",
			response.Header().Get("X-Request-ID"), envelope.RequestID)
	}
}

func TestDecodeEnforcesOneMiBBoundaryAndSingleObject(t *testing.T) {
	exact := requestPayload(maxRequestBytes)
	var decoded estimateRequest
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(exact))
	if err := decode(request, &decoded); err != nil {
		t.Fatalf("精确 1MiB 请求不应被拒绝: %v", err)
	}
	tooLarge := requestPayload(maxRequestBytes + 1)
	assertDecodeInvalid(t, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(tooLarge)))
	chunked := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(bytes.NewReader(tooLarge)))
	chunked.ContentLength = -1
	assertDecodeInvalid(t, chunked)
	trailing := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"snapshotId":"snapshot-1"}{}`))
	assertDecodeInvalid(t, trailing)
}

func TestHandlerErrorClassificationPreservesInsufficientPriority(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"insufficient_joined", errors.Join(domain.ErrInsufficientData, domain.ErrInvalidInput, domain.ErrNotFound), http.StatusServiceUnavailable, "insufficient_data"},
		{"provider", domain.ErrProviderUnavailable, http.StatusServiceUnavailable, "provider_unavailable"},
		{"invalid", domain.ErrInvalidInput, http.StatusBadRequest, "invalid_request"},
		{"missing", domain.ErrNotFound, http.StatusNotFound, "assessment_not_found"},
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "request_timeout"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			api, _, _ := newTestAPI(t, validHTTPAssessment(t), test.err)
			response := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-1"}`)
			assertAPIError(t, response, test.status, test.code)
		})
	}
	api, _, _ := newTestAPI(t, lossdomain.Assessment{}, nil)
	response := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-1"}`)
	assertAPIError(t, response, http.StatusInternalServerError, "stored_assessment_invalid")
}

func TestHandlerClassifiesStoredIntegrityAsServerErrorAndPreservesCause(t *testing.T) {
	value := validHTTPAssessment(t)
	api, _, store := newTestAPI(t, value, nil)
	store.saveErr = domain.ErrInvalidInput
	created := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-1"}`)
	assertAPIError(t, created, http.StatusInternalServerError, "stored_assessment_invalid")

	store.saveErr, store.getErr = nil, domain.ErrInvalidInput
	loaded := performJSON(t, api, http.MethodGet, "/api/v1/loss/assessments/"+value.ID, "")
	assertAPIError(t, loaded, http.StatusInternalServerError, "stored_assessment_invalid")

	cause := errors.Join(ports.ErrStoredAssessmentIntegrity, domain.ErrInvalidInput)
	wrapped := storedAssessmentError(cause)
	if !errors.Is(wrapped, errStoredAssessment) || !errors.Is(wrapped, ports.ErrStoredAssessmentIntegrity) ||
		!errors.Is(wrapped, domain.ErrInvalidInput) {
		t.Fatalf("已存评估错误链丢失: %v", wrapped)
	}
}

func TestResponseAndReferenceBudgetsFailClosed(t *testing.T) {
	references := make([]string, maxResponseItems)
	for index := range references {
		references[index] = fmt.Sprintf("https://example.test/%04d", index)
	}
	if _, err := sanitizeReferences(references); err != nil {
		t.Fatalf("1000 条来源引用应可接受: %v", err)
	}
	if _, err := sanitizeReferences(append(references, "https://example.test/overflow")); err == nil {
		t.Fatal("1001 条来源引用未被拒绝")
	}
	if value, err := sanitizeReference("https://example.test/%zz"); err != nil || value != unavailableReference {
		t.Fatalf("无法解析的 URL 未转为不可用: value=%q err=%v", value, err)
	}
	for _, value := range []string{"http://example.test/source", "analysis://input", "https:///missing-host"} {
		if got, err := sanitizeReference(value); err != nil || got != unavailableReference {
			t.Fatalf("非 HTTPS host 引用 %q 未转为不可用: value=%q err=%v", value, got, err)
		}
	}
	if got, err := sanitizeReference("https://example.test/source?revision=7"); err != nil || got != "https://example.test/source?revision=7" {
		t.Fatalf("含 host 的 HTTPS 来源未被保留: value=%q err=%v", got, err)
	}
	if err := validateResponseBounds(strings.Repeat("x", maxResponseStringBytes+1)); err == nil {
		t.Fatal("超长单字符串未被拒绝")
	}
	assertAggregateBudgets(t)
}

func TestSanitizeReferenceRejectsPrivateAndInternalHosts(t *testing.T) {
	values := []string{
		"https://127.0.0.1/private", "https://10.0.0.1/private", "https://169.254.169.254/latest",
		"https://127.1/private", "https://0177.0.0.1/private", "https://0x7f.0.0.1/private",
		"https://2130706433/private",
		"https://172.16.0.1/private", "https://192.168.1.1/private", "https://100.64.0.1/private",
		"https://198.18.0.1/private", "https://192.0.2.1/private", "https://198.51.100.1/private",
		"https://203.0.113.1/private", "https://[::1]/private", "https://[fd00::1]/private",
		"https://[fe80::1]/private", "https://[2001:db8::1]/private", "https://[::ffff:127.0.0.1]/private",
		"https://localhost/private", "https://metadata.internal/private", "https://metadata.local/private",
		"https://metadata/private", "https://example.com:8443/private",
	}
	for _, value := range values {
		if got, err := sanitizeReference(value); err != nil || got != unavailableReference {
			t.Fatalf("非公网来源 %q 未转为不可用: value=%q err=%v", value, got, err)
		}
	}
	if got, err := sanitizeReference("https://example.com/public"); err != nil || got != "https://example.com/public" {
		t.Fatalf("公网 HTTPS 来源被拒绝: value=%q err=%v", got, err)
	}
}

func TestSanitizeReferencePreservesGeoBoundaryAuditBinding(t *testing.T) {
	value := "https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/9469f09/" +
		"releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson?" +
		"boundaryID=CHN-ADM0-351020&boundaryYear=2019&geometrySha256=" + strings.Repeat("b", 64) +
		"&license=Public+Domain&metadataSha256=" + strings.Repeat("a", 64) +
		"&shapeID=351020B83567386155957&source=geoBoundaries%2C+Wikimedia+Commons"
	got, err := sanitizeReference(value)
	if err != nil || got != value {
		t.Fatalf("geoBoundaries 审计引用未保留: value=%q error=%v", got, err)
	}
}

func TestSanitizeProvenanceAndInputReferencesHideInternalHosts(t *testing.T) {
	now := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	value := dynamicHTTPSource(now)
	value.SourceURI = "https://127.0.0.1/private"
	value.SourceParts[0].Reference = "https://metadata.internal/part"
	value.SourceRevision = provenance.CompositeSourceRevision(value.SourceParts)
	sanitized, err := sanitizeProvenance(value)
	if err != nil {
		t.Fatal(err)
	}
	if sanitized.SourceURI != unavailableReference || sanitized.SourceParts[0].Reference != unavailableReference {
		t.Fatalf("来源或分片未脱敏: %+v", sanitized)
	}
	references, err := sanitizeReferences([]string{"https://127.0.0.1/input", "https://example.com/input"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(references, []string{"https://example.com/input", unavailableReference}) {
		t.Fatalf("输入引用脱敏=%v", references)
	}
}

func TestSanitizeProvenanceRejectsSourcePartCollisions(t *testing.T) {
	now := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	value := dynamicHTTPSource(now)
	value.SourceParts = []provenance.SourcePart{
		{Reference: "https://example.test/tile?token=first", Revision: "a", SizeBytes: 1},
		{Reference: "https://example.test/tile?token=second", Revision: "b", SizeBytes: 2},
	}
	value.SourceRevision = provenance.CompositeSourceRevision(value.SourceParts)
	if _, err := sanitizeProvenance(value); err == nil {
		t.Fatal("来源分片脱敏后碰撞未 fail-closed")
	}
}

func TestCreateDoesNotPersistSourcePartSanitizationCollision(t *testing.T) {
	value := validHTTPAssessment(t)
	source := &value.Evidence.Snapshot.Source
	source.SourceParts = []provenance.SourcePart{
		{Reference: "https://example.test/tile?token=first", Revision: "a", SizeBytes: 1},
		{Reference: "https://example.test/tile?token=second", Revision: "b", SizeBytes: 2},
	}
	source.SourceRevision = provenance.CompositeSourceRevision(source.SourceParts)
	value.InputReferences = lossdomain.EvidenceReferences(value.Evidence)
	value.ID, value.InputDigest = "", ""
	bound, err := lossdomain.BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	api, _, store := newTestAPI(t, bound, nil)
	response := performJSON(t, api, http.MethodPost, "/api/v1/loss/assessments", `{"snapshotId":"snapshot-1"}`)
	assertAPIError(t, response, http.StatusInternalServerError, "stored_assessment_invalid")
	if len(store.saved) != 0 || response.Header().Get("Location") != "" {
		t.Fatalf("来源碰撞结果被保存或返回 Location: saved=%d location=%q", len(store.saved), response.Header().Get("Location"))
	}
}

func TestWriteJSONCountsTrailingNewlineInOneMiBWireBudget(t *testing.T) {
	handler := &Handler{logger: testLogger()}
	cases := []struct {
		wireSize int
		status   int
	}{
		{maxResponseBytes - 1, http.StatusOK},
		{maxResponseBytes, http.StatusOK},
		{maxResponseBytes + 1, http.StatusInternalServerError},
	}
	for _, test := range cases {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		response := httptest.NewRecorder()
		handler.writeJSON(response, request, http.StatusOK, sizedJSON(test.wireSize-1))
		if response.Code != test.status {
			t.Fatalf("wire=%d status=%d want=%d", test.wireSize, response.Code, test.status)
		}
		if test.status == http.StatusOK && response.Body.Len() != test.wireSize {
			t.Fatalf("wire=%d actual=%d", test.wireSize, response.Body.Len())
		}
	}
}

func TestNewRequiresCanonicalPublicBasePath(t *testing.T) {
	estimator := &estimatorStub{}
	store := &assessmentStoreStub{}
	for _, value := range []string{"", "/", "api/v1/loss", "/api/v1/loss/", "/api/v1/../loss", "/api/v1/loss?x=1", "/api\\loss", "/api/lo\nss"} {
		if _, err := New(estimator, store, store, value, testLogger()); err == nil {
			t.Fatalf("非规范公开路径 %q 未被拒绝", value)
		}
	}
}

func newTestAPI(t *testing.T, value lossdomain.Assessment, estimateErr error) (http.Handler, *estimatorStub, *assessmentStoreStub) {
	t.Helper()
	estimator := &estimatorStub{value: value, err: estimateErr}
	store := &assessmentStoreStub{value: value}
	handler, err := New(estimator, store, store, "/api/v1/loss", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	root := http.NewServeMux()
	root.Handle("/api/v1/loss/", http.StripPrefix("/api/v1/loss", handler))
	return root, estimator, store
}

type integratedInputReader struct {
	value applicationloss.LossInputProjection
}

func (r integratedInputReader) ReadLossInput(context.Context, string, time.Time, applicationloss.RiskProjectionLimits) (
	applicationloss.LossInputProjection, error,
) {
	return r.value, nil
}

type integratedBaselineReader struct{ value lossdomain.BaselineSet }

func (r integratedBaselineReader) BaselineSet(context.Context,
	applicationloss.BaselineQuery,
) (lossdomain.BaselineSet, error) {
	return r.value, nil
}

type integratedClock struct{ now time.Time }

func (c integratedClock) Now() time.Time { return c.now }

func newIntegratedLossAPI(t *testing.T, now time.Time) (http.Handler, *assessmentStoreStub) {
	t.Helper()
	return newIntegratedLossAPIWithProjection(t, now, integratedProjection(now))
}

func newIntegratedLossAPIWithProjection(t *testing.T, now time.Time,
	projection applicationloss.LossInputProjection) (http.Handler, *assessmentStoreStub) {
	t.Helper()
	service, err := applicationloss.NewService(integratedInputReader{value: projection},
		integratedBaselineReader{value: integratedBaselineSet(now)}, integratedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	store := &assessmentStoreStub{}
	handler, err := New(service, store, store, "/api/v1/loss", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	root := http.NewServeMux()
	root.Handle("/api/v1/loss/", http.StripPrefix("/api/v1/loss", handler))
	return root, store
}

func integratedProjection(now time.Time) applicationloss.LossInputProjection {
	snapshot := integratedSnapshot(now)
	zones := []applicationloss.LossRiskZone{
		{ID: "zone-1", SnapshotID: snapshot.ID, Level: hazarddomain.RiskLow, AreaSquareM: 100,
			AreaCalculated: true, AdminCodes: []string{"CN"}},
		{ID: "zone-2", SnapshotID: snapshot.ID, Level: hazarddomain.RiskVeryHigh, AreaSquareM: 100,
			AreaCalculated: true, AdminCodes: []string{"CN"}},
	}
	analysis := integratedSpatialProjection(snapshot)
	stats := applicationloss.RiskProjectionStats{ZoneCount: 2, MaxGeometryPoints: 5, MaxGeometryBytes: 100,
		TotalGeometryPoints: 10, TotalGeometryBytes: 200, SpatialJSONBytes: 400,
		FeatureCount: 3, ProjectionBytes: 4096,
		AnalysisID: analysis.ID, AnalysisDigest: analysis.Digest}
	result := applicationloss.LossInputProjection{Snapshot: snapshot, Zones: zones, Analysis: analysis, Stats: stats}
	result.Stats.ReferenceCount = integratedReferenceCount(result)
	result.Stats.UniqueReferenceCount = len(applicationloss.RiskProjectionSourceDigests(result))
	if err := applicationloss.BindRiskProjectionIdentity(&result); err != nil {
		panic(err)
	}
	return result
}

func integratedSpatialProjection(snapshot hazarddomain.Snapshot) applicationloss.LossSpatialProjection {
	available, both := spatialdomain.MetricAvailable, []string{"zone-1", "zone-2"}
	features := []applicationloss.LossExposureFeature{
		{FeatureID: "facility-shared", Kind: applicationloss.LossFeatureFacility, ZoneIDs: both,
			Quantity: 2, Unit: "count", CoverageRatio: 1, Status: available, Provided: true,
			InputReferences: []string{"https://example.test/facility/shared"}},
		{FeatureID: "population-shared", Kind: applicationloss.LossFeaturePopulation, ZoneIDs: both,
			Quantity: 50, Unit: "people", CoverageRatio: 1, Status: available, Provided: true,
			InputReferences: []string{"https://example.test/population/shared"}},
		{FeatureID: "road-shared", Kind: applicationloss.LossFeatureRoad, ZoneIDs: both,
			Quantity: 10, Unit: "meters", CoverageRatio: 1, Status: available, Provided: true,
			InputReferences: []string{"https://example.test/road/shared"}},
	}
	return applicationloss.LossSpatialProjection{ID: "analysis-multi", Version: "spatial-v2",
		Digest: strings.Repeat("c", 64), ProjectionCollectedAt: snapshot.RunAt.Add(30 * time.Minute),
		ProjectionValidFrom: snapshot.RunAt, ProjectionValidTo: snapshot.ValidTo,
		AdminBoundaryID: "CHN-ADM0-geoboundaries-v6", AdminBoundaryDigest: strings.Repeat("d", 64),
		AdminBoundaryReference: "https://example.test/boundary/chn", SnapshotID: snapshot.ID,
		Status:     spatialdomain.AnalysisAvailable,
		RegionCode: "CN", TotalAreaSquareMeters: 150, CalculatedAt: snapshot.RunAt.Add(30 * time.Minute),
		InputReferences:   []string{"https://example.test/spatial/input"},
		DatasetReferences: []string{"https://example.test/spatial/dataset"}, Features: features}
}

func integratedReferenceCount(value applicationloss.LossInputProjection) int {
	count := 3 + len(value.Snapshot.Source.SourceParts) + len(value.Analysis.InputReferences) +
		len(value.Analysis.DatasetReferences)
	for _, feature := range value.Analysis.Features {
		count += len(feature.InputReferences)
	}
	return count
}

func integratedSnapshot(now time.Time) hazarddomain.Snapshot {
	source := dynamicHTTPSource(now)
	return hazarddomain.Snapshot{ID: "snapshot-multi", HazardType: hazarddomain.TypeLandslide,
		ModelName: "LHASA", ModelVersion: "2.1.1", RunAt: now.Add(-time.Hour), ValidFrom: now.Add(-2 * time.Hour),
		ValidTo: now.Add(12 * time.Hour), RasterReference: source.SourceURI, ProbabilitySemantics: "模型概率",
		Thresholds: []hazarddomain.RiskThreshold{{Level: hazarddomain.RiskLow, Minimum: 0, Maximum: 1}},
		Status:     hazarddomain.SnapshotAvailable, Source: source, Limitations: []string{"辅助研判"}}
}

func integratedBaselineSet(now time.Time) lossdomain.BaselineSet {
	source := baselineHTTPSource(now)
	return lossdomain.BaselineSet{Version: source.DatasetVersion,
		Population: []lossdomain.ExposureBaseline{{ID: "population-cn", RegionCode: "CN",
			Kind: lossdomain.ExposurePopulation, Quantity: 1, Unit: "people", DataYear: 2026, CoverageRatio: 1, Source: source}},
		Roads: []lossdomain.ExposureBaseline{{ID: "road-cn", RegionCode: "CN",
			Kind: lossdomain.ExposureRoad, Quantity: 1, Unit: "meters", DataYear: 2026, CoverageRatio: 1, Source: source}},
		Costs: []lossdomain.CostBaseline{
			integratedCost(lossdomain.AssetFacility, "count", 1000, 2000, 3000, now, source),
			integratedCost(lossdomain.AssetRoad, "meters", 100, 200, 300, now, source)},
		Vulnerabilities: []lossdomain.Vulnerability{
			integratedVulnerability(lossdomain.AssetFacility, source), integratedVulnerability(lossdomain.AssetRoad, source)}}
}

func integratedCost(asset lossdomain.AssetType, unit string, low, mid, high int64, now time.Time,
	source provenance.Provenance) lossdomain.CostBaseline {
	return lossdomain.CostBaseline{ID: "cost-" + string(asset), AssetType: asset, RegionCode: "CN", Unit: unit,
		LowCents: low, CentralCents: mid, HighCents: high, Currency: "CNY", PriceBaseDate: now.Add(-24 * time.Hour),
		Status: lossdomain.BaselineApproved, ApprovedBy: "reviewer", Source: source}
}

func integratedVulnerability(asset lossdomain.AssetType, source provenance.Provenance) lossdomain.Vulnerability {
	return lossdomain.Vulnerability{ID: "vulnerability-" + string(asset), AssetType: asset,
		HazardType: "landslide", IntensityBand: "very_high", ImpactFractionLow: 1, ImpactFractionMid: 1,
		ImpactFractionHigh: 1, DamageRatioLow: 1, DamageRatioMid: 1, DamageRatioHigh: 1,
		CalibrationRegion: "CN", Status: lossdomain.BaselineApproved, ApprovedBy: "reviewer", Source: source}
}

func performJSON(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeAssessmentEnvelope(t *testing.T, payload []byte) assessmentResponse {
	t.Helper()
	var envelope struct {
		Data assessmentResponse `json:"data"`
	}
	decodeJSON(t, payload, &envelope)
	return envelope.Data
}

func decodeJSON(t *testing.T, payload []byte, destination any) {
	t.Helper()
	if err := json.Unmarshal(payload, destination); err != nil {
		t.Fatalf("解码响应 JSON: %v payload=%s", err, payload)
	}
}

func assertCapturedSnapshot(t *testing.T, estimator *estimatorStub) {
	t.Helper()
	want := []applicationloss.EstimateInput{{SnapshotID: "snapshot-1"}}
	if !reflect.DeepEqual(estimator.inputs, want) {
		t.Fatalf("用例输入=%+v want=%+v", estimator.inputs, want)
	}
}

func assertMoneyWire(t *testing.T, wire string, value assessmentResponse) {
	t.Helper()
	if value.ConditionalLowCents != "9007199254740993" || value.ExpectedLowCents != nil {
		t.Fatalf("金额线协议错误: low=%q expected=%v", value.ConditionalLowCents, value.ExpectedLowCents)
	}
	if !strings.Contains(wire, `"conditionalLowCents":"9007199254740993"`) ||
		strings.Contains(wire, `"expectedLowCents"`) || !strings.Contains(wire, `"lowCents":"10"`) ||
		!strings.Contains(wire, `"status":"approved"`) || !strings.Contains(wire, `"provided":true`) ||
		!strings.Contains(wire, `"baselineLevel":"national"`) || !strings.Contains(wire, `"coverageRatio":1`) {
		t.Fatalf("金额未全部使用十进制字符串或可选值未省略: %s", wire)
	}
}

func assertEvidenceWire(t *testing.T, value assessmentResponse) {
	t.Helper()
	if value.Evidence.IntensityBand != "high" || len(value.Evidence.RiskZones) != 1 || len(value.Evidence.Exposures) != 2 {
		t.Fatalf("单区权威证据丢失: %+v", value.Evidence)
	}
	if value.Evidence.Exposures[0].IntensityBand != "high" || value.Evidence.Exposures[1].IntensityBand != "high" {
		t.Fatalf("逐区强度线协议错误: %+v", value.Evidence.Exposures)
	}
	if !value.Evidence.Population[0].Provided || !value.Evidence.Exposures[0].Provided ||
		value.Evidence.Costs[0].BaselineLevel != lossdomain.BaselineNational {
		t.Fatalf("可用性或基线级别线协议缺失: %+v", value.Evidence)
	}
	if !value.Metrics.ConditionalDirectLoss.Provided || value.Metrics.ConditionalDirectLoss.Status != "available" ||
		value.Metrics.ConditionalDirectLoss.BaselineLevel != "national" ||
		value.Metrics.ImpactArea.BaselineLevel != "not_applicable" {
		t.Fatalf("顶层分项契约缺失: %+v", value.Metrics)
	}
}

func assertDecodeInvalid(t *testing.T, request *http.Request) {
	t.Helper()
	var destination estimateRequest
	if err := decode(request, &destination); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("请求体边界未 fail-closed: %v", err)
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("状态=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var value errorResponse
	decodeJSON(t, response.Body.Bytes(), &value)
	if value.Error.Code != code {
		t.Fatalf("错误码=%q want=%q", value.Error.Code, code)
	}
}

func assertAggregateBudgets(t *testing.T) {
	t.Helper()
	itemHeavy := make([][]string, 6)
	for index := range itemHeavy {
		itemHeavy[index] = make([]string, 900)
	}
	if err := validateResponseBounds(itemHeavy); err == nil {
		t.Fatal("响应总项数预算未生效")
	}
	charHeavy := make([]string, 200)
	for index := range charHeavy {
		charHeavy[index] = strings.Repeat("x", 3000)
	}
	if err := validateResponseBounds(charHeavy); err == nil {
		t.Fatal("响应总字符预算未生效")
	}
}

func requestPayload(size int) []byte {
	prefix, suffix := []byte(`{"snapshotId":"`), []byte(`"}`)
	return append(append(prefix, bytes.Repeat([]byte("x"), size-len(prefix)-len(suffix))...), suffix...)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func validHTTPAssessment(t *testing.T) lossdomain.Assessment {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evidence := validHTTPEvidence(now)
	value := lossdomain.Assessment{SnapshotID: "snapshot-1", FormulaVersion: lossdomain.FormulaVersion,
		ScenarioMethod: "逐区确定性公式", HazardType: "landslide", RegionCode: "CN",
		ConditionalLowCents: 9007199254740993, ConditionalMidCents: 9007199254740994, ConditionalHighCents: 9007199254740995,
		ImpactAreaSquareM: 100, AffectedPopulation: 1, AffectedRoadMeters: 10, AffectedFacilities: 1,
		InputReferences: lossdomain.EvidenceReferences(evidence), IncludedAssets: []lossdomain.AssetType{lossdomain.AssetFacility, lossdomain.AssetRoad},
		ExcludedLosses: []string{"建筑物损失未纳入"}, Status: lossdomain.AssessmentAvailable,
		Confidence: 1, ConfidenceBand: "high", Limitations: []string{"仅用于辅助研判"}, CalculatedAt: now, Evidence: evidence}
	bound, err := lossdomain.BindAssessmentIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func validHTTPEvidence(now time.Time) lossdomain.AssessmentEvidence {
	baseline := baselineHTTPSource(now)
	dynamic := dynamicHTTPSource(now)
	return lossdomain.AssessmentEvidence{Version: lossdomain.EvidenceVersion,
		Snapshot: lossdomain.SnapshotEvidence{ID: "snapshot-1", HazardType: "landslide", ModelName: "LHASA", ModelVersion: "2.1.1",
			Status: "available", RunAt: now.Add(-time.Hour), ValidFrom: now.Add(-2 * time.Hour), ValidTo: now.Add(12 * time.Hour), Source: dynamic},
		SpatialAnalysis: lossdomain.SpatialAnalysisEvidence{ID: "analysis-1", Version: "analysis-v1",
			Digest: strings.Repeat("b", 64), ProjectionID: "exposure-" + strings.Repeat("c", 64),
			ProjectionVersion: lossdomain.RiskProjectionVersion, ProjectionDigest: strings.Repeat("c", 64),
			ProjectionCollectedAt: now.Add(-20 * time.Minute), ProjectionValidFrom: now.Add(-time.Hour),
			ProjectionValidTo: now.Add(time.Hour), SourceReferenceDigests: []string{strings.Repeat("d", 64)},
			ProjectionLimitations: []string{}, AdminBoundaryID: "CHN-ADM0-geoboundaries-v6",
			AdminBoundaryDigest: strings.Repeat("e", 64), Status: "available",
			RegionCode: "CN", TotalAreaSquareM: 100, CalculatedAt: now.Add(-30 * time.Minute),
			InputReferences: []string{"analysis://input"}, DatasetReferences: []string{"analysis://dataset"}},
		BaselineSet:   lossdomain.BaselineSetEvidence{Provider: baseline.Provider, Dataset: baseline.Dataset, Version: baseline.DatasetVersion},
		IntensityBand: "high", RiskZones: httpRiskZones(), Population: httpPopulation(), Exposures: httpExposures(),
		Costs: httpCosts(baseline), Vulnerabilities: httpVulnerabilities(baseline)}
}

func httpRiskZones() []lossdomain.RiskZoneEvidence {
	return []lossdomain.RiskZoneEvidence{
		{ID: "zone-a", Level: "high", AreaSquareMeters: 100, AdminCodes: []string{"CN"}},
	}
}

func httpPopulation() []lossdomain.PopulationEvidence {
	return []lossdomain.PopulationEvidence{
		{FeatureID: "population-a", ZoneID: "zone-a", ZoneIDs: []string{"zone-a"}, Quantity: 1,
			Unit: "people", CoverageRatio: 1, Provided: true, MetricStatus: "available",
			InputReferences: []string{"population://zone-a"}},
	}
}

func httpExposures() []lossdomain.Exposure {
	return []lossdomain.Exposure{
		{FeatureID: "facility-a", ZoneID: "zone-a", ZoneIDs: []string{"zone-a"}, AssetType: lossdomain.AssetFacility,
			Quantity: 1, Unit: "count", CoverageRatio: 1, Provided: true, MetricStatus: "available",
			IntensityBand: "high", AnalysisID: "analysis-1", AnalysisVersion: "analysis-v1", InputReferences: []string{"poi://zone-a"}},
		{FeatureID: "road-a", ZoneID: "zone-a", ZoneIDs: []string{"zone-a"}, AssetType: lossdomain.AssetRoad,
			Quantity: 10, Unit: "meters", CoverageRatio: 1, Provided: true, MetricStatus: "available",
			IntensityBand: "high", AnalysisID: "analysis-1", AnalysisVersion: "analysis-v1", InputReferences: []string{"road://zone-a"}},
	}
}

func httpCosts(source provenance.Provenance) []lossdomain.CostBaseline {
	return []lossdomain.CostBaseline{
		{ID: "cost-facility", AssetType: lossdomain.AssetFacility, RegionCode: "CN", Unit: "count", LowCents: 10,
			CentralCents: 20, HighCents: 30, Currency: "CNY", PriceBaseDate: source.ValidFrom,
			Status: lossdomain.BaselineApproved, Provided: true, BaselineLevel: lossdomain.BaselineNational,
			ApprovedBy: "reviewer", Source: source},
		{ID: "cost-road", AssetType: lossdomain.AssetRoad, RegionCode: "CN", Unit: "meters", LowCents: 10,
			CentralCents: 20, HighCents: 30, Currency: "CNY", PriceBaseDate: source.ValidFrom,
			Status: lossdomain.BaselineApproved, Provided: true, BaselineLevel: lossdomain.BaselineNational,
			ApprovedBy: "reviewer", Source: source},
	}
}

func httpVulnerabilities(source provenance.Provenance) []lossdomain.Vulnerability {
	return []lossdomain.Vulnerability{
		httpVulnerability("vulnerability-facility-high", lossdomain.AssetFacility, "high", source),
		httpVulnerability("vulnerability-road-high", lossdomain.AssetRoad, "high", source),
	}
}

func httpVulnerability(id string, asset lossdomain.AssetType, intensity string, source provenance.Provenance) lossdomain.Vulnerability {
	return lossdomain.Vulnerability{ID: id, AssetType: asset, HazardType: "landslide", IntensityBand: intensity,
		ImpactFractionLow: 0.1, ImpactFractionMid: 0.2, ImpactFractionHigh: 0.3,
		DamageRatioLow: 0.1, DamageRatioMid: 0.2, DamageRatioHigh: 0.3,
		CalibrationRegion: "CN", Status: lossdomain.BaselineApproved, Provided: true,
		BaselineLevel: lossdomain.BaselineNational, ApprovedBy: "reviewer", Source: source}
}

func baselineHTTPSource(now time.Time) provenance.Provenance {
	value := provenance.Provenance{Provider: "baseline-provider", Dataset: "loss-baseline", DatasetVersion: "v2026",
		SourceRevision: "revision-1", SourceURI: signedSourceURL("baseline"), Citation: "审核基线", License: "CC-BY-4.0",
		DataKind: provenance.DataKindBaseline, FetchedAt: now.Add(-24 * time.Hour), ValidFrom: now.Add(-30 * 24 * time.Hour),
		ValidTo: now.Add(300 * 24 * time.Hour), SHA256: strings.Repeat("a", 64), TransformVersion: "baseline-v1",
		QualityFlags: []string{"approved"}}
	return withHTTPSourcePart(value, "baseline-part")
}

func dynamicHTTPSource(now time.Time) provenance.Provenance {
	value := provenance.Provenance{Provider: "NASA", Dataset: "LHASA", SourceURI: signedSourceURL("lhasa"),
		DataKind: provenance.DataKindNowcast, FetchedAt: now.Add(-time.Hour), ValidFrom: now.Add(-2 * time.Hour),
		ValidTo: now.Add(12 * time.Hour), QualityFlags: []string{"current"}}
	return withHTTPSourcePart(value, "lhasa-part")
}

func withHTTPSourcePart(value provenance.Provenance, name string) provenance.Provenance {
	value.SourceParts = []provenance.SourcePart{{Reference: signedSourceURL(name), Revision: "part-1", SizeBytes: 128}}
	value.SourceRevision = provenance.CompositeSourceRevision(value.SourceParts)
	return value
}

func signedSourceURL(name string) string {
	return "https://user:pass@example.test/" + name +
		"?revision=7&password=secret&passwd=secret&session=secret&X-Amz-Signature=secret&X-Amz-Credential=credential&X-Amz-Security-Token=token#fragment"
}
