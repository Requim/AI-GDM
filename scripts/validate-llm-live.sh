#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_FILE=${LLM_ENV_FILE:-/home/ubuntu/.config/ai-gdm/runtime.env}
IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

if [ ! -f "$ENV_FILE" ]; then
  printf '%s\n' "缺少 LLM 运行配置：$ENV_FILE" >&2
  exit 1
fi
if [ "$(stat -c %a "$ENV_FILE")" != "600" ]; then
  printf '%s\n' 'LLM 运行配置权限必须为 600' >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${LLM_ENABLED:?缺少 LLM_ENABLED}"
: "${LLM_BASE_URL:?缺少 LLM_BASE_URL}"
: "${LLM_API_KEY:?缺少 LLM_API_KEY}"
: "${LLM_MODEL:?缺少 LLM_MODEL}"
if [ "$LLM_ENABLED" != "true" ]; then
  printf '%s\n' 'LLM_ENABLED 必须为 true' >&2
  exit 1
fi

OUTPUT=$(mktemp)
trap 'rm -f "$OUTPUT"' EXIT HUP INT TERM
STATUS=0
sh "$ROOT/scripts/run-validation-container.sh" \
  -v "$ROOT:/src:ro" \
  -w /src \
  -e "GOPROXY=$GO_PROXY" \
  -e LLM_LIVE_TEST=1 \
  -e LLM_PROVIDER_NAME \
  -e LLM_BASE_URL \
  -e LLM_API_KEY \
  -e LLM_MODEL \
  -e LLM_MAX_COMPLETION_TOKENS \
  -e LLM_OUTPUT_ATTEMPTS \
  "$IMAGE" \
  go test ./internal/adapters/provider/chatcompletions ./internal/application/agent \
  -run '^(TestLiveProviderState|TestGenerateDegradesProviderTimeoutsWhenRequestContextIsAlive)$' \
  -count=1 -v \
  >"$OUTPUT" 2>&1 || STATUS=$?

cat "$OUTPUT"
[ "$STATUS" -eq 0 ] || exit "$STATUS"
require_test_passed_once() {
  name=$1
  run_count=$(grep -F -x -c -- "=== RUN   $name" "$OUTPUT" || true)
  pass_count=$(grep -E -c -- "^--- PASS: $name \\([0-9]+(\\.[0-9]+)?s\\)$" "$OUTPUT" || true)
  bad_count=$(grep -E -c -- "^--- (FAIL|SKIP): $name " "$OUTPUT" || true)
  [ "$run_count" -eq 1 ] && [ "$pass_count" -eq 1 ] && [ "$bad_count" -eq 0 ]
}

require_test_passed_once TestLiveProviderState || {
  printf '%s\n' 'LLM 在线供应商状态测试未精确执行并通过' >&2
  exit 1
}
require_test_passed_once TestGenerateDegradesProviderTimeoutsWhenRequestContextIsAlive || {
  printf '%s\n' 'LLM 应用降级合同测试未精确执行并通过' >&2
  exit 1
}
RECOVERED=$(grep -E -c -e '(^|: )LLM_LIVE_RECOVERED$' "$OUTPUT" || true)
DEGRADED=$(grep -E -c -e '(^|: )LLM_UPSTREAM_DEGRADED$' "$OUTPUT" || true)
if [ "$RECOVERED" -eq 1 ] && [ "$DEGRADED" -eq 0 ]; then
  printf '%s\n' '真实 OpenAI 兼容 LLM 结构化输出契约验证通过'
  exit 0
fi
if [ "$RECOVERED" -eq 0 ] && [ "$DEGRADED" -eq 1 ]; then
  printf '%s\n' '真实 LLM 上游暂不可用，模型目录与系统降级合同验证通过'
  exit 75
fi

printf '%s\n' 'LLM 在线门禁未形成唯一供应商状态' >&2
exit 1
