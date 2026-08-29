package aiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

const (
	maxAIReportResponseBytes = 1 << 20
)

type successResponse struct {
	Data      any    `json:"data"`
	RequestID string `json:"requestId"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyError(err)
	h.writeAPIError(w, r, status, code, message, err)
}

func classifyError(err error) (int, string, string) {
	switch {
	case errors.Is(err, report.ErrUnsafeStoredAnalysis):
		return http.StatusServiceUnavailable, "unsafe_authority", "权威分析包含不安全字段，暂不可用于智能研判"
	case errors.Is(err, report.ErrInvalidAuthority):
		return http.StatusInternalServerError, "invalid_authority", "服务端权威分析无效"
	case errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "authority_not_found", "权威分析引用不存在"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request_timeout", "请求处理超时"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "请求已取消"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable", "智能研判外部供应商暂时不可用"
	default:
		return http.StatusInternalServerError, "internal_error", "服务内部错误"
	}
}

func (h *Handler) writeAPIError(w http.ResponseWriter, r *http.Request, status int,
	code, message string, cause error,
) {
	if cause != nil {
		h.logger.ErrorContext(r.Context(), "智能研判 API 请求失败",
			"status", status, "code", code, "request_id", requestID(r), "error", cause)
	}
	h.writeJSON(w, r, status, errorResponse{Error: apiError{
		Code: code, Message: message, RequestID: requestID(r),
	}})
}

func (h *Handler) writeReport(w http.ResponseWriter, r *http.Request, result applicationagent.Result) {
	payload, err := boundedReportPayload(result, requestID(r))
	if err != nil {
		h.logger.ErrorContext(r.Context(), "智能研判 API 响应超过安全预算",
			"error", err, "request_id", requestID(r))
		writeFallbackError(w, requestID(r))
		return
	}
	h.writePayload(w, r, http.StatusOK, payload)
}

func boundedReportPayload(result applicationagent.Result, id string) ([]byte, error) {
	payload, err := json.Marshal(successResponse{Data: result, RequestID: id})
	if err != nil {
		return nil, fmt.Errorf("编码智能研判响应: %w", err)
	}
	if len(payload)+1 > maxAIReportResponseBytes {
		return nil, fmt.Errorf("智能研判响应超过 %d 字节", maxAIReportResponseBytes)
	}
	return payload, nil
}

func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "编码智能研判 API 响应失败",
			"error", err, "request_id", requestID(r))
		writeFallbackError(w, requestID(r))
		return
	}
	h.writePayload(w, r, status, payload)
}

func (h *Handler) writePayload(w http.ResponseWriter, r *http.Request, status int, payload []byte) {
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

func requestID(r *http.Request) string { return middleware.GetReqID(r.Context()) }
