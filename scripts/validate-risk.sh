#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

docker run --rm -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c 'go test -race ./internal/domain/risk -count=20 && go test ./internal/domain/hazard -count=5'

printf '%s\n' 'P3.1 风险引擎确定性验证通过'
