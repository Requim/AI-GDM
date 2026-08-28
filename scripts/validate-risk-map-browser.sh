#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
BROWSER_IMAGE=${RISK_MAP_BROWSER_IMAGE:-ai-gdm-risk-map-browser:node22-chromium-playwright155}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')
NPM_REGISTRY=${NPM_REGISTRY:-https://registry.npmjs.org}
PORT=${RISK_MAP_E2E_PORT:-18080}
FIXTURE_NAME="ai-gdm-risk-map-browser-$$"

cleanup() {
  docker rm -f "$FIXTURE_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker build --pull=false --build-arg "NPM_REGISTRY=$NPM_REGISTRY" \
  -t "$BROWSER_IMAGE" -f "$ROOT/tests/e2e/Dockerfile" "$ROOT/tests/e2e"

sh "$ROOT/scripts/run-validation-container.sh" -d --name "$FIXTURE_NAME" --network host \
  -v "$ROOT:/src:ro" -w /src -e "GOPROXY=$GO_PROXY" -e "E2E_ADDR=127.0.0.1:$PORT" \
  "$GO_IMAGE" sh -c 'go run ./tests/e2e/fixture'

attempt=0
until curl -fsS "http://127.0.0.1:$PORT/__fixture/health" >/dev/null; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    docker logs "$FIXTURE_NAME" || true
    printf '%s\n' '风险地图浏览器 fixture 启动超时' >&2
    exit 1
  fi
  sleep 1
done

sh "$ROOT/scripts/run-validation-container.sh" --network host --shm-size=1g \
  -e "E2E_BASE_URL=http://127.0.0.1:$PORT" \
  -e E2E_CHROMIUM_PATH=/usr/bin/chromium "$BROWSER_IMAGE"

printf '%s\n' '风险地图 fail-closed 浏览器回归通过'
