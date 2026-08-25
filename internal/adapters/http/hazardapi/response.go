package hazardapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	applicationhazard "github.com/Requim/AI-GDM/internal/application/hazard"
	"github.com/Requim/AI-GDM/internal/domain"
	domainhazard "github.com/Requim/AI-GDM/internal/domain/hazard"
)

type successResponse struct {
	Data      applicationhazard.RiskResult `json:"data"`
	RequestID string                       `json:"requestId"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

type parameterError struct {
	name string
}

func (e parameterError) Error() string {
	return e.name + "无效"
}

func invalidParameter(name string) error {
	return parameterError{name: name}
}

func (h *Handler) writeResult(w http.ResponseWriter, r *http.Request,
	result applicationhazard.RiskResult, err error,
) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if result.Zones == nil {
		result.Zones = make([]domainhazard.RiskZone, 0)
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{
		Data: result, RequestID: requestID(r),
	})
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyError(err)
	h.writeAPIError(w, r, status, code, message, err)
}

func classifyError(err error) (int, string, string) {
	var parameter parameterError
	switch {
	case errors.As(err, &parameter), errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, applicationhazard.ErrHazardNotSupported):
		return http.StatusNotFound, "hazard_not_supported", "尚未接入该灾种的实时能力"
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "risk_not_found", "未找到风险结果"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request_timeout", "请求处理超时"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "请求已取消"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable", "外部数据供应商暂时不可用"
	case errors.Is(err, domain.ErrInsufficientData):
		return http.StatusServiceUnavailable, "insufficient_data", "实时风险数据不足"
	default:
		return http.StatusInternalServerError, "internal_error", "服务内部错误"
	}
}

func (h *Handler) writeAPIError(w http.ResponseWriter, r *http.Request, status int,
	code, message string, cause error,
) {
	if cause != nil {
		h.logger.ErrorContext(r.Context(), "预警 API 请求失败",
			"status", status, "code", code, "request_id", requestID(r), "error", cause)
	}
	h.writeJSON(w, r, status, errorResponse{Error: apiError{
		Code: code, Message: message, RequestID: requestID(r),
	}})
}

func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "编码预警 API 响应失败", "error", err,
			"request_id", requestID(r))
		writeFallbackError(w, requestID(r))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if id := requestID(r); id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func writeFallbackError(w http.ResponseWriter, requestID string) {
	payload, _ := json.Marshal(errorResponse{Error: apiError{
		Code: "internal_error", Message: "服务内部错误", RequestID: requestID,
	}})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(append(payload, '\n'))
}

func requestID(r *http.Request) string {
	return middleware.GetReqID(r.Context())
}
