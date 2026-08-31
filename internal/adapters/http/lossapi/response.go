package lossapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	maxResponseBytes       = 1 << 20
	maxRequestIDBytes      = 128
	maxResponseItems       = 1000
	maxResponseStringBytes = 4096
	maxResponseTotalItems  = 5000
	maxResponseTotalChars  = 512 << 10
	maxResponseDepth       = 16
	unavailableReference   = "unavailable"
)

var timeValueType = reflect.TypeOf(time.Time{})

var nonPublicReferencePrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type successResponse struct {
	Data      any    `json:"data"`
	RequestID string `json:"requestId"`
}

type assessmentResponse struct {
	ID                   string                      `json:"id"`
	SnapshotID           string                      `json:"snapshotId"`
	FormulaVersion       string                      `json:"formulaVersion"`
	ScenarioMethod       string                      `json:"scenarioMethod"`
	HazardType           string                      `json:"hazardType"`
	RegionCode           string                      `json:"regionCode"`
	ConditionalLowCents  string                      `json:"conditionalLowCents"`
	ConditionalMidCents  string                      `json:"conditionalCentralCents"`
	ConditionalHighCents string                      `json:"conditionalHighCents"`
	ExpectedLowCents     *string                     `json:"expectedLowCents,omitempty"`
	ExpectedMidCents     *string                     `json:"expectedCentralCents,omitempty"`
	ExpectedHighCents    *string                     `json:"expectedHighCents,omitempty"`
	ImpactAreaSquareM    float64                     `json:"impactAreaSquareMeters"`
	AffectedPopulation   float64                     `json:"affectedPopulation"`
	AffectedRoadMeters   float64                     `json:"affectedRoadMeters"`
	AffectedFacilities   int                         `json:"affectedFacilities"`
	InputReferences      []string                    `json:"inputReferences"`
	IncludedAssets       []lossdomain.AssetType      `json:"includedAssets"`
	ExcludedLosses       []string                    `json:"excludedLosses"`
	Status               lossdomain.AssessmentStatus `json:"status"`
	Confidence           float64                     `json:"confidence"`
	ConfidenceBand       string                      `json:"confidenceBand"`
	Limitations          []string                    `json:"limitations"`
	CalculatedAt         time.Time                   `json:"calculatedAt"`
	InputDigest          string                      `json:"inputDigest"`
	Metrics              assessmentMetricsResponse   `json:"metrics"`
	Evidence             assessmentEvidenceResponse  `json:"evidence"`
}

type assessmentMetricsResponse struct {
	ImpactArea            metricContractResponse `json:"impactArea"`
	AffectedPopulation    metricContractResponse `json:"affectedPopulation"`
	AffectedRoads         metricContractResponse `json:"affectedRoads"`
	AffectedFacilities    metricContractResponse `json:"affectedFacilities"`
	ConditionalDirectLoss metricContractResponse `json:"conditionalDirectLoss"`
}

type metricContractResponse struct {
	Provided      bool   `json:"provided"`
	Status        string `json:"status"`
	BaselineLevel string `json:"baselineLevel"`
}

type assessmentEvidenceResponse struct {
	Version         string                             `json:"version"`
	Snapshot        lossdomain.SnapshotEvidence        `json:"snapshot"`
	SpatialAnalysis lossdomain.SpatialAnalysisEvidence `json:"spatialAnalysis"`
	BaselineSet     lossdomain.BaselineSetEvidence     `json:"baselineSet"`
	IntensityBand   string                             `json:"intensityBand"`
	RiskZones       []lossdomain.RiskZoneEvidence      `json:"riskZones"`
	Population      []lossdomain.PopulationEvidence    `json:"population"`
	Exposures       []lossdomain.Exposure              `json:"exposures"`
	Costs           []costBaselineResponse             `json:"costBaselines"`
	Vulnerabilities []lossdomain.Vulnerability         `json:"vulnerabilities"`
}

type costBaselineResponse struct {
	ID            string                    `json:"id"`
	AssetType     lossdomain.AssetType      `json:"assetType"`
	RegionCode    string                    `json:"regionCode"`
	Unit          string                    `json:"unit"`
	LowCents      string                    `json:"lowCents"`
	CentralCents  string                    `json:"centralCents"`
	HighCents     string                    `json:"highCents"`
	Currency      string                    `json:"currency"`
	PriceBaseDate time.Time                 `json:"priceBaseDate"`
	Status        lossdomain.BaselineStatus `json:"status"`
	Provided      bool                      `json:"provided"`
	BaselineLevel lossdomain.BaselineLevel  `json:"baselineLevel"`
	ApprovedBy    string                    `json:"approvedBy,omitempty"`
	Source        provenance.Provenance     `json:"source"`
}

type sourceAudit struct {
	AssessmentID           string                      `json:"assessmentId"`
	SnapshotID             string                      `json:"snapshotId"`
	AnalysisID             string                      `json:"analysisId"`
	AnalysisVersion        string                      `json:"analysisVersion"`
	AnalysisDigest         string                      `json:"analysisDigest"`
	ProjectionID           string                      `json:"projectionId"`
	ProjectionVersion      string                      `json:"projectionVersion"`
	ProjectionDigest       string                      `json:"projectionDigest"`
	ProjectionCollectedAt  time.Time                   `json:"projectionCollectedAt"`
	ProjectionValidFrom    time.Time                   `json:"projectionValidFrom"`
	ProjectionValidTo      time.Time                   `json:"projectionValidTo"`
	ProjectionLimitations  []string                    `json:"projectionLimitations"`
	SourceReferenceDigests []string                    `json:"sourceReferenceDigests"`
	AdminBoundaryID        string                      `json:"adminBoundaryId"`
	AdminBoundaryDigest    string                      `json:"adminBoundaryDigest"`
	FormulaVersion         string                      `json:"formulaVersion"`
	InputDigest            string                      `json:"inputDigest"`
	Status                 lossdomain.AssessmentStatus `json:"status"`
	CalculatedAt           time.Time                   `json:"calculatedAt"`
	InputReferences        []string                    `json:"inputReferences"`
	InputReferenceCount    int                         `json:"inputReferenceCount"`
	Evidence               assessmentEvidenceResponse  `json:"evidence"`
	Scope                  string                      `json:"scope"`
	Limitations            []string                    `json:"limitations"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

type parameterError struct{ name string }

func (e parameterError) Error() string   { return e.name + "无效" }
func invalidParameter(name string) error { return parameterError{name: name} }

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyError(err)
	h.writeAPIError(w, r, status, code, message, err)
}

func classifyError(err error) (int, string, string) {
	var parameter parameterError
	switch {
	case errors.Is(err, errStoredAssessment), errors.Is(err, ports.ErrStoredAssessmentIntegrity):
		return http.StatusInternalServerError, "stored_assessment_invalid", "已保存的损失评估不可用"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request_timeout", "请求处理超时"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "请求已取消"
	case errors.Is(err, domain.ErrInsufficientData):
		return http.StatusServiceUnavailable, "insufficient_data", "损失评估数据不足"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable", "损失评估依赖暂时不可用"
	case errors.As(err, &parameter), errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "assessment_not_found", "未找到损失评估"
	default:
		return http.StatusInternalServerError, "internal_error", "服务内部错误"
	}
}

func (h *Handler) writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string, cause error) {
	responseID := requestID(r)
	if cause != nil {
		h.logger.ErrorContext(r.Context(), "损失评估 API 请求失败", "status", status, "code", code, "request_id", responseID, "error", cause)
	}
	h.writeJSON(w, r, status, errorResponse{Error: apiError{Code: code, Message: message, RequestID: responseID}})
}

func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	payload, err := encodeResponse(value)
	if err != nil {
		responseID := requestID(r)
		h.logger.ErrorContext(r.Context(), "编码损失评估 API 响应失败", "error", err, "request_id", responseID)
		writeFallbackError(w, responseID)
		return
	}
	h.writeEncodedJSON(w, status, payload, requestID(r))
}

func (h *Handler) writeEncodedJSON(w http.ResponseWriter, status int, payload []byte, responseID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if responseID != "" {
		w.Header().Set("X-Request-ID", responseID)
	}
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func encodeResponse(value any) ([]byte, error) {
	if err := validateResponseBounds(value); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("编码响应 JSON: %w", err)
	}
	if len(payload)+1 > maxResponseBytes {
		return nil, fmt.Errorf("响应线字节超过 %d", maxResponseBytes)
	}
	return append(payload, '\n'), nil
}

func writeFallbackError(w http.ResponseWriter, requestID string) {
	payload, _ := json.Marshal(errorResponse{Error: apiError{Code: "internal_error", Message: "服务内部错误", RequestID: requestID}})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(append(payload, '\n'))
}

func requestID(r *http.Request) string {
	value, err := normalizedRequestID(r)
	if err != nil {
		return ""
	}
	return value
}

func normalizedRequestID(r *http.Request) (string, error) {
	value := middleware.GetReqID(r.Context())
	if value == "" {
		return "", nil
	}
	if len(value) > maxRequestIDBytes {
		return "", fmt.Errorf("%w: 请求标识超过 %d 字节", domain.ErrInvalidInput, maxRequestIDBytes)
	}
	normalized := sanitizeRequestID(value)
	if normalized == "" {
		return "", fmt.Errorf("%w: 请求标识没有可用字符", domain.ErrInvalidInput)
	}
	return normalized, nil
}

func sanitizeRequestID(value string) string {
	var normalized strings.Builder
	for index := 0; index < len(value) && normalized.Len() < maxRequestIDBytes; index++ {
		if requestIDCharacter(value[index]) {
			normalized.WriteByte(value[index])
		}
	}
	return normalized.String()
}

func requestIDCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || strings.ContainsRune("-_.:", rune(value))
}

func newAssessmentResponse(value lossdomain.Assessment) (assessmentResponse, error) {
	if err := value.Validate(); err != nil {
		return assessmentResponse{}, err
	}
	evidence, err := sanitizeEvidence(value.Evidence)
	if err != nil {
		return assessmentResponse{}, err
	}
	references, err := sanitizeReferences(value.InputReferences)
	if err != nil {
		return assessmentResponse{}, err
	}
	response := assessmentResponse{ID: value.ID, SnapshotID: value.SnapshotID, FormulaVersion: value.FormulaVersion,
		ScenarioMethod: value.ScenarioMethod, HazardType: value.HazardType, RegionCode: value.RegionCode,
		ConditionalLowCents: cents(value.ConditionalLowCents), ConditionalMidCents: cents(value.ConditionalMidCents),
		ConditionalHighCents: cents(value.ConditionalHighCents), ExpectedLowCents: optionalCents(value.ExpectedLowCents),
		ExpectedMidCents: optionalCents(value.ExpectedMidCents), ExpectedHighCents: optionalCents(value.ExpectedHighCents),
		ImpactAreaSquareM: value.ImpactAreaSquareM, AffectedPopulation: value.AffectedPopulation,
		AffectedRoadMeters: value.AffectedRoadMeters, AffectedFacilities: value.AffectedFacilities,
		InputReferences: references, IncludedAssets: append([]lossdomain.AssetType(nil), value.IncludedAssets...),
		ExcludedLosses: append([]string(nil), value.ExcludedLosses...), Status: value.Status, Confidence: value.Confidence,
		ConfidenceBand: value.ConfidenceBand, Limitations: append([]string(nil), value.Limitations...),
		CalculatedAt: value.CalculatedAt, InputDigest: value.InputDigest, Metrics: assessmentMetrics(value), Evidence: evidence}
	if err = validateResponseBounds(response); err != nil {
		return assessmentResponse{}, err
	}
	return response, nil
}

func assessmentMetrics(value lossdomain.Assessment) assessmentMetricsResponse {
	provided := value.Status == lossdomain.AssessmentAvailable || value.Status == lossdomain.AssessmentReferenceOnly
	level := "unavailable"
	if provided {
		level = "not_applicable"
	}
	plain := metricContractResponse{Provided: provided, Status: string(value.Status), BaselineLevel: level}
	return assessmentMetricsResponse{ImpactArea: plain, AffectedPopulation: plain,
		AffectedRoads:         assetMetric(value, lossdomain.AssetRoad),
		AffectedFacilities:    assetMetric(value, lossdomain.AssetFacility),
		ConditionalDirectLoss: directLossMetric(value)}
}

func assetMetric(value lossdomain.Assessment, asset lossdomain.AssetType) metricContractResponse {
	if value.Status != lossdomain.AssessmentAvailable && value.Status != lossdomain.AssessmentReferenceOnly {
		return metricContractResponse{Status: string(value.Status), BaselineLevel: "unavailable"}
	}
	levels := make([]lossdomain.BaselineLevel, 0, 2)
	for _, cost := range value.Evidence.Costs {
		if cost.AssetType == asset {
			levels = append(levels, cost.BaselineLevel)
		}
	}
	for _, vulnerability := range value.Evidence.Vulnerabilities {
		if vulnerability.AssetType == asset {
			levels = append(levels, vulnerability.BaselineLevel)
		}
	}
	level := mergedBaselineLevel(levels)
	if value.Status == lossdomain.AssessmentReferenceOnly && len(levels) == 0 {
		level = "not_applicable"
	}
	return metricContractResponse{Provided: true, Status: string(value.Status), BaselineLevel: level}
}

func directLossMetric(value lossdomain.Assessment) metricContractResponse {
	if value.Status != lossdomain.AssessmentAvailable && value.Status != lossdomain.AssessmentReferenceOnly {
		return metricContractResponse{Status: string(value.Status), BaselineLevel: "unavailable"}
	}
	levels := make([]lossdomain.BaselineLevel, 0, len(value.Evidence.Costs)+len(value.Evidence.Vulnerabilities))
	for _, cost := range value.Evidence.Costs {
		levels = append(levels, cost.BaselineLevel)
	}
	for _, vulnerability := range value.Evidence.Vulnerabilities {
		levels = append(levels, vulnerability.BaselineLevel)
	}
	return metricContractResponse{Provided: true, Status: string(value.Status), BaselineLevel: mergedBaselineLevel(levels)}
}

func mergedBaselineLevel(values []lossdomain.BaselineLevel) string {
	if len(values) == 0 {
		return "unavailable"
	}
	selected := values[0]
	for _, value := range values[1:] {
		if value != selected {
			return "mixed"
		}
	}
	return string(selected)
}

func newSourceAudit(value assessmentResponse) sourceAudit {
	spatial := value.Evidence.SpatialAnalysis
	return sourceAudit{AssessmentID: value.ID, SnapshotID: value.SnapshotID,
		AnalysisID: spatial.ID, AnalysisVersion: spatial.Version, AnalysisDigest: spatial.Digest,
		ProjectionID: spatial.ProjectionID, ProjectionVersion: spatial.ProjectionVersion,
		ProjectionDigest: spatial.ProjectionDigest, ProjectionCollectedAt: spatial.ProjectionCollectedAt,
		ProjectionValidFrom: spatial.ProjectionValidFrom, ProjectionValidTo: spatial.ProjectionValidTo,
		ProjectionLimitations:  append([]string{}, spatial.ProjectionLimitations...),
		SourceReferenceDigests: append([]string(nil), spatial.SourceReferenceDigests...),
		AdminBoundaryID:        spatial.AdminBoundaryID, AdminBoundaryDigest: spatial.AdminBoundaryDigest,
		FormulaVersion: value.FormulaVersion, InputDigest: value.InputDigest, Status: value.Status,
		CalculatedAt: value.CalculatedAt, InputReferences: append([]string(nil), value.InputReferences...),
		InputReferenceCount: len(value.InputReferences), Evidence: value.Evidence,
		Scope:       "评估时固化的权威输入、基线取值和脱敏来源",
		Limitations: []string{"来源内容未在本服务留存，输入摘要绑定脱敏前的原始权威记录"}}
}

func sanitizeEvidence(value lossdomain.AssessmentEvidence) (assessmentEvidenceResponse, error) {
	result := assessmentEvidenceResponse{Version: value.Version, Snapshot: value.Snapshot,
		SpatialAnalysis: value.SpatialAnalysis, BaselineSet: value.BaselineSet, IntensityBand: value.IntensityBand,
		RiskZones:       append([]lossdomain.RiskZoneEvidence(nil), value.RiskZones...),
		Population:      append([]lossdomain.PopulationEvidence(nil), value.Population...),
		Exposures:       append([]lossdomain.Exposure(nil), value.Exposures...),
		Vulnerabilities: append([]lossdomain.Vulnerability(nil), value.Vulnerabilities...)}
	var err error
	if result.Snapshot.Source, err = sanitizeProvenance(result.Snapshot.Source); err != nil {
		return assessmentEvidenceResponse{}, err
	}
	if result.SpatialAnalysis.InputReferences, err = sanitizeReferences(value.SpatialAnalysis.InputReferences); err != nil {
		return assessmentEvidenceResponse{}, err
	}
	if result.SpatialAnalysis.DatasetReferences, err = sanitizeReferences(value.SpatialAnalysis.DatasetReferences); err != nil {
		return assessmentEvidenceResponse{}, err
	}
	result.SpatialAnalysis.ProjectionLimitations = append([]string{}, value.SpatialAnalysis.ProjectionLimitations...)
	if err = sanitizeEvidenceCollections(&result, value); err != nil {
		return assessmentEvidenceResponse{}, err
	}
	return result, nil
}

func sanitizeEvidenceCollections(result *assessmentEvidenceResponse, source lossdomain.AssessmentEvidence) error {
	for index := range result.RiskZones {
		result.RiskZones[index].AdminCodes = append([]string(nil), source.RiskZones[index].AdminCodes...)
	}
	for index := range result.Population {
		result.Population[index].ZoneIDs = append([]string(nil), source.Population[index].ZoneIDs...)
		values, err := sanitizeReferences(source.Population[index].InputReferences)
		if err != nil {
			return err
		}
		result.Population[index].InputReferences = values
	}
	for index := range result.Exposures {
		result.Exposures[index].ZoneIDs = append([]string(nil), source.Exposures[index].ZoneIDs...)
		values, err := sanitizeReferences(source.Exposures[index].InputReferences)
		if err != nil {
			return err
		}
		result.Exposures[index].InputReferences = values
	}
	return sanitizeBaselines(result, source)
}

func sanitizeBaselines(result *assessmentEvidenceResponse, source lossdomain.AssessmentEvidence) error {
	result.Costs = make([]costBaselineResponse, 0, len(source.Costs))
	for _, value := range source.Costs {
		sanitized, err := sanitizeProvenance(value.Source)
		if err != nil {
			return err
		}
		result.Costs = append(result.Costs, costBaselineResponse{ID: value.ID, AssetType: value.AssetType,
			RegionCode: value.RegionCode, Unit: value.Unit, LowCents: cents(value.LowCents),
			CentralCents: cents(value.CentralCents), HighCents: cents(value.HighCents), Currency: value.Currency,
			PriceBaseDate: value.PriceBaseDate, Status: value.Status, Provided: value.Provided,
			BaselineLevel: value.BaselineLevel, ApprovedBy: value.ApprovedBy, Source: sanitized})
	}
	for index := range result.Vulnerabilities {
		sanitized, err := sanitizeProvenance(source.Vulnerabilities[index].Source)
		if err != nil {
			return err
		}
		result.Vulnerabilities[index].Source = sanitized
	}
	return nil
}

func sanitizeProvenance(value provenance.Provenance) (provenance.Provenance, error) {
	var err error
	if value.SourceURI, err = sanitizeReference(value.SourceURI); err != nil {
		return provenance.Provenance{}, err
	}
	value.QualityFlags = append([]string(nil), value.QualityFlags...)
	value.Limitations = append([]string(nil), value.Limitations...)
	value.SourceParts = append([]provenance.SourcePart(nil), value.SourceParts...)
	seen := make(map[string]struct{}, len(value.SourceParts))
	for index := range value.SourceParts {
		value.SourceParts[index].Reference, err = sanitizeReference(value.SourceParts[index].Reference)
		if err != nil {
			return provenance.Provenance{}, err
		}
		if _, exists := seen[value.SourceParts[index].Reference]; exists {
			return provenance.Provenance{}, fmt.Errorf("来源分片脱敏后发生引用碰撞")
		}
		seen[value.SourceParts[index].Reference] = struct{}{}
	}
	if len(value.SourceParts) > 0 {
		value.SourceRevision = provenance.CompositeSourceRevision(value.SourceParts)
	}
	if err = validateResponseBounds(value); err != nil {
		return provenance.Provenance{}, err
	}
	return value, nil
}

func sanitizeReferences(values []string) ([]string, error) {
	if len(values) > maxResponseItems {
		return nil, fmt.Errorf("来源引用超过 %d 条", maxResponseItems)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		sanitized, err := sanitizeReference(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[sanitized]; exists {
			continue
		}
		seen[sanitized] = struct{}{}
		result = append(result, sanitized)
	}
	sort.Strings(result)
	return result, nil
}

func sanitizeReference(value string) (string, error) {
	if value == "" || len(value) > maxResponseStringBytes {
		return "", fmt.Errorf("来源引用长度无效")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return unavailableReference, nil
	}
	if !strings.EqualFold(parsed.Scheme, "https") || !publicReferenceHost(parsed) {
		return unavailableReference, nil
	}
	parsed.User = nil
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		if sensitiveQueryKey(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	cleaned := parsed.String()
	if cleaned == "" || len(cleaned) > maxResponseStringBytes {
		return "", fmt.Errorf("脱敏来源引用长度无效")
	}
	return cleaned, nil
}

func publicReferenceHost(value *url.URL) bool {
	if value == nil || (value.Port() != "" && value.Port() != "443") {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(value.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".home") ||
		strings.HasSuffix(host, ".home.arpa") || strings.HasSuffix(host, ".corp") {
		return false
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return publicReferenceAddress(address)
	}
	if legacyIPv4Literal(host) {
		return false
	}
	return validPublicHostname(host)
}

func legacyIPv4Literal(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if !legacyIPv4Part(part) {
			return false
		}
	}
	return true
}

func legacyIPv4Part(value string) bool {
	if value == "0" {
		return true
	}
	digits, base := value, 10
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		digits, base = value[2:], 16
	} else if len(value) > 1 && value[0] == '0' {
		digits, base = value[1:], 8
	}
	if digits == "" {
		return false
	}
	_, err := strconv.ParseUint(digits, base, 64)
	return err == nil
}

func publicReferenceAddress(value netip.Addr) bool {
	if value.Is4In6() {
		value = value.Unmap()
	}
	if !value.IsGlobalUnicast() || value.IsPrivate() || value.IsLoopback() ||
		value.IsLinkLocalUnicast() || value.IsLinkLocalMulticast() || value.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicReferencePrefixes {
		if prefix.Contains(value) {
			return false
		}
	}
	return true
}

func validPublicHostname(value string) bool {
	if !strings.Contains(value, ".") || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func sensitiveQueryKey(value string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(value))
	if strings.HasPrefix(normalized, "x_amz_") || strings.HasPrefix(normalized, "x_goog_") {
		return true
	}
	if strings.Contains(normalized, "signature") || strings.Contains(normalized, "credential") {
		return true
	}
	if strings.Contains(normalized, "password") || strings.Contains(normalized, "passwd") || strings.Contains(normalized, "session") {
		return true
	}
	if strings.HasSuffix(normalized, "_token") || strings.HasSuffix(normalized, "_secret") || strings.HasSuffix(normalized, "_key") {
		return true
	}
	switch normalized {
	case "key", "token", "secret", "sig", "apikey", "api_key", "access_token", "authorization", "security_token", "awsaccesskeyid":
		return true
	default:
		return false
	}
}

func validateResponseBounds(value any) error {
	budget := &responseBudget{}
	if err := validateBoundedValue(reflect.ValueOf(value), budget, 0); err != nil {
		return err
	}
	if budget.items > maxResponseTotalItems || budget.chars > maxResponseTotalChars {
		return fmt.Errorf("响应总项数或总字符预算超限")
	}
	return nil
}

type responseBudget struct {
	items int
	chars int
}

func validateBoundedValue(value reflect.Value, budget *responseBudget, depth int) error {
	if !value.IsValid() || depth > maxResponseDepth {
		if depth > maxResponseDepth {
			return fmt.Errorf("响应结构嵌套过深")
		}
		return nil
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return validateBoundedValue(value.Elem(), budget, depth+1)
	}
	if value.Type() == timeValueType {
		return nil
	}
	if value.Kind() == reflect.String {
		budget.chars += value.Len()
		if value.Len() > maxResponseStringBytes {
			return fmt.Errorf("响应字符串超过 %d 字节", maxResponseStringBytes)
		}
	}
	if value.Kind() == reflect.Slice || value.Kind() == reflect.Array || value.Kind() == reflect.Map {
		budget.items += value.Len()
		if value.Len() > maxResponseItems {
			return fmt.Errorf("响应数组超过 %d 项", maxResponseItems)
		}
	}
	return validateBoundedChildren(value, budget, depth)
}

func validateBoundedChildren(value reflect.Value, budget *responseBudget, depth int) error {
	switch value.Kind() {
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath == "" {
				if err := validateBoundedValue(value.Field(index), budget, depth+1); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateBoundedValue(value.Index(index), budget, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			if err := validateBoundedValue(key, budget, depth+1); err != nil {
				return err
			}
			if err := validateBoundedValue(value.MapIndex(key), budget, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func cents(value int64) string { return strconv.FormatInt(value, 10) }

func optionalCents(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := cents(*value)
	return &formatted
}
