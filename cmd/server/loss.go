package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Requim/AI-GDM/internal/adapters/http/lossapi"
	"github.com/Requim/AI-GDM/internal/adapters/storage/postgres"
	applicationloss "github.com/Requim/AI-GDM/internal/application/loss"
)

// newLossAPIHandler 创建损失计算、查询和来源审计接口。
func newLossAPIHandler(runtime *hazardRuntime, logger *slog.Logger) (http.Handler, error) {
	if runtime == nil || runtime.database == nil {
		logger.Warn("未配置 Postgres，损失 API 未挂载")
		return nil, nil
	}
	repository := postgres.NewHazardRepository(runtime.database)
	baseline := postgres.NewLossBaselineRepository(runtime.database)
	assessmentStore := postgres.NewLossAssessmentRepository(runtime.database)
	service, err := applicationloss.NewService(repository, baseline, utcClock{})
	if err != nil {
		return nil, fmt.Errorf("创建损失评估用例: %w", err)
	}
	handler, err := lossapi.New(service, assessmentStore, assessmentStore, "/api/v1/loss", logger)
	if err != nil {
		return nil, fmt.Errorf("创建损失 HTTP 适配器: %w", err)
	}
	return handler, nil
}
