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

docker run --rm \
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
  go test ./internal/adapters/provider/chatcompletions -run '^TestLiveGenerate$' -count=1

printf '%s\n' '真实 OpenAI 兼容 LLM 结构化输出契约验证通过'
