// Package aiapi 暴露可审计的智能研判编排接口。
package aiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/domain"
)

// BasePath 是智能研判 API 在 /api/v1 下的固定路径。
const BasePath = "/ai"

const maxRequestBytes = 1 << 20

// Reporter 是 HTTP 适配器使用的最小编排端口。
type Reporter interface {
	// Generate 组合确定性分析、搜索证据和解释性报告。
	Generate(context.Context, applicationagent.Input) (applicationagent.Result, error)
}

// Handler 将智能研判用例暴露为严格 JSON 接口。
type Handler struct {
	reporter Reporter
	logger   *slog.Logger
}

// New 创建相对于 BasePath 挂载的智能研判路由。
func New(reporter Reporter, logger *slog.Logger) (http.Handler, error) {
	if reporter == nil || logger == nil {
		return nil, fmt.Errorf("智能研判服务或日志器不能为空")
	}
	handler := &Handler{reporter: reporter, logger: logger}
	router := chi.NewRouter()
	router.Post("/report", handler.report)
	router.NotFound(handler.notFound)
	router.MethodNotAllowed(handler.methodNotAllowed)
	return router, nil
}

type reportRequest struct {
	Query           string          `json:"query"`
	AnalysisJSON    json.RawMessage `json:"analysis"`
	ImmutableFields []string        `json:"immutableFields"`
	EvidenceLimit   int             `json:"evidenceLimit,omitempty"`
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request) {
	var request reportRequest
	if err := decode(r, &request); err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.reporter.Generate(r.Context(), applicationagent.Input{
		Query: request.Query, AnalysisJSON: request.AnalysisJSON,
		ImmutableFields: request.ImmutableFields, EvidenceLimit: request.EvidenceLimit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err = result.Validate(); err != nil {
		h.writeError(w, r, fmt.Errorf("校验智能研判结果: %w", err))
		return
	}
	h.writeJSON(w, r, http.StatusOK, successResponse{Data: result, RequestID: requestID(r)})
}

func decode(request *http.Request, destination any) error {
	if request.Body == nil {
		return fmt.Errorf("%w: 请求体为空", domain.ErrInvalidInput)
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil {
		return fmt.Errorf("%w: 读取请求体失败", domain.ErrInvalidInput)
	}
	if len(body) > maxRequestBytes {
		return fmt.Errorf("%w: 请求体超过 %d 字节", domain.ErrInvalidInput, maxRequestBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: 请求 JSON 无效", domain.ErrInvalidInput)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: 请求只能包含一个 JSON 对象", domain.ErrInvalidInput)
	}
	return nil
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusNotFound, "route_not_found", "接口不存在", nil)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许", nil)
}
