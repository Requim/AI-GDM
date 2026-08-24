package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server 管理 HTTP 路由和进程生命周期。
type Server struct {
	httpServer      *http.Server
	logger          *slog.Logger
	readiness       *Readiness
	shutdownTimeout time.Duration
}

// New 创建带基础中间件和健康探针的 HTTP 服务。
func New(addr string, timeout time.Duration, logger *slog.Logger) *Server {
	readiness := &Readiness{}
	router := routes(logger, readiness)
	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger:          logger,
		readiness:       readiness,
		shutdownTimeout: timeout,
	}
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

func routes(logger *slog.Logger, readiness *Readiness) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(accessLog(logger))
	router.Get("/healthz", healthHandler)
	router.Get("/readyz", readinessHandler(readiness))
	return router
}

func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Request-ID", middleware.GetReqID(r.Context()))
			started := time.Now()
			next.ServeHTTP(w, r)
			logger.InfoContext(r.Context(), "HTTP 请求完成",
				"method", r.Method, "path", r.URL.Path,
				"request_id", middleware.GetReqID(r.Context()),
				"duration_ms", time.Since(started).Milliseconds())
		})
	}
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
