package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Requim/AI-GDM/internal/adapters/http/survivalapi"
	"github.com/Requim/AI-GDM/internal/adapters/storage/memory"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
)

type survivalRuntime struct {
	handler    http.Handler
	catalog    applicationsurvival.CatalogService
	assessment applicationsurvival.AssessmentService
}

func newSurvivalRuntime(logger *slog.Logger) (*survivalRuntime, error) {
	if logger == nil {
		return nil, fmt.Errorf("生还回放日志器为空")
	}
	catalog, err := memory.NewDefaultSurvivalCatalog()
	if err != nil {
		return nil, fmt.Errorf("创建历史案例目录: %w", err)
	}
	cases, err := applicationsurvival.NewCatalogService(catalog)
	if err != nil {
		return nil, fmt.Errorf("创建历史案例用例: %w", err)
	}
	assessment, err := applicationsurvival.NewAssessmentService(catalog, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建生还评估用例: %w", err)
	}
	handler, err := survivalapi.New(cases, assessment, logger)
	if err != nil {
		return nil, fmt.Errorf("创建生还回放 HTTP 适配器: %w", err)
	}
	return &survivalRuntime{handler: handler, catalog: cases, assessment: assessment}, nil
}

// newSurvivalAPIHandler 创建公开历史回放和合成场景评估接口。
func newSurvivalAPIHandler(logger *slog.Logger) (http.Handler, error) {
	runtime, err := newSurvivalRuntime(logger)
	if err != nil {
		return nil, err
	}
	return runtime.handler, nil
}
