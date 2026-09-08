// Package lossapi 暴露灾害损失计算、查询和来源审计接口。
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
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/ports"
)

// BasePath 是损失评估 API 在 /api/v1 下的固定路径。
const BasePath = "/loss"

const maxRequestBytes = 1 << 20

var assessmentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var regionCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var publicBasePathPattern = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+/)*[A-Za-z0-9._~-]+$`)

var errStoredAssessment = errors.New("已保存损失评估无效")

// Handler 把损失计算用例和评估仓储暴露为 JSON API。
type Handler struct {
	estimator      applicationloss.AssessmentService
	writer         ports.LossAssessmentWriter
	reader         ports.LossAssessmentReader
	logger         *slog.Logger
	publicBasePath string
	regions        exposurecollection.AdministrativeRegionCatalogProvider
	projector      RegionalExposureProjector
}

// RegionalExposureProjector 为指定快照生成真实行政区暴露投影。
type RegionalExposureProjector interface {
	CollectRegion(context.Context, string, string) (exposurecollection.ExposureProjection, error)
}

// New 创建相对于 BasePath 挂载的损失评估路由。
func New(estimator applicationloss.AssessmentService, writer ports.LossAssessmentWriter,
	reader ports.LossAssessmentReader, publicBasePath string, logger *slog.Logger) (http.Handler, error) {
	return newHandler(estimator, writer, reader, publicBasePath, logger, nil, nil)
}

// NewWithRegionCatalog 创建带真实行政区目录的损失评估 HTTP 服务。
func NewWithRegionCatalog(estimator applicationloss.AssessmentService, writer ports.LossAssessmentWriter,
	reader ports.LossAssessmentReader, publicBasePath string, logger *slog.Logger,
	regions exposurecollection.AdministrativeRegionCatalogProvider) (http.Handler, error) {
	return newHandler(estimator, writer, reader, publicBasePath, logger, regions, nil)
}

// NewWithRegionCatalogAndProjector 创建支持行政区目录和暴露投影的损失评估服务。
func NewWithRegionCatalogAndProjector(estimator applicationloss.AssessmentService, writer ports.LossAssessmentWriter,
	reader ports.LossAssessmentReader, publicBasePath string, logger *slog.Logger,
	regions exposurecollection.AdministrativeRegionCatalogProvider, projector RegionalExposureProjector) (http.Handler, error) {
	return newHandler(estimator, writer, reader, publicBasePath, logger, regions, projector)
}

func newHandler(estimator applicationloss.AssessmentService, writer ports.LossAssessmentWriter,
	reader ports.LossAssessmentReader, publicBasePath string, logger *slog.Logger,
	regions exposurecollection.AdministrativeRegionCatalogProvider, projector RegionalExposureProjector) (http.Handler, error) {
	if estimator == nil || writer == nil || reader == nil || logger == nil {
		return nil, fmt.Errorf("损失评估 HTTP 服务、仓储或日志器不能为空")
	}
	publicBasePath, err := validatePublicBasePath(publicBasePath)
	if err != nil {
		return nil, err
	}
	handler := &Handler{estimator: estimator, writer: writer, reader: reader, logger: logger,
		publicBasePath: publicBasePath, regions: regions, projector: projector}
	router := chi.NewRouter()
	router.Post("/assessments", handler.createAssessment)
	router.Get("/regions", handler.listRegions)
	router.Post("/regions/{regionCode}/projection", handler.createRegionalProjection)
	router.Get("/assessments/{assessmentID}", handler.getAssessment)
	router.Get("/assessments/{assessmentID}/sources", handler.getSources)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

type regionCapability struct {
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Level    string   `json:"level"`
	Status   string   `json:"status"`
	Supports []string `json:"supports"`
	Note     string   `json:"note"`
}

func (h *Handler) listRegions(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	if level == "ADM1" || level == "ADM2" {
		h.listRegionLevel(w, r, level)
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: struct {
		Version string             `json:"version"`
		Regions []regionCapability `json:"regions"`
	}{
		Version: "loss-region-capability-v1",
		Regions: []regionCapability{{
			Code: "CN", Name: "中国全国", Level: "ADM0", Status: "available",
			Supports: []string{"risk", "road_loss", "impact_range"},
			Note:     "当前风险与暴露投影按中国全国边界生成",
		}, {
			Code: "*", Name: "省、市行政区", Level: "ADM1/ADM2", Status: "unavailable",
			Supports: []string{}, Note: "省市行政边界目录和按区裁剪尚未接入",
		}},
	}, RequestID: requestID(r)})
}

func (h *Handler) listRegionLevel(w http.ResponseWriter, r *http.Request, level string) {
	if h.regions == nil {
		h.writeError(w, r, fmt.Errorf("%w: 省市行政区目录尚未配置", domain.ErrInsufficientData))
		return
	}
	values, err := h.regions.RegionCatalog(r.Context(), "CHN", level)
	if err != nil {
		h.writeError(w, r, fmt.Errorf("读取 %s 行政区目录: %w", level, err))
		return
	}
	regions := make([]regionCapability, 0, len(values))
	for _, value := range values {
		regions = append(regions, regionCapability{Code: value.Code, Name: value.Name,
			Level: value.Level, Status: "catalog_only",
			Note: "已读取行政区目录；按区域裁剪和道路损失尚未接入"})
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: struct {
		Version string             `json:"version"`
		Regions []regionCapability `json:"regions"`
	}{Version: "loss-region-capability-v1", Regions: regions}, RequestID: requestID(r)})
}

func (h *Handler) createRegionalProjection(w http.ResponseWriter, r *http.Request) {
	if h.projector == nil {
		h.writeError(w, r, fmt.Errorf("%w: 区域暴露投影尚未配置", domain.ErrInsufficientData))
		return
	}
	var request struct {
		SnapshotID string `json:"snapshotId"`
	}
	if err := decode(r, &request); err != nil {
		h.writeError(w, r, err)
		return
	}
	regionCode := chi.URLParam(r, "regionCode")
	if request.SnapshotID == "" || request.SnapshotID != strings.TrimSpace(request.SnapshotID) ||
		!regionCodePattern.MatchString(regionCode) || regionCode == "CN" {
		h.writeError(w, r, fmt.Errorf("%w: 区域暴露投影参数无效", domain.ErrInvalidInput))
		return
	}
	value, err := h.projector.CollectRegion(r.Context(), request.SnapshotID, regionCode)
	if err != nil {
		h.writeError(w, r, fmt.Errorf("生成 %s 区域暴露投影: %w", regionCode, err))
		return
	}
	h.writeJSON(w, r, http.StatusCreated, successResponse{Data: struct {
		SnapshotID   string `json:"snapshotId"`
		RegionCode   string `json:"regionCode"`
		ProjectionID string `json:"projectionId"`
		Status       string `json:"status"`
		ValidFrom    string `json:"validFrom"`
		ValidTo      string `json:"validTo"`
	}{
		SnapshotID: request.SnapshotID, RegionCode: value.Input.Analysis.RegionCode,
		ProjectionID: value.Input.Analysis.ProjectionID, Status: string(value.Input.Analysis.Status),
		ValidFrom: value.ValidFrom.UTC().Format(time.RFC3339Nano),
		ValidTo:   value.ValidTo.UTC().Format(time.RFC3339Nano),
	}, RequestID: requestID(r)})
}

type estimateRequest struct {
	SnapshotID string `json:"snapshotId"`
	RegionCode string `json:"regionCode"`
}

func (h *Handler) createAssessment(w http.ResponseWriter, r *http.Request) {
	responseID, err := normalizedRequestID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var request estimateRequest
	if err = decode(r, &request); err != nil {
		h.writeError(w, r, err)
		return
	}
	input, err := request.input()
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	value, err := h.estimator.Estimate(r.Context(), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response, err := newAssessmentResponse(value)
	if err != nil {
		h.writeError(w, r, storedAssessmentError(err))
		return
	}
	payload, err := encodeResponse(successResponse{Data: response, RequestID: responseID})
	if err != nil {
		h.writeError(w, r, storedAssessmentError(err))
		return
	}
	if err = h.writer.SaveAssessment(r.Context(), value); err != nil {
		h.writeError(w, r, normalizeAssessmentStoreError(
			fmt.Errorf("保存损失评估 %s: %w", value.ID, err)))
		return
	}
	location := h.publicBasePath + "/assessments/" + url.PathEscape(value.ID)
	w.Header().Set("Location", location)
	h.writeEncodedJSON(w, http.StatusCreated, payload, responseID)
}

func (h *Handler) getAssessment(w http.ResponseWriter, r *http.Request) {
	value, err := h.loadAssessment(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response, err := newAssessmentResponse(value)
	if err != nil {
		h.writeError(w, r, storedAssessmentError(err))
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: response, RequestID: requestID(r)})
}

func (h *Handler) getSources(w http.ResponseWriter, r *http.Request) {
	value, err := h.loadAssessment(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	sanitized, err := newAssessmentResponse(value)
	if err != nil {
		h.writeError(w, r, storedAssessmentError(err))
		return
	}
	audit := newSourceAudit(sanitized)
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: audit, RequestID: requestID(r)})
}

func (h *Handler) loadAssessment(r *http.Request) (lossdomain.Assessment, error) {
	id := chi.URLParam(r, "assessmentID")
	if !assessmentIDPattern.MatchString(id) {
		return lossdomain.Assessment{}, invalidParameter("损失评估标识")
	}
	value, err := h.reader.GetAssessment(r.Context(), id)
	if err != nil {
		return lossdomain.Assessment{}, normalizeAssessmentStoreError(
			fmt.Errorf("读取损失评估 %s: %w", id, err))
	}
	if err = value.Validate(); err != nil {
		return lossdomain.Assessment{}, storedAssessmentError(err)
	}
	return value, nil
}

func normalizeAssessmentStoreError(err error) error {
	if errors.Is(err, ports.ErrStoredAssessmentIntegrity) || errors.Is(err, domain.ErrInvalidInput) {
		return storedAssessmentError(err)
	}
	return err
}

func storedAssessmentError(err error) error {
	if err == nil {
		return errStoredAssessment
	}
	return fmt.Errorf("%w: %w", errStoredAssessment, err)
}

func (r estimateRequest) input() (applicationloss.EstimateInput, error) {
	if strings.TrimSpace(r.SnapshotID) == "" || r.SnapshotID != strings.TrimSpace(r.SnapshotID) {
		return applicationloss.EstimateInput{}, fmt.Errorf("%w: 风险快照标识无效", domain.ErrInvalidInput)
	}
	region := strings.TrimSpace(r.RegionCode)
	if region != "" && !regionCodePattern.MatchString(region) {
		return applicationloss.EstimateInput{}, fmt.Errorf("%w: 行政区代码无效", domain.ErrInvalidInput)
	}
	return applicationloss.EstimateInput{SnapshotID: r.SnapshotID, RegionCode: region}, nil
}

func decode(request *http.Request, destination any) error {
	if request.ContentLength > maxRequestBytes {
		return fmt.Errorf("%w: 请求体超过 %d 字节", domain.ErrInvalidInput, maxRequestBytes)
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil {
		return fmt.Errorf("%w: 读取请求 JSON 失败", domain.ErrInvalidInput)
	}
	if len(payload) > maxRequestBytes {
		return fmt.Errorf("%w: 请求体超过 %d 字节", domain.ErrInvalidInput, maxRequestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: 请求 JSON 无效", domain.ErrInvalidInput)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: 请求只能包含一个 JSON 对象", domain.ErrInvalidInput)
	}
	return nil
}

func validatePublicBasePath(value string) (string, error) {
	if !publicBasePathPattern.MatchString(value) || strings.ContainsAny(value, "?#") {
		return "", fmt.Errorf("%w: 损失评估公开路径无效", domain.ErrInvalidInput)
	}
	cleaned := path.Clean(value)
	if cleaned == "/" || cleaned != strings.TrimSuffix(value, "/") {
		return "", fmt.Errorf("%w: 损失评估公开路径无效", domain.ErrInvalidInput)
	}
	return cleaned, nil
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusNotFound, "route_not_found", "接口不存在", nil)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许", nil)
}
