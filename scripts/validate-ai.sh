#!/usr/bin/env sh
set -eu

# P7 只验证本阶段的领域校验、供应商契约和编排降级；不调用真实密钥。
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

docker run --rm \
  -v "$ROOT:/src:ro" \
  -w /src \
  -e "GOPROXY=$GO_PROXY" \
  "$IMAGE" \
  sh -c 'set -eu
    go mod verify
    go test -race ./internal/domain/report ./internal/adapters/provider/bocha ./internal/adapters/provider/qwen ./internal/application/agent ./internal/platform/config -count=20
    go vet ./...
    go build ./...'

printf '%s\n' "P7 搜索、Qwen 与智能编排验证通过（未使用真实密钥）"
