// Package lossapi 暴露灾害损失计算、查询和来源审计接口。
package lossapi

import (
	"bytes"
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

	"github.com/go-chi/chi/v5"

	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
	"github.com/Requim/AI-GDM/internal/ports"
)

// BasePath 是损失评估 API 在 /api/v1 下的固定路径。
const BasePath = "/loss"

const maxRequestBytes = 1 << 20

var assessmentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var publicBasePathPattern = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+/)*[A-Za-z0-9._~-]+$`)

var errStoredAssessment = errors.New("已保存损失评估无效")

// Handler 把损失计算用例和评估仓储暴露为 JSON API。
type Handler struct {
	estimator      applicationloss.AssessmentService
	writer         ports.LossAssessmentWriter
	reader         ports.LossAssessmentReader
	logger         *slog.Logger
	publicBasePath string
}

// New 创建相对于 BasePath 挂载的损失评估路由。
func New(estimator applicationloss.AssessmentService, writer ports.LossAssessmentWriter,
	reader ports.LossAssessmentReader, publicBasePath string, logger *slog.Logger) (http.Handler, error) {
	if estimator == nil || writer == nil || reader == nil || logger == nil {
		return nil, fmt.Errorf("损失评估 HTTP 服务、仓储或日志器不能为空")
	}
	publicBasePath, err := validatePublicBasePath(publicBasePath)
	if err != nil {
		return nil, err
	}
	handler := &Handler{estimator: estimator, writer: writer, reader: reader, logger: logger, publicBasePath: publicBasePath}
	router := chi.NewRouter()
	router.Post("/assessments", handler.createAssessment)
	router.Get("/assessments/{assessmentID}", handler.getAssessment)
	router.Get("/assessments/{assessmentID}/sources", handler.getSources)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

type estimateRequest struct {
	SnapshotID string `json:"snapshotId"`
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
	return applicationloss.EstimateInput{SnapshotID: r.SnapshotID}, nil
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
