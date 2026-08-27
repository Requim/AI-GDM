#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

docker run --rm -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c '
    go mod verify &&
    go test -race ./internal/domain/report ./internal/adapters/provider/qwen ./internal/platform/config -count=20 &&
    go vet ./internal/domain/report ./internal/adapters/provider/qwen ./internal/platform/config ./cmd/server &&
    go build ./...
  '

printf '%s\n' 'P7.2 Qwen 结构化输出、重试和提示注入防护验证通过'
