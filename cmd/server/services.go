package main

import (
	"context"
	"errors"
	"fmt"
)

type runnableService interface {
	Run(context.Context) error
}

type serviceResult struct {
	name string
	err  error
}

func runServices(ctx context.Context, server runnableService, refresh runnableService) error {
	if refresh == nil {
		return server.Run(ctx)
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan serviceResult, 2)
	startService(serviceCtx, results, "HTTP 服务", server)
	startService(serviceCtx, results, "数据刷新调度器", refresh)
	first := <-results
	cancel()
	second := <-results
	return errors.Join(namedServiceError(first), namedServiceError(second))
}

func startService(ctx context.Context, results chan<- serviceResult,
	name string, service runnableService,
) {
	go func() {
		results <- serviceResult{name: name, err: service.Run(ctx)}
	}()
}

func namedServiceError(result serviceResult) error {
	if result.err == nil {
		return nil
	}
	return fmt.Errorf("%s退出: %w", result.name, result.err)
}
