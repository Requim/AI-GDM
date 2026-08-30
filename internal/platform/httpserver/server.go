package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	maxRequestIDBytes    = 128
	maxAccessLogPathSize = 256
)

var rejectedRequestSequence atomic.Uint64

// Server 管理 HTTP 路由和进程生命周期。
type Server struct {
	httpServer      *http.Server
	router          *chi.Mux
	logger          *slog.Logger
	readiness       *Readiness
	shutdownTimeout time.Duration
}

// New 创建带安全中间件和健康探针的 HTTP 服务。
func New(addr string, timeout time.Duration, logger *slog.Logger,
	security SecurityOptions,
) (*Server, error) {
	if logger == nil || strings.TrimSpace(addr) == "" || timeout <= 0 {
		return nil, fmt.Errorf("HTTP 地址、关闭超时或日志器无效")
	}
	readiness := &Readiness{}
	router, err := routes(logger, readiness, security)
	if err != nil {
		return nil, err
	}
	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    32 * 1024,
		},
		router:          router,
		logger:          logger,
		readiness:       readiness,
		shutdownTimeout: timeout,
	}, nil
}

// Mount 在服务启动前挂载一个独立 HTTP 适配器。
func (s *Server) Mount(pattern string, handler http.Handler) error {
	if s == nil || s.router == nil || handler == nil {
		return fmt.Errorf("HTTP 路由或处理器不能为空")
	}
	if pattern == "" || !strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "?#") {
		return fmt.Errorf("HTTP 挂载路径 %q 无效", pattern)
	}
	s.router.Mount(pattern, handler)
	return nil
}

// HandleExact 在服务启动前挂载一个精确路径 HTTP 适配器。
func (s *Server) HandleExact(pattern string, handler http.Handler) error {
	if s == nil || s.router == nil || handler == nil {
		return fmt.Errorf("HTTP 路由或处理器不能为空")
	}
	if pattern == "" || !strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "?#") {
		return fmt.Errorf("HTTP 挂载路径 %q 无效", pattern)
	}
	s.router.Handle(pattern, handler)
	return nil
}

// Handler 返回服务路由，供集成测试和后续模块挂载使用。
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Run 启动服务，并在上下文取消时优雅关闭。
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	s.readiness.Set(true)
	go func() { errCh <- s.httpServer.ListenAndServe() }()

	select {
	case err := <-errCh:
		s.readiness.Set(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return s.shutdown()
	}
}

func (s *Server) shutdown() error {
	s.readiness.Set(false)
	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	s.logger.Info("HTTP 服务正在关闭")
	return s.httpServer.Shutdown(ctx)
}

func routes(logger *slog.Logger, readiness *Readiness, options SecurityOptions) (*chi.Mux, error) {
	security, err := newRequestSecurity(options)
	if err != nil {
		return nil, err
	}
	router := chi.NewRouter()
	router.Use(securityHeaders)
	router.Use(security.rateLimit)
	router.Use(requestIDMiddleware(logger))
	router.Use(accessLog(logger))
	router.Use(middleware.Recoverer)
	router.Use(security.authorize)
	router.Use(csrfProtection)
	router.Get("/healthz", healthHandler)
	router.Get("/readyz", readinessHandler(readiness))
	return router, nil
}

func requestIDMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		withGeneratedID := normalizedGeneratedRequestID(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reason, lengthCategory, invalid := requestIDRejection(r.Header.Values(middleware.RequestIDHeader))
			if !invalid {
				withGeneratedID.ServeHTTP(w, r)
				return
			}
			r.Header.Del(middleware.RequestIDHeader)
			requestID := rejectedRequestID()
			logger.WarnContext(r.Context(), "拒绝无效请求标识", "request_id", requestID,
				"reason", reason, "length_category", lengthCategory)
			writeInvalidRequestID(w, requestID)
		})
	}
}

func normalizedGeneratedRequestID(next http.Handler) http.Handler {
	return middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := middleware.GetReqID(r.Context())
		if !validRequestID(value) {
			value = generatedRequestID(value)
		}
		ctx := context.WithValue(r.Context(), middleware.RequestIDKey, value)
		next.ServeHTTP(w, r.WithContext(ctx))
	}))
}

func requestIDRejection(values []string) (string, string, bool) {
	if len(values) == 0 {
		return "", "", false
	}
	if len(values) != 1 {
		return "multiple_values", "multiple", true
	}
	value := values[0]
	if len(value) > maxRequestIDBytes {
		return "too_long", "over_limit", true
	}
	if value == "" {
		return "empty", "empty", true
	}
	if !validRequestID(value) {
		return "invalid_character", "within_limit", true
	}
	return "", "", false
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !requestIDCharacter(character) {
			return false
		}
	}
	return true
}

func requestIDCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || strings.ContainsRune("-_.:", rune(value))
}

func generatedRequestID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("req-%x", digest[:16])
}

func rejectedRequestID() string {
	sequence := rejectedRequestSequence.Add(1)
	return generatedRequestID(fmt.Sprintf("rejected-%d-%d", time.Now().UnixNano(), sequence))
}

func earlyResponseRequest(w http.ResponseWriter, r *http.Request) *http.Request {
	requestID := rejectedRequestID()
	values := r.Header.Values(middleware.RequestIDHeader)
	if _, _, invalid := requestIDRejection(values); !invalid && len(values) == 1 {
		requestID = values[0]
	}
	w.Header().Set(middleware.RequestIDHeader, requestID)
	ctx := context.WithValue(r.Context(), middleware.RequestIDKey, requestID)
	return r.WithContext(ctx)
}

func writeInvalidRequestID(w http.ResponseWriter, requestID string) {
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
		"code": "invalid_request_id", "message": "请求标识无效", "requestId": requestID,
	}})
}

func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Request-ID", middleware.GetReqID(r.Context()))
			started := time.Now()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(wrapped, r)
			status := responseStatus(wrapped.Status())
			duration := time.Since(started)
			logger.InfoContext(r.Context(), "HTTP 请求完成",
				"method", r.Method, "path", accessLogPath(r.URL.Path),
				"request_id", middleware.GetReqID(r.Context()),
				"status", status, "outcome", responseOutcome(status),
				"duration_ms", duration.Milliseconds())
			auditProtectedWrite(logger, r, status, duration)
		})
	}
}

func auditProtectedWrite(logger *slog.Logger, r *http.Request, status int, duration time.Duration) {
	if !protectedWriteRequest(r) {
		return
	}
	logger.InfoContext(r.Context(), "受保护写操作审计",
		"action", writeAction(r.URL.Path), "method", r.Method,
		"request_id", middleware.GetReqID(r.Context()), "subject_type", "shared_admin_role",
		"status", status, "outcome", responseOutcome(status),
		"duration_ms", duration.Milliseconds())
}

func responseStatus(status int) int {
	if status == 0 {
		return http.StatusOK
	}
	return status
}

func responseOutcome(status int) string {
	switch {
	case status >= 200 && status < 400:
		return "success"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "denied"
	default:
		return "failure"
	}
}

func writeAction(path string) string {
	switch {
	case strings.HasSuffix(path, "/refresh"):
		return "hazard.refresh"
	case path == "/api/v1/loss/assessments":
		return "loss.assess"
	case path == "/api/v1/ai/report":
		return "ai.report"
	case strings.HasPrefix(path, "/api/v1/map/"):
		return "map.plan"
	case strings.HasPrefix(path, "/api/v1/survival/"):
		return "survival.replay"
	default:
		return "api.write"
	}
}

func accessLogPath(path string) string {
	if len(path) <= maxAccessLogPathSize {
		return path
	}
	digest := sha256.Sum256([]byte(path))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, "ok")
}

func readinessHandler(readiness *Readiness) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !readiness.IsReady() {
			writeStatus(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeStatus(w, http.StatusOK, "ready")
	}
}

func writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
