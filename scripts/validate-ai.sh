#!/usr/bin/env sh
set -eu

# P7 离线验证领域校验、供应商契约和编排降级；不调用真实密钥。
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

sh "$ROOT/scripts/run-validation-container.sh" \
  -v "$ROOT:/src:ro" \
  -w /src \
  -e "GOPROXY=$GO_PROXY" \
  "$IMAGE" \
  sh -c 'set -eu
    go mod verify
    go test -race ./internal/domain/report ./internal/adapters/provider/bocha ./internal/adapters/provider/chatcompletions ./internal/application/agent ./internal/platform/config -count=20
    go vet ./...
    go build ./...'

printf '%s\n' "P7 搜索、OpenAI 兼容 LLM 与智能编排离线验证通过"
