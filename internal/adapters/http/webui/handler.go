// Package webui 使用嵌入式 Go 模板提供中文监控控制台。
package webui

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Requim/AI-GDM/internal/application/dashboard"
)

//go:embed templates/*.html static
var content embed.FS

// OverviewService 是控制台 HTTP 适配器使用的最小应用接口。
type OverviewService interface {
	Overview(context.Context) dashboard.Overview
}

// Handler 渲染监控控制台和嵌入式静态资源。
type Handler struct {
	service  OverviewService
	template *template.Template
	logger   *slog.Logger
}

// New 创建控制台路由。
func New(service OverviewService, logger *slog.Logger) (http.Handler, error) {
	if service == nil || logger == nil {
		return nil, fmt.Errorf("控制台服务或日志器不能为空")
	}
	parsed, err := template.New("index.html").ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("解析控制台模板: %w", err)
	}
	handler := &Handler{service: service, template: parsed, logger: logger}
	router := chi.NewRouter()
	router.Get("/", handler.index)
	router.Get("/assets/*", handler.asset)
	router.NotFound(handler.notFound)
	return router, nil
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	overview := h.service.Overview(r.Context())
	var payload bytes.Buffer
	if err := h.template.ExecuteTemplate(&payload, "index.html", newPage(overview)); err != nil {
		h.logger.ErrorContext(r.Context(), "渲染监控控制台失败", "error", err,
			"request_id", middleware.GetReqID(r.Context()))
		http.Error(w, "控制台暂时不可用", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if id := middleware.GetReqID(r.Context()); id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	_, _ = w.Write(payload.Bytes())
}

func (h *Handler) asset(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	clean := strings.TrimPrefix(path.Clean("/"+requested), "/")
	if requested == "" || clean != requested || strings.HasPrefix(clean, ".") {
		h.notFound(w, r)
		return
	}
	payload, err := content.ReadFile("static/" + clean)
	if err != nil {
		h.notFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(clean))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", assetCacheControl(clean))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(payload)
}

func assetCacheControl(name string) string {
	switch path.Ext(name) {
	case ".js", ".css":
		return "no-cache"
	default:
		return "public, max-age=3600"
	}
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "页面不存在", http.StatusNotFound)
}

type pageView struct {
	Environment string
	Version     string
	GeneratedAt string
	Summary     dashboard.Summary
	Sources     []sourceView
}

type sourceView struct {
	ID        string
	Name      string
	Provider  string
	Category  string
	State     string
	StateText string
	UpdatedAt string
	ValidTo   string
	Detail    string
}

func newPage(value dashboard.Overview) pageView {
	sources := make([]sourceView, 0, len(value.Sources))
	for _, source := range value.Sources {
		sources = append(sources, newSourceView(source))
	}
	return pageView{Environment: value.Environment, Version: value.Version,
		GeneratedAt: formatTime(value.GeneratedAt), Summary: value.Summary, Sources: sources}
}

func newSourceView(source dashboard.SourceStatus) sourceView {
	return sourceView{ID: source.ID, Name: source.Name, Provider: source.Provider,
		Category: source.Category, State: string(source.State), StateText: stateText(source.State),
		UpdatedAt: formatOptionalTime(source.UpdatedAt), ValidTo: formatOptionalTime(source.ValidTo),
		Detail: source.Detail}
}

func stateText(state dashboard.SourceState) string {
	labels := map[dashboard.SourceState]string{
		dashboard.StateAvailable: "可用", dashboard.StateStale: "已过期",
		dashboard.StateWaiting: "等待数据", dashboard.StateConfigured: "已配置",
		dashboard.StateDisabled: "未启用", dashboard.StateUnavailable: "不可用",
	}
	return labels[state]
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return "未提供"
	}
	return formatTime(value)
}

func formatTime(value time.Time) string {
	china := time.FixedZone("UTC+8", 8*60*60)
	return value.In(china).Format("2006-01-02 15:04:05 UTC+8")
}
