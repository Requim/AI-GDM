package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewLossAPIHandlerBuildsPostgresProjection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newLossAPIHandler(&hazardRuntime{database: &pgxpool.Pool{}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	if handler == nil {
		t.Fatal("配置 Postgres 时未构建损失 API")
	}
}

func TestNewLossAPIHandlerSkipsWithoutPostgres(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newLossAPIHandler(nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if handler != nil {
		t.Fatal("缺少 Postgres 时不应构建损失 API")
	}
}
