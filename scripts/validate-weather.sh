#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c 'go test -race ./internal/adapters/provider/httpclient ./internal/adapters/provider/openmeteo ./internal/application/collection ./internal/platform/scheduler ./cmd/server -count=10'

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" -e OPEN_METEO_LIVE_TEST=1 -e OPEN_METEO_API_KEY \
  golang:1.26.7-bookworm \
  sh -c "go test ./internal/adapters/provider/openmeteo -run '^TestLiveForecast$' -count=1 -v"
