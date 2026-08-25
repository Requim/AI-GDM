package hazardapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	applicationhazard "github.com/Requim/AI-GDM/internal/application/hazard"
	domainhazard "github.com/Requim/AI-GDM/internal/domain/hazard"
)

// BasePath 是预警 API 在主 HTTP 服务中的固定挂载路径。
const BasePath = "/api/v1"

var (
	hazardTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	snapshotIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// RiskService 是预警 HTTP 适配器使用的最小应用服务接口。
type RiskService interface {
	Latest(context.Context, domainhazard.Type) (applicationhazard.RiskResult, error)
	Get(context.Context, domainhazard.Type, string) (applicationhazard.RiskResult, error)
	Refresh(context.Context, domainhazard.Type) (applicationhazard.RiskResult, error)
}

// Handler 把风险查询和刷新用例暴露为 JSON API。
type Handler struct {
	service RiskService
	logger  *slog.Logger
}

// New 创建相对于 BasePath 挂载的预警路由。
func New(service RiskService, logger *slog.Logger) (http.Handler, error) {
	if service == nil || logger == nil {
		return nil, fmt.Errorf("预警 HTTP 服务或日志器不能为空")
	}
	handler := &Handler{service: service, logger: logger}
	router := chi.NewRouter()
	router.Get("/hazards/{hazardType}/risks/latest", handler.latest)
	router.Get("/hazards/{hazardType}/risks/{snapshotID}", handler.detail)
	router.Post("/hazards/{hazardType}/refresh", handler.refresh)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

func (h *Handler) latest(w http.ResponseWriter, r *http.Request) {
	hazardType, err := hazardTypeFromRequest(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.service.Latest(r.Context(), hazardType)
	h.writeResult(w, r, result, err)
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	hazardType, err := hazardTypeFromRequest(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	snapshotID, err := snapshotIDFromRequest(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.service.Get(r.Context(), hazardType, snapshotID)
	h.writeResult(w, r, result, err)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	hazardType, err := hazardTypeFromRequest(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.service.Refresh(r.Context(), hazardType)
	h.writeResult(w, r, result, err)
}

func hazardTypeFromRequest(r *http.Request) (domainhazard.Type, error) {
	value := chi.URLParam(r, "hazardType")
	if value == "" || value != strings.TrimSpace(value) || !hazardTypePattern.MatchString(value) {
		return "", invalidParameter("灾种标识")
	}
	return domainhazard.Type(value), nil
}

func snapshotIDFromRequest(r *http.Request) (string, error) {
	value := chi.URLParam(r, "snapshotID")
	if value == "" || value != strings.TrimSpace(value) || !snapshotIDPattern.MatchString(value) {
		return "", invalidParameter("风险快照标识")
	}
	return value, nil
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusNotFound, "route_not_found", "接口不存在", nil)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许", nil)
}
