#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

docker run --rm -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c '
    go mod verify &&
    go test -race ./internal/domain/survival ./internal/application/survival ./internal/adapters/storage/memory ./internal/adapters/http/survivalapi -count=20 &&
    go vet ./internal/domain/survival ./internal/application/survival ./internal/adapters/storage/memory ./internal/adapters/http/survivalapi ./cmd/server &&
    go build ./...
  '

printf '%s\n' 'P6.1-P6.3 历史案例、确定性生还评估和回放 API 验证通过'
