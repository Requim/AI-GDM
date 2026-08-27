// Package lossapi 暴露灾害损失计算、查询和来源审计接口。
package lossapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/ports"
)

// BasePath 是损失评估 API 在 /api/v1 下的固定路径。
const BasePath = "/loss"

const maxRequestBytes = 1 << 20

var assessmentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var errStoredAssessment = errors.New("已保存损失评估无效")

// Handler 把损失计算用例和评估仓储暴露为 JSON API。
type Handler struct {
	estimator applicationloss.AssessmentService
	writer    ports.LossAssessmentWriter
	reader    ports.LossAssessmentReader
	logger    *slog.Logger
}

// New 创建相对于 BasePath 挂载的损失评估路由。
func New(estimator applicationloss.AssessmentService, writer ports.LossAssessmentWriter,
	reader ports.LossAssessmentReader, logger *slog.Logger) (http.Handler, error) {
	if estimator == nil || writer == nil || reader == nil || logger == nil {
		return nil, fmt.Errorf("损失评估 HTTP 服务、仓储或日志器不能为空")
	}
	handler := &Handler{estimator: estimator, writer: writer, reader: reader, logger: logger}
	router := chi.NewRouter()
	router.Post("/assessments", handler.createAssessment)
	router.Get("/assessments/{assessmentID}", handler.getAssessment)
	router.Get("/assessments/{assessmentID}/sources", handler.getSources)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

type estimateRequest struct {
	SnapshotID    string                `json:"snapshotId"`
	RegionCode    string                `json:"regionCode"`
	HazardType    string                `json:"hazardType"`
	IntensityBand string                `json:"intensityBand"`
	Exposures     []lossdomain.Exposure `json:"exposures"`
}

func (h *Handler) createAssessment(w http.ResponseWriter, r *http.Request) {
	var request estimateRequest
	if err := decode(r, &request); err != nil {
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
	if err = h.writer.SaveAssessment(r.Context(), value); err != nil {
		h.writeError(w, r, fmt.Errorf("保存损失评估 %s: %w", value.ID, err))
		return
	}
	location := BasePath + "/assessments/" + value.ID
	w.Header().Set("Location", location)
	h.writeJSON(w, r, http.StatusCreated, successResponse{Data: sanitizeAssessment(value), RequestID: requestID(r)})
}

func (h *Handler) getAssessment(w http.ResponseWriter, r *http.Request) {
	value, err := h.loadAssessment(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: sanitizeAssessment(value), RequestID: requestID(r)})
}

func (h *Handler) getSources(w http.ResponseWriter, r *http.Request) {
	value, err := h.loadAssessment(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	sanitized := sanitizeAssessment(value)
	audit := sourceAudit{
		AssessmentID: sanitized.ID, SnapshotID: sanitized.SnapshotID, FormulaVersion: sanitized.FormulaVersion,
		Status: sanitized.Status, CalculatedAt: sanitized.CalculatedAt, InputReferences: sanitized.InputReferences,
		InputReferenceCount: len(sanitized.InputReferences), Scope: "评估记录中的输入引用",
		Limitations: []string{"审计结果仅包含评估时保存的引用，不代表源文件内容已在本服务留存"},
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: audit, RequestID: requestID(r)})
}

func (h *Handler) loadAssessment(r *http.Request) (lossdomain.Assessment, error) {
	id := chi.URLParam(r, "assessmentID")
	if !assessmentIDPattern.MatchString(id) {
		return lossdomain.Assessment{}, invalidParameter("损失评估标识")
	}
	value, err := h.reader.GetAssessment(r.Context(), id)
	if err != nil {
		return lossdomain.Assessment{}, fmt.Errorf("读取损失评估 %s: %w", id, err)
	}
	if err = value.Validate(); err != nil {
		return lossdomain.Assessment{}, fmt.Errorf("%w: %v", errStoredAssessment, err)
	}
	return value, nil
}

func (r estimateRequest) input() (applicationloss.EstimateInput, error) {
	if strings.TrimSpace(r.HazardType) == "" {
		return applicationloss.EstimateInput{}, fmt.Errorf("%w: 灾种不能为空", domain.ErrInvalidInput)
	}
	if len(r.Exposures) > 1000 {
		return applicationloss.EstimateInput{}, fmt.Errorf("%w: 单次最多提交 1000 条资产暴露", domain.ErrInvalidInput)
	}
	return applicationloss.EstimateInput{SnapshotID: r.SnapshotID, RegionCode: r.RegionCode,
		HazardType: hazardType(r.HazardType), IntensityBand: r.IntensityBand, Exposures: r.Exposures}, nil
}

func decode(request *http.Request, destination any) error {
	if request.ContentLength > maxRequestBytes {
		return fmt.Errorf("%w: 请求体超过 %d 字节", domain.ErrInvalidInput, maxRequestBytes)
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: 请求 JSON 无效", domain.ErrInvalidInput)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: 请求只能包含一个 JSON 对象", domain.ErrInvalidInput)
	}
	return nil
}

func hazardType(value string) hazarddomain.Type { return hazarddomain.Type(value) }

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusNotFound, "route_not_found", "接口不存在", nil)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许", nil)
}
