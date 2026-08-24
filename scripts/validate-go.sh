#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

docker run --rm -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c 'mkdir -p /tmp/build && go mod verify && go test ./... -count=1 && go vet ./... && go build -o /tmp/build/ ./...'
