#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

printf '%s\n' '运行离线全量 Go 门禁；PostgreSQL 集成与真实 TestLive 由对应专项脚本验证'
sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" -e "AI_GDM_LIVE_EXPOSURE=0" golang:1.26.7-bookworm \
  sh -c 'mkdir -p /tmp/build && go mod verify && go test ./... -count=1 && go vet ./... && go build -o /tmp/build/ ./...'
