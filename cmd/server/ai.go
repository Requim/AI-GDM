package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

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
	aiHTTPTimeout = 30 * time.Second
	aiRequestRate = 500 * time.Millisecond
)

// newAIService 在组合根装配可选的博查和 LLM 适配器。
func newAIService(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (*applicationagent.Service, error) {
	search, err := newEvidenceSearcher(cfg, dependencies, logger)
	if err != nil {
		return nil, err
	}
	generator, err := newNarrativeGenerator(cfg, dependencies, logger)
	if err != nil {
		return nil, err
	}
	service, err := applicationagent.New(search, generator, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建智能研判编排服务: %w", err)
	}
	return service, nil
}

// newAIHandler 将可选供应商装配为 /api/v1/ai 的 HTTP 适配器。
func newAIHandler(cfg config.Config, dependencies *resources.Resources,
	logger *slog.Logger,
) (http.Handler, error) {
	service, err := newAIService(cfg, dependencies, logger)
	if err != nil {
		return nil, err
	}
	handler, err := aiapi.New(service, logger)
	if err != nil {
		return nil, fmt.Errorf("创建智能研判 HTTP 适配器: %w", err)
	}
	return handler, nil
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
	provider, err := bocha.New(newAIHTTPClient(dependencies, logger), bocha.Config{
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
	provider, err := chatcompletions.New(newAIHTTPClient(dependencies, logger), chatcompletions.Config{
		ProviderName: cfg.LLM.ProviderName, BaseURL: cfg.LLM.BaseURL,
		APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
		MaxCompletionTokens: cfg.LLM.MaxCompletionTokens, OutputAttempts: cfg.LLM.OutputAttempts,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 LLM 适配器: %w", err)
	}
	return provider, nil
}

func newAIHTTPClient(dependencies *resources.Resources, logger *slog.Logger) *httpclient.Client {
	var cache ports.Cache
	if dependencies != nil && dependencies.Redis != nil {
		cache = refreshCache(dependencies)
	}
	return httpclient.New(httpclient.Options{
		HTTPClient: &http.Client{Timeout: aiHTTPTimeout}, Cache: cache,
		Limiter: rate.NewLimiter(rate.Every(aiRequestRate), 1), Logger: logger,
		MaxAttempts: 2,
	})
}
