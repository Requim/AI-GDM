// Package mapapi 暴露不携带供应商密钥的地图业务代理接口。
package mapapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

// BasePath 是地图代理在主 HTTP 服务中的固定挂载路径。
const BasePath = "/map"

const maxRequestBytes = 16 << 10

// Handler 将浏览器地图请求转发到服务端供应商适配器，浏览器永远不会接触高德密钥。
type Handler struct {
	places ports.PlaceFinder
	routes ports.RoutePlanner
	logger *slog.Logger
}

// New 创建相对于 BasePath 挂载的地图代理路由。
func New(places ports.PlaceFinder, routes ports.RoutePlanner, logger *slog.Logger) (http.Handler, error) {
	if places == nil || routes == nil || logger == nil {
		return nil, fmt.Errorf("地图代理依赖不能为空")
	}
	handler := &Handler{places: places, routes: routes, logger: logger}
	router := chi.NewRouter()
	router.Post("/places/nearby", handler.nearby)
	router.Post("/routes", handler.route)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

type nearbyRequest struct {
	Center  spatial.Point           `json:"center"`
	Kind    evacuation.FacilityType `json:"kind"`
	RadiusM int                     `json:"radiusMeters"`
}

type routeRequest struct {
	Origin      spatial.Point         `json:"origin"`
	Destination spatial.Point         `json:"destination"`
	Mode        evacuation.TravelMode `json:"mode"`
}

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

func (h *Handler) nearby(w http.ResponseWriter, r *http.Request) {
	var input nearbyRequest
	if err := decode(r, &input); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := validateNearby(input); err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.places.FindNearby(r.Context(), input.Center, input.Kind, input.RadiusM)
	h.writeResult(w, r, result, err)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	var input routeRequest
	if err := decode(r, &input); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := validateRoute(input); err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.routes.Plan(r.Context(), input.Origin, input.Destination, input.Mode)
	h.writeResult(w, r, result, err)
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

func validateNearby(input nearbyRequest) error {
	if err := validatePoint(input.Center); err != nil {
		return fmt.Errorf("中心坐标: %w", err)
	}
	if input.RadiusM <= 0 || input.RadiusM > 50_000 {
		return fmt.Errorf("%w: 搜索半径必须在 1 至 50000 米之间", domain.ErrInvalidInput)
	}
	switch input.Kind {
	case evacuation.FacilityShelter, evacuation.FacilityHospital, evacuation.FacilityTransport:
		return nil
	default:
		return fmt.Errorf("%w: 设施类型无效", domain.ErrInvalidInput)
	}
}

func validateRoute(input routeRequest) error {
	if err := validatePoint(input.Origin); err != nil {
		return fmt.Errorf("起点: %w", err)
	}
	if err := validatePoint(input.Destination); err != nil {
		return fmt.Errorf("终点: %w", err)
	}
	switch input.Mode {
	case evacuation.TravelDriving, evacuation.TravelWalking, evacuation.TravelTransit:
		if input.Mode == evacuation.TravelTransit {
			return fmt.Errorf("%w: 公交路线需要城市参数，当前接口暂不支持", domain.ErrInvalidInput)
		}
		return nil
	default:
		return fmt.Errorf("%w: 交通方式无效", domain.ErrInvalidInput)
	}
}

func validatePoint(point spatial.Point) error {
	if math.IsNaN(point.Longitude) || math.IsInf(point.Longitude, 0) ||
		math.IsNaN(point.Latitude) || math.IsInf(point.Latitude, 0) {
		return fmt.Errorf("%w: 坐标必须是有限数值", domain.ErrInvalidInput)
	}
	return point.Validate()
}

func (h *Handler) writeResult(w http.ResponseWriter, r *http.Request, data any, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, successResponse{Data: data, RequestID: requestID(r)})
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyError(err)
	h.logger.ErrorContext(r.Context(), "地图代理请求失败", "status", status,
		"code", code, "request_id", requestID(r), "error", err)
	writeJSON(w, r, status, errorResponse{Error: apiError{
		Code: code, Message: message, RequestID: requestID(r),
	}})
}

func classifyError(err error) (int, string, string) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request_timeout", "地图供应商请求超时"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "请求已取消"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable", "地图供应商暂时不可用"
	default:
		return http.StatusInternalServerError, "internal_error", "服务内部错误"
	}
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusNotFound, errorResponse{Error: apiError{
		Code: "route_not_found", Message: "接口不存在", RequestID: requestID(r),
	}})
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusMethodNotAllowed, errorResponse{Error: apiError{
		Code: "method_not_allowed", Message: "请求方法不允许", RequestID: requestID(r),
	}})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"error":{"code":"internal_error","message":"服务内部错误"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if id := requestID(r); id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func requestID(r *http.Request) string {
	return middleware.GetReqID(r.Context())
}
