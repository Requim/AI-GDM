#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="https://goproxy.cn,direct"

docker run --rm -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c '
    go mod verify &&
    go test -race ./internal/domain/survival ./internal/application/survival ./internal/adapters/storage/memory -count=20 &&
    go vet ./internal/domain/survival ./internal/application/survival ./internal/adapters/storage/memory ./cmd/server &&
    go build ./...
  '

printf '%s\n' 'P6.1 历史案例目录、匿名回放场景和时间来源边界验证通过'
