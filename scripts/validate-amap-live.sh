#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_FILE=${AMAP_ENV_FILE:-/home/ubuntu/.config/ai-gdm/amap.env}
IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

if [ ! -f "$ENV_FILE" ]; then
  printf '%s\n' "缺少高德运行配置：$ENV_FILE" >&2
  exit 1
fi
if [ "$(stat -c %a "$ENV_FILE")" != "600" ]; then
  printf '%s\n' '高德运行配置权限必须为 600' >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${AMAP_API_KEY:?缺少 AMAP_API_KEY}"

sh "$ROOT/scripts/run-validation-container.sh" \
  -v "$ROOT:/src:ro" \
  -w /src \
  -e "GOPROXY=$GO_PROXY" \
  -e AMAP_LIVE_TEST=1 \
  -e AMAP_API_KEY \
  -e AMAP_JSCODE \
  "$IMAGE" \
  go test ./internal/adapters/provider/amap -run '^TestLiveFindNearbyAndWalkingPlan$' -count=1

printf '%s\n' '真实高德 Web 服务医院候选与步行路线契约验证通过'
