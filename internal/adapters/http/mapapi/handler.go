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
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	applicationevacuation "github.com/Requim/AI-GDM/internal/application/evacuation"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/evacuation"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
	"github.com/Requim/AI-GDM/internal/ports"
)

// BasePath 是地图代理在主 HTTP 服务中的固定挂载路径。
const BasePath = "/map"

const (
	maxRequestBytes   = 16 << 10
	maxResponseBytes  = 2 << 20
	maxRequestIDBytes = 128
)

// Handler 将浏览器地图请求转发到服务端供应商适配器，浏览器永远不会接触高德密钥。
type Handler struct {
	facilities  applicationevacuation.FacilitySearcher
	routes      ports.RoutePlanner
	transit     ports.TransitRoutePlanner
	routeSafety applicationevacuation.RouteSafetySearcher
	authority   RouteAuthorityRecorder
	logger      *slog.Logger
}

// RouteAuthorityRecorder 为可见安全路线生成短期、无位置数据的 AI 权威引用。
type RouteAuthorityRecorder interface {
	RecordRoute(context.Context, hazard.Snapshot, evacuation.Route, string) (*report.AnalysisReference, error)
}

// New 创建相对于 BasePath 挂载的地图代理路由。
func New(facilities applicationevacuation.FacilitySearcher, routes ports.RoutePlanner,
	logger *slog.Logger,
) (http.Handler, error) {
	return NewWithSafety(facilities, routes, nil, logger)
}

// NewWithTransit 创建支持公交城市编码的地图代理路由。
func NewWithTransit(facilities applicationevacuation.FacilitySearcher, routes ports.RoutePlanner,
	transit ports.TransitRoutePlanner, logger *slog.Logger,
) (http.Handler, error) {
	return NewWithTransitAndSafety(facilities, routes, transit, nil, logger)
}

// NewWithSafety 创建带路线风险区过滤和安全排序的地图代理路由。
func NewWithSafety(facilities applicationevacuation.FacilitySearcher, routes ports.RoutePlanner,
	routeSafety applicationevacuation.RouteSafetySearcher, logger *slog.Logger,
) (http.Handler, error) {
	return NewWithTransitAndSafety(facilities, routes, nil, routeSafety, logger)
}

// NewWithTransitAndSafety 创建支持公交和路线安全评估的地图代理路由。
func NewWithTransitAndSafety(facilities applicationevacuation.FacilitySearcher, routes ports.RoutePlanner,
	transit ports.TransitRoutePlanner, routeSafety applicationevacuation.RouteSafetySearcher,
	logger *slog.Logger,
) (http.Handler, error) {
	return NewWithTransitSafetyAndAuthority(facilities, routes, transit, routeSafety, nil, logger)
}

// NewWithTransitSafetyAndAuthority 创建支持路线权威引用的完整地图代理路由。
func NewWithTransitSafetyAndAuthority(facilities applicationevacuation.FacilitySearcher,
	routes ports.RoutePlanner, transit ports.TransitRoutePlanner,
	routeSafety applicationevacuation.RouteSafetySearcher, authority RouteAuthorityRecorder,
	logger *slog.Logger,
) (http.Handler, error) {
	if facilities == nil || routes == nil || logger == nil {
		return nil, fmt.Errorf("地图代理依赖不能为空")
	}
	handler := &Handler{facilities: facilities, routes: routes, transit: transit,
		routeSafety: routeSafety, authority: authority, logger: logger}
	router := chi.NewRouter()
	router.Post("/places/nearby", handler.nearby)
	router.Post("/routes", handler.route)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

type nearbyRequest struct {
	HazardType hazard.Type             `json:"hazardType,omitempty"`
	Center     spatial.Point           `json:"center"`
	Kind       evacuation.FacilityType `json:"kind"`
	RadiusM    int                     `json:"radiusMeters"`
}

type routeRequest struct {
	HazardType      hazard.Type           `json:"hazardType,omitempty"`
	Origin          spatial.Point         `json:"origin"`
	Destination     spatial.Point         `json:"destination"`
	Mode            evacuation.TravelMode `json:"mode"`
	OriginCity      string                `json:"originCity,omitempty"`
	DestinationCity string                `json:"destinationCity,omitempty"`
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

var errHazardNotSupported = errors.New("设施筛选灾种尚未接入")

func (h *Handler) nearby(w http.ResponseWriter, r *http.Request) {
	var input nearbyRequest
	if err := decode(r, &input); err != nil {
		h.writeError(w, r, err)
		return
	}
	searchInput, err := normalizeNearby(input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.facilities.Search(r.Context(), searchInput)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	projected, err := buildNearbyResult(result)
	h.writeResult(w, r, projected, err)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	var input routeRequest
	if err := decode(r, &input); err != nil {
		h.writeError(w, r, err)
		return
	}
	if h.routeSafety != nil {
		searchInput, err := normalizeSafeRoute(input)
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		result, err := h.routeSafety.SearchRoutes(r.Context(), searchInput)
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		projected, err := buildSafeRouteResult(result)
		if err == nil {
			err = h.attachRouteAuthorities(r.Context(), &projected)
		}
		h.writeResult(w, r, projected, err)
		return
	}
	if err := validateRoute(input); err != nil {
		h.writeError(w, r, err)
		return
	}
	var result []evacuation.Route
	var err error
	if input.Mode == evacuation.TravelTransit {
		if h.transit == nil {
			err = fmt.Errorf("%w: 公交路线规划端口未配置", domain.ErrProviderUnavailable)
		} else {
			result, err = h.transit.PlanTransit(r.Context(), input.Origin, input.Destination,
				input.OriginCity, input.DestinationCity)
		}
	} else {
		result, err = h.routes.Plan(r.Context(), input.Origin, input.Destination, input.Mode)
	}
	h.writeResult(w, r, result, err)
}

func (h *Handler) attachRouteAuthorities(ctx context.Context, result *safeRouteResult) error {
	if h.authority == nil {
		return nil
	}
	cacheFailed, unavailable := false, false
	for index := range result.Routes {
		ref, err := h.authority.RecordRoute(ctx, result.Snapshot,
			result.Routes[index].Route, result.RuleVersion)
		if err != nil {
			cacheFailed = true
			h.logger.WarnContext(ctx, "路线权威引用记录失败", "route_id", result.Routes[index].ID, "error", err)
			continue
		}
		if ref == nil {
			unavailable = true
			continue
		}
		if !validRouteAuthorityReference(*ref) {
			cacheFailed = true
			continue
		}
		copyValue := *ref
		result.Routes[index].AnalysisRef = &copyValue
	}
	addAuthorityLimitations(result, cacheFailed, unavailable)
	return fitRouteResponse(result)
}

func validRouteAuthorityReference(value report.AnalysisReference) bool {
	normalized, err := value.Normalize()
	return err == nil && normalized == value && value.Kind == report.AuthorityEvacuationRoute
}

func addAuthorityLimitations(result *safeRouteResult, cacheFailed, unavailable bool) {
	if cacheFailed {
		result.Limitations = appendUnique(result.Limitations,
			"路线权威引用缓存失败；确定性路线仍可使用，但暂不能生成对应 AI 说明")
	}
	if unavailable {
		result.Limitations = appendUnique(result.Limitations,
			"路线权威缓存未配置或快照已过期，未生成对应 AI 说明引用")
	}
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

func normalizeNearby(input nearbyRequest) (applicationevacuation.SearchInput, error) {
	if input.HazardType == "" {
		input.HazardType = hazard.TypeLandslide
	}
	if input.HazardType != hazard.TypeLandslide {
		return applicationevacuation.SearchInput{}, fmt.Errorf("%w: 灾种 %q",
			errHazardNotSupported, input.HazardType)
	}
	if err := validatePoint(input.Center); err != nil {
		return applicationevacuation.SearchInput{}, fmt.Errorf("中心坐标: %w", err)
	}
	if input.RadiusM <= 0 || input.RadiusM > 50_000 {
		return applicationevacuation.SearchInput{}, fmt.Errorf(
			"%w: 搜索半径必须在 1 至 50000 米之间", domain.ErrInvalidInput)
	}
	switch input.Kind {
	case evacuation.FacilityShelter, evacuation.FacilityHospital, evacuation.FacilityTransport:
		return applicationevacuation.SearchInput{
			HazardType: input.HazardType, Center: input.Center,
			Kind: input.Kind, RadiusMeters: input.RadiusM,
		}, nil
	default:
		return applicationevacuation.SearchInput{}, fmt.Errorf("%w: 设施类型无效", domain.ErrInvalidInput)
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
	case evacuation.TravelDriving, evacuation.TravelWalking:
		if input.OriginCity != "" || input.DestinationCity != "" {
			return fmt.Errorf("%w: 非公交路线不需要城市编码", domain.ErrInvalidInput)
		}
		return nil
	case evacuation.TravelTransit:
		if err := validateCityCode(input.OriginCity, "起点城市"); err != nil {
			return err
		}
		return validateCityCode(input.DestinationCity, "终点城市")
	default:
		return fmt.Errorf("%w: 交通方式无效", domain.ErrInvalidInput)
	}
}

func normalizeSafeRoute(input routeRequest) (applicationevacuation.RouteSearchInput, error) {
	hazardType := input.HazardType
	if hazardType == "" {
		hazardType = hazard.TypeLandslide
	}
	if err := validatePoint(input.Origin); err != nil {
		return applicationevacuation.RouteSearchInput{}, fmt.Errorf("起点: %w", err)
	}
	if err := validatePoint(input.Destination); err != nil {
		return applicationevacuation.RouteSearchInput{}, fmt.Errorf("终点: %w", err)
	}
	switch input.Mode {
	case evacuation.TravelDriving, evacuation.TravelWalking:
		if input.OriginCity != "" || input.DestinationCity != "" {
			return applicationevacuation.RouteSearchInput{}, fmt.Errorf(
				"%w: 非公交路线不需要城市编码", domain.ErrInvalidInput)
		}
	case evacuation.TravelTransit:
		if err := validateCityCode(input.OriginCity, "起点城市"); err != nil {
			return applicationevacuation.RouteSearchInput{}, err
		}
		if err := validateCityCode(input.DestinationCity, "终点城市"); err != nil {
			return applicationevacuation.RouteSearchInput{}, err
		}
	default:
		return applicationevacuation.RouteSearchInput{}, fmt.Errorf("%w: 交通方式无效", domain.ErrInvalidInput)
	}
	return applicationevacuation.RouteSearchInput{
		HazardType: hazardType, Origin: input.Origin, Destination: input.Destination,
		Mode: input.Mode, OriginCity: input.OriginCity, DestinationCity: input.DestinationCity,
	}, nil
}

func validateCityCode(value, field string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 12 {
		return fmt.Errorf("%w: %s citycode 无效", domain.ErrInvalidInput, field)
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return fmt.Errorf("%w: %s 仅支持数字 citycode", domain.ErrInvalidInput, field)
		}
	}
	return nil
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
	case errors.Is(err, applicationevacuation.ErrUnsafeProviderResult),
		errors.Is(err, errUnsafeMapResult):
		return http.StatusServiceUnavailable, "unsafe_provider_result", "地图供应商结果不满足安全显示约束"
	case errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_request", "请求参数无效"
	case errors.Is(err, errHazardNotSupported):
		return http.StatusNotFound, "hazard_not_supported", "尚未接入该灾种的实时设施筛选能力"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request_timeout", "地图供应商请求超时"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "请求已取消"
	case errors.Is(err, domain.ErrProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable", "地图供应商暂时不可用"
	case errors.Is(err, domain.ErrInsufficientData):
		return http.StatusServiceUnavailable, "insufficient_data", "实时风险区数据不足，无法安全筛选设施"
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
		payload, _ = json.Marshal(errorResponse{Error: apiError{
			Code: "internal_error", Message: "服务内部错误", RequestID: requestID(r),
		}})
		status = http.StatusInternalServerError
	} else if len(payload) > maxResponseBytes {
		payload, _ = json.Marshal(errorResponse{Error: apiError{
			Code: "response_too_large", Message: "响应超过安全上限", RequestID: requestID(r),
		}})
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if id := requestID(r); id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func requestID(r *http.Request) string {
	value := middleware.GetReqID(r.Context())
	var result strings.Builder
	for index := 0; index < len(value) && result.Len() < maxRequestIDBytes; index++ {
		character := value[index]
		if requestIDCharacter(character) {
			result.WriteByte(character)
		}
	}
	return result.String()
}

func requestIDCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || strings.ContainsRune("-_.:", rune(value))
}
