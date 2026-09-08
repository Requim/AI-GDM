#!/usr/bin/env sh
set -eu

# 在隔离源码目录运行全部前端夹具，共用浏览器镜像但不连接业务数据库。
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
WORK=${WORKSPACE_VALIDATION_DIR:?必须指定隔离验证目录}
CACHE=${WORKSPACE_CACHE_DIR:-"$WORK/cache"}
IMAGE=${WORKSPACE_BROWSER_IMAGE:-ai-gdm-ui-browser:p11}
GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
BASE_PORT=${WORKSPACE_BASE_PORT:-18280}
FIXTURE=
mkdir -p "$WORK" "$CACHE"
cleanup() {
  [ -z "$FIXTURE" ] || docker rm -f "$FIXTURE" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

run_suite() {
  suite=$1
  port=$2
  [ -z "$(ss -H -ltn "sport = :$port")" ] || { printf '验证端口 %s 已占用\n' "$port" >&2; return 1; }
  FIXTURE="ai-gdm-ui-$suite-$$"
  docker run -d --rm --name "$FIXTURE" --network host \
    -v "$ROOT:/src:ro" -v "$CACHE:/cache" -w /src \
    -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build \
    -e GOPROXY=https://goproxy.cn,direct -e "E2E_ADDR=127.0.0.1:$port" \
    -e "APP_ADMIN_TOKEN=$ADMIN_TOKEN" "$GO_IMAGE" go run "./tests/$suite/fixture" >/dev/null
  count=0
  until curl -fsS "http://127.0.0.1:$port/" >/dev/null; do
    count=$((count + 1))
    [ "$count" -lt 60 ] || { docker logs "$FIXTURE"; return 1; }
    sleep 1
  done
  docker run --rm --network host --shm-size=512m \
    -v "$ROOT/tests/$suite/specs:/opt/e2e/specs:ro" \
    -v "$ROOT/tests/$suite/playwright.config.js:/opt/e2e/playwright.config.js:ro" \
    -v "$ROOT/tests/security-e2e:/audit:ro" -v "$WORK:/evidence" \
    -v "$WORK/$suite-artifacts:/opt/e2e/test-results" \
    -e "E2E_BASE_URL=http://127.0.0.1:$port" -e "SECURITY_TEST_ADMIN_TOKEN=$ADMIN_TOKEN" \
    "$IMAGE" npx playwright test --reporter=json > "$WORK/$suite.json"
  docker rm -f "$FIXTURE" >/dev/null
  FIXTURE=
}

ADMIN_TOKEN=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
run_suite e2e "$BASE_PORT"
run_suite evacuation-e2e "$((BASE_PORT + 1))"
run_suite assessment-e2e "$((BASE_PORT + 2))"
run_suite security-e2e "$((BASE_PORT + 3))"
docker run --rm -v "$ROOT/tests/security-e2e:/audit:ro" -v "$WORK:/evidence:ro" \
  "$IMAGE" node /audit/audit-results.mjs /audit/expected-tests.json /evidence/security-e2e.json
printf '%s\n' '四组前端浏览器回归通过；测试夹具不代表实时供应商验收'
