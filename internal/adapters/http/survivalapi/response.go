package survivalapi

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/domain"
)

const (
	maxResponseBytes       = 1 << 20
	maxResponseItems       = 1000
	maxResponseStringBytes = 4096
	maxResponseTotalItems  = 5000
	maxResponseTotalChars  = 512 << 10
	maxResponseDepth       = 16
	fallbackErrorPayload   = "{\"error\":{\"code\":\"internal_error\",\"message\":\"服务内部错误\",\"requestId\":\"\"}}\n"
)

var (
	timeValueType     = reflect.TypeOf(time.Time{})
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
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

type parameterError struct{ name string }

func (e parameterError) Error() string { return e.name + "无效" }

func invalidParameter(name string) error { return parameterError{name: name} }

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyError(err)
	h.writeAPIError(w, r, status, code, message, err)
}

func classifyError(err error) (int, string, string) {
	var parameter parameterError
	switch {
	case errors.As(err, &parameter):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, domain.ErrInsufficientData):
		return http.StatusServiceUnavailable, "insufficient_data", "生还评估数据不足"
	case errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "replay_not_found", "未找到历史回放数据"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request_timeout", "请求处理超时"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "请求已取消"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable", "外部数据供应商暂时不可用"
	default:
		return http.StatusInternalServerError, "internal_error", "服务内部错误"
	}
}

func (h *Handler) writeAPIError(w http.ResponseWriter, r *http.Request, status int,
	code, message string, cause error,
) {
	if cause != nil {
		h.logger.ErrorContext(r.Context(), "生还回放 API 请求失败",
			"status", status, "code", code, "request_id", requestID(r), "error", cause)
	}
	h.writeJSON(w, r, status, errorResponse{Error: apiError{
		Code: code, Message: message, RequestID: requestID(r),
	}})
}

func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	payload, err := encodeResponse(value)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "编码生还回放 API 响应失败", "error", err,
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
	payload, err := encodeResponse(errorResponse{Error: apiError{
		Code: "internal_error", Message: "服务内部错误", RequestID: requestID,
	}})
	if err != nil {
		payload = []byte(fallbackErrorPayload)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(payload)
}

func requestID(r *http.Request) string { return middleware.GetReqID(r.Context()) }

type responseBudget struct {
	items int
	chars int
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

func validateBoundedValue(value reflect.Value, budget *responseBudget, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > maxResponseDepth {
		return fmt.Errorf("响应结构嵌套过深")
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateBoundedValue(value.Elem(), budget, depth+1)
	}
	if isTimeValueType(value.Type()) {
		return nil
	}
	if hasCustomMarshaler(value.Type()) {
		return fmt.Errorf("响应包含未受支持的自定义编码器")
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return validateBoundedValue(value.Elem(), budget, depth+1)
	}
	if err := addScalarBudget(value, budget); err != nil {
		return err
	}
	return validateBoundedChildren(value, budget, depth)
}

func isTimeValueType(value reflect.Type) bool {
	return value == timeValueType || (value.Kind() == reflect.Pointer && value.Elem() == timeValueType)
}

func hasCustomMarshaler(value reflect.Type) bool {
	if implementsCustomMarshaler(value) {
		return true
	}
	return value.Kind() != reflect.Pointer && implementsCustomMarshaler(reflect.PointerTo(value))
}

func implementsCustomMarshaler(value reflect.Type) bool {
	return value.Implements(jsonMarshalerType) || value.Implements(textMarshalerType)
}

func addScalarBudget(value reflect.Value, budget *responseBudget) error {
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
	return nil
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
