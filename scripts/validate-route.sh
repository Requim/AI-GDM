#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c '
    go mod verify &&
    go test -race ./internal/adapters/provider/amap ./internal/adapters/http/mapapi ./internal/domain/evacuation ./cmd/server -count=20 &&
    go vet ./... &&
    go build ./...
  '

printf '%s\n' 'P4.3 多方式路线规划、公交城市编码和组合根验证通过'
