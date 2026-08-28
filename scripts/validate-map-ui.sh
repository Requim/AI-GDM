#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
NODE_IMAGE=${NODE_VALIDATION_IMAGE:-node:22-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" "$GO_IMAGE" sh -c '
    go mod verify &&
    go test -race ./internal/adapters/http/hazardapi ./internal/adapters/http/webui ./cmd/server -count=20 &&
    go vet ./... &&
    go build ./...
  '

for script in api.js risk-map.js; do
  sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src "$NODE_IMAGE" \
    node --check "internal/adapters/http/webui/static/$script"
done

printf '%s\n' 'P8.2 风险地图、来源时效、截断提示与无数据降级验证通过'
