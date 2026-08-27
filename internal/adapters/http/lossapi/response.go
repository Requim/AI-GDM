package lossapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/domain"
	lossdomain "github.com/Requim/AI-GDM/internal/domain/loss"
)

type successResponse struct {
	Data      any    `json:"data"`
	RequestID string `json:"requestId"`
}

type sourceAudit struct {
	AssessmentID        string                      `json:"assessmentId"`
	SnapshotID          string                      `json:"snapshotId"`
	FormulaVersion      string                      `json:"formulaVersion"`
	Status              lossdomain.AssessmentStatus `json:"status"`
	CalculatedAt        time.Time                   `json:"calculatedAt"`
	InputReferences     []string                    `json:"inputReferences"`
	InputReferenceCount int                         `json:"inputReferenceCount"`
	Scope               string                      `json:"scope"`
	Limitations         []string                    `json:"limitations"`
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
	case errors.Is(err, errStoredAssessment):
		return http.StatusInternalServerError, "stored_assessment_invalid", "已保存的损失评估不可用"
	case errors.As(err, &parameter), errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request_timeout", "请求处理超时"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "请求已取消"
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "assessment_not_found", "未找到损失评估"
	case errors.Is(err, domain.ErrInsufficientData):
		return http.StatusServiceUnavailable, "insufficient_data", "损失评估数据不足"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable", "损失评估依赖暂时不可用"
	default:
		return http.StatusInternalServerError, "internal_error", "服务内部错误"
	}
}

func (h *Handler) writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string, cause error) {
	if cause != nil {
		h.logger.ErrorContext(r.Context(), "损失评估 API 请求失败", "status", status, "code", code, "request_id", requestID(r), "error", cause)
	}
	h.writeJSON(w, r, status, errorResponse{Error: apiError{Code: code, Message: message, RequestID: requestID(r)}})
}

func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "编码损失评估 API 响应失败", "error", err, "request_id", requestID(r))
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
	payload, _ := json.Marshal(errorResponse{Error: apiError{Code: "internal_error", Message: "服务内部错误", RequestID: requestID}})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(append(payload, '\n'))
}

func requestID(r *http.Request) string { return middleware.GetReqID(r.Context()) }

func sanitizeAssessment(value lossdomain.Assessment) lossdomain.Assessment {
	value.InputReferences = sanitizeReferences(value.InputReferences)
	return value
}

func sanitizeReferences(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if sanitized := sanitizeReference(value); sanitized != "" {
			result = append(result, sanitized)
		}
	}
	return result
}

func sanitizeReference(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return value
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		if normalized == "key" || normalized == "token" || normalized == "sig" || normalized == "signature" || normalized == "apikey" || normalized == "api_key" || normalized == "access_token" || normalized == "secret" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
