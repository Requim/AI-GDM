#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
NODE_IMAGE=${NODE_VALIDATION_IMAGE:-node:22-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" "$GO_IMAGE" sh -c '
    go mod verify &&
    go test ./scripts -run "^TestValidateAssessmentUIScript$" -count=1 &&
    go test -race ./internal/adapters/http/webui ./internal/adapters/http/lossapi \
      ./internal/adapters/http/survivalapi ./internal/adapters/http/aiapi \
      ./internal/adapters/authority \
      ./internal/adapters/provider/httpclient ./internal/adapters/provider/bocha \
      ./internal/adapters/provider/chatcompletions ./internal/platform/httpserver \
      ./internal/application/agent ./internal/application/loss ./internal/application/survival \
      ./internal/domain/report ./internal/domain/loss ./internal/domain/survival ./cmd/server \
      ./tests/assessment-e2e/fixture -count=10 &&
    go vet ./... &&
    go build ./...
  '

for script in api.js assessment.js; do
  sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -w /src "$NODE_IMAGE" \
    node --check "internal/adapters/http/webui/static/$script"
done

printf '%s\n' 'P8.4 损失评估、历史回放、Authority 与非权威解释静态门禁通过'
