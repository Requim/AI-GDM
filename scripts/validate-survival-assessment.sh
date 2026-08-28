#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="https://goproxy.cn,direct"

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c '
    go mod verify &&
    go test -race ./internal/domain/survival ./internal/application/survival -count=20 &&
    go vet ./internal/domain/survival ./internal/application/survival ./cmd/server &&
    go build ./...
  '

printf '%s\n' 'P6.2 生还规则、概率区间、搜救优先级和应用用例验证通过'
