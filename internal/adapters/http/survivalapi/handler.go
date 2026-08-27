// Package survivalapi 暴露历史案例回放和生还辅助评估 HTTP 接口。
package survivalapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

// BasePath 是回放 API 在 /api/v1 下的固定路径。
const BasePath = "/survival"

var identifierPattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")

// Handler 把历史案例、规则评估和模型卡暴露为 JSON API。
type Handler struct {
	catalog    applicationsurvival.CatalogService
	assessment applicationsurvival.AssessmentService
	logger     *slog.Logger
}

// New 创建相对于 BasePath 挂载的回放路由。

func New(catalog applicationsurvival.CatalogService, assessment applicationsurvival.AssessmentService,
	logger *slog.Logger,
) (http.Handler, error) {
	if catalog == nil || assessment == nil || logger == nil {
		return nil, fmt.Errorf("生还回放 HTTP 服务或日志器不能为空")
	}
	handler := &Handler{catalog: catalog, assessment: assessment, logger: logger}
	router := chi.NewRouter()
	router.Get("/cases", handler.listCases)
	router.Get("/cases/{caseID}", handler.getCase)
	router.Post("/scenarios/{scenarioID}/assess", handler.assess)
	router.Get("/model-card", handler.modelCard)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

func (h *Handler) listCases(w http.ResponseWriter, r *http.Request) {
	values, err := h.catalog.ListCases(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if values == nil {
		values = make([]applicationsurvival.HistoricalCase, 0)
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: values, RequestID: requestID(r)})
}

func (h *Handler) getCase(w http.ResponseWriter, r *http.Request) {
	id, err := identifierFromRequest(r, "caseID")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	value, err := h.catalog.GetCase(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: value, RequestID: requestID(r)})
}

func (h *Handler) assess(w http.ResponseWriter, r *http.Request) {
	id, err := identifierFromRequest(r, "scenarioID")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	value, err := h.assessment.Assess(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: value, RequestID: requestID(r)})
}

func (h *Handler) modelCard(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, r, http.StatusOK, successResponse{
		Data: survivaldomain.DefaultModelCard(), RequestID: requestID(r),
	})
}

func identifierFromRequest(r *http.Request, name string) (string, error) {
	value := chi.URLParam(r, name)
	if !identifierPattern.MatchString(value) {
		return "", invalidParameter("回放标识")
	}
	return value, nil
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusNotFound, "route_not_found", "接口不存在", nil)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许", nil)
}
