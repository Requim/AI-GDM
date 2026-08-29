// Package survivalapi 暴露历史案例回放和生还辅助评估 HTTP 接口。
package survivalapi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
)

// BasePath 是回放 API 在 /api/v1 下的固定路径。
const BasePath = "/survival"

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
	router.Post("/replays/cases/{caseID}/assessment", handler.assessCase)
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
	if err = validateCaseSummaries(values); err != nil {
		h.writeError(w, r, invalidServiceResponse("案例目录", err))
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: values, RequestID: requestID(r)})
}

func validateCaseSummaries(values []applicationsurvival.HistoricalCase) error {
	if len(values) > applicationsurvival.MaxCatalogCases {
		return fmt.Errorf("%w: 案例目录超过 %d 条", domain.ErrInvalidInput, applicationsurvival.MaxCatalogCases)
	}
	caseIDs := make(map[string]struct{}, len(values))
	scenarioIDs := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("%w: 案例目录第 %d 项无效: %w", domain.ErrInvalidInput, index+1, err)
		}
		if _, exists := caseIDs[value.Event.ID]; exists {
			return fmt.Errorf("%w: 案例目录包含重复案例", domain.ErrInvalidInput)
		}
		if _, exists := scenarioIDs[value.ScenarioID]; exists {
			return fmt.Errorf("%w: 案例目录包含重复场景", domain.ErrInvalidInput)
		}
		caseIDs[value.Event.ID] = struct{}{}
		scenarioIDs[value.ScenarioID] = struct{}{}
	}
	return nil
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
	if err = value.Validate(); err != nil {
		h.writeError(w, r, invalidServiceResponse("案例详情", err))
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: value, RequestID: requestID(r)})
}

func (h *Handler) assessCase(w http.ResponseWriter, r *http.Request) {
	id, err := identifierFromRequest(r, "caseID")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err = requireEmptyBody(r); err != nil {
		h.writeError(w, r, err)
		return
	}
	detail, err := h.catalog.GetCase(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err = detail.Validate(); err != nil {
		h.writeError(w, r, invalidServiceResponse("案例详情", err))
		return
	}
	value, err := h.assessment.AssessCase(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err = value.Validate(); err != nil {
		h.writeError(w, r, invalidServiceResponse("案例评估", err))
		return
	}
	if err = validateReplayBinding(detail, value); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: value, RequestID: requestID(r)})
}

func validateReplayBinding(detail applicationsurvival.HistoricalCaseDetail,
	value applicationsurvival.ReplayAssessment,
) error {
	if detail.Event.ID != value.CaseID || detail.Scenario.ID != value.ScenarioID ||
		detail.ScenarioDigest != value.ScenarioDigest || detail.Usage != value.Usage {
		return fmt.Errorf("%w: 案例详情与评估结果绑定不一致", domain.ErrInsufficientData)
	}
	if value.Assessment.CalculatedAt.Before(detail.Scenario.AsOf) {
		return fmt.Errorf("%w: 案例评估时刻早于回放场景", domain.ErrInsufficientData)
	}
	return nil
}

func invalidServiceResponse(name string, err error) error {
	return fmt.Errorf("%s响应无效: %w", name, errors.Join(domain.ErrInsufficientData, err))
}

func requireEmptyBody(r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(payload) != 0 {
		return invalidParameter("请求体")
	}
	return nil
}

func (h *Handler) modelCard(w http.ResponseWriter, r *http.Request) {
	value := survivaldomain.DefaultModelCard()
	if err := value.Validate(); err != nil {
		h.writeError(w, r, invalidServiceResponse("模型卡", err))
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{
		Data: value, RequestID: requestID(r),
	})
}

func identifierFromRequest(r *http.Request, name string) (string, error) {
	value := chi.URLParam(r, name)
	if err := applicationsurvival.ValidateIdentifier(value); err != nil {
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
