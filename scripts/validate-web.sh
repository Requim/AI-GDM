#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" "$IMAGE" sh -c '
    go mod verify &&
    go test -race ./internal/application/dashboard ./internal/adapters/http/webui ./cmd/server -count=20 &&
    go vet ./... &&
    go build ./...
  '

printf '%s\n' 'P8.1 中文控制台、数据状态和路由隔离验证通过'
