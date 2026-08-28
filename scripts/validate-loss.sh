#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c 'go mod verify && go test -race ./internal/domain/loss ./internal/application/loss -count=20 && go vet ./... && go build ./...'

printf '%s\n' 'P5.2 确定性损失引擎验证通过'
