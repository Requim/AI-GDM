package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/http/aiapi"
	"github.com/Requim/AI-GDM/internal/adapters/provider/bocha"
	"github.com/Requim/AI-GDM/internal/adapters/provider/chatcompletions"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	applicationagent "github.com/Requim/AI-GDM/internal/application/agent"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/resources"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	aiSearchHTTPTimeout            = 6 * time.Second
	aiNarrativeFallbackHTTPTimeout = 10 * time.Second
	aiNarrativeStageSafetyMargin   = 1 * time.Second
	aiRequestRate                  = 500 * time.Millisecond
	aiMaxOutputAttempts            = 3
)

// newAIService 在组合根装配可选的博查和 LLM 适配器。
func newAIService(cfg config.Config, dependencies *resources.Resources,
	resolver applicationagent.AuthoritativeAnalysisResolver, logger *slog.Logger,
) (*applicationagent.Service, error) {
	return buildAIService(cfg, dependencies, resolver, logger, nil)
}

func newAIServiceWithObservations(cfg config.Config, dependencies *resources.Resources,
	resolver applicationagent.AuthoritativeAnalysisResolver, logger *slog.Logger,
	recorder ports.ObservationRecorder,
) (aiReporter, error) {
	service, err := buildAIService(cfg, dependencies, resolver, logger, recorder)
	if err != nil || recorder == nil {
		return service, err
	}
	return observedAIReporter{inner: service, recorder: recorder}, nil
}

func buildAIService(cfg config.Config, dependencies *resources.Resources,
	resolver applicationagent.AuthoritativeAnalysisResolver, logger *slog.Logger,
	recorder ports.ObservationRecorder,
) (*applicationagent.Service, error) {
	search, err := newEvidenceSearcher(cfg, dependencies, logger)
	if err != nil {
		return nil, err
	}
	generator, err := newNarrativeGenerator(cfg, dependencies, logger)
	if err != nil {
		return nil, err
	}
	if search != nil && recorder != nil {
		search = observedEvidenceSearcher{inner: search}
	}
	if generator != nil && recorder != nil {
		generator = observedNarrativeGenerator{inner: generator}
	}
	service, err := applicationagent.New(resolver, search, generator, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建智能研判编排服务: %w", err)
	}
	return service, nil
}

// newAIHandler 将可选供应商装配为 /api/v1/ai 的 HTTP 适配器。
func newAIHandler(cfg config.Config, dependencies *resources.Resources,
	resolver applicationagent.AuthoritativeAnalysisResolver, logger *slog.Logger,
) (http.Handler, error) {
	return newAIHandlerWithObservations(cfg, dependencies, resolver, logger, nil)
}

func newAIHandlerWithObservations(cfg config.Config, dependencies *resources.Resources,
	resolver applicationagent.AuthoritativeAnalysisResolver, logger *slog.Logger,
	recorder ports.ObservationRecorder,
) (http.Handler, error) {
	if resolver == nil {
		logger.Warn("未配置权威分析 resolver，智能研判 API 未挂载")
		return nil, nil
	}
	reporter, err := newAIServiceWithObservations(cfg, dependencies, resolver, logger, recorder)
	if err != nil {
		return nil, err
	}
	handler, err := aiapi.New(reporter, logger)
	if err != nil {
		return nil, fmt.Errorf("创建智能研判 HTTP 适配器: %w", err)
	}
	return isolateAIRouteContext(handler), nil
}

func isolateAIRouteContext(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, chi.NewRouteContext())
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newEvidenceSearcher(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (ports.EvidenceSearcher, error) {
	if !cfg.Search.Enabled {
		return nil, nil
	}
	if err := cfg.Search.Validate(); err != nil {
		return nil, err
	}
	provider, err := bocha.New(newAIHTTPClient(dependencies, logger, aiSearchHTTPTimeout), bocha.Config{
		BaseURL: cfg.Search.BaseURL, APIKey: cfg.Search.APIKey,
		MaxResults: cfg.Search.MaxResults, MaxAge: cfg.Search.MaxAge,
		TrustedDomains: cfg.Search.TrustedDomains,
	})
	if err != nil {
		return nil, fmt.Errorf("创建博查搜索适配器: %w", err)
	}
	return provider, nil
}

func newNarrativeGenerator(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (ports.NarrativeGenerator, error) {
	if !cfg.LLM.Enabled {
		return nil, nil
	}
	if err := cfg.LLM.Validate(); err != nil {
		return nil, err
	}
	provider, err := chatcompletions.New(newAIHTTPClient(dependencies, logger,
		narrativeHTTPTimeout(cfg.LLM.OutputAttempts)), chatcompletions.Config{
		ProviderName: cfg.LLM.ProviderName, BaseURL: cfg.LLM.BaseURL,
		APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
		MaxCompletionTokens: cfg.LLM.MaxCompletionTokens, OutputAttempts: cfg.LLM.OutputAttempts,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 LLM 适配器: %w", err)
	}
	return provider, nil
}

func narrativeHTTPTimeout(outputAttempts int) time.Duration {
	if outputAttempts < 1 || outputAttempts > aiMaxOutputAttempts {
		return aiNarrativeFallbackHTTPTimeout
	}
	retryQueue := time.Duration(outputAttempts-1) * aiRequestRate
	available := applicationagent.NarrativeStageTimeout - aiNarrativeStageSafetyMargin - retryQueue
	return available / time.Duration(outputAttempts)
}

func newAIHTTPClient(dependencies *resources.Resources, logger *slog.Logger,
	timeout time.Duration,
) *httpclient.Client {
	var cache ports.Cache
	if dependencies != nil && dependencies.Redis != nil {
		cache = refreshCache(dependencies)
	}
	return httpclient.New(httpclient.Options{
		HTTPClient: &http.Client{Timeout: timeout}, Cache: cache,
		Limiter: rate.NewLimiter(rate.Every(aiRequestRate), 1), Logger: logger,
		MaxAttempts: 1,
	})
}
