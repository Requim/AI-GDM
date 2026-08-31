#!/usr/bin/env sh
set -eu

if [ "${AI_GDM_EXPOSURE_GATE_LIBRARY_ONLY:-0}" = "1" ]; then
  echo "生产暴露验证入口禁止启用 library-only 模式" >&2
  exit 64
fi

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VALIDATION_ID="ai-gdm-exposure-$$"
NETWORK="$VALIDATION_ID"
POSTGRES_NAME="$VALIDATION_ID-postgis"
POSTGRES_PASSWORD="validation-$VALIDATION_ID"
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"
LIVE_EXPOSURE="${AI_GDM_LIVE_EXPOSURE:-0}"
. "$ROOT/scripts/validate-exposure-gates.lib.sh"

cleanup() {
  docker rm -fv "$POSTGRES_NAME" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker network create "$NETWORK" >/dev/null
docker run -d --name "$POSTGRES_NAME" --network "$NETWORK" \
  -e POSTGRES_DB=ai_gdm -e POSTGRES_USER=ai_gdm \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  postgis/postgis:17-3.5 >/dev/null

attempt=0
until docker exec "$POSTGRES_NAME" pg_isready -U ai_gdm -d ai_gdm >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "PostGIS 未在期限内就绪" >&2
    exit 1
  fi
  sleep 2
done

sh "$ROOT/scripts/run-validation-container.sh" --network "$NETWORK" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" -e "AI_GDM_LIVE_EXPOSURE=$LIVE_EXPOSURE" \
  -e "TEST_DATABASE_URL=postgres://ai_gdm:$POSTGRES_PASSWORD@$POSTGRES_NAME:5432/ai_gdm?sslmode=disable" \
  golang:1.26.7-bookworm sh -c '
    set -eu
    . ./scripts/validate-exposure-gates.lib.sh
    go mod verify
    go test ./scripts \
      -run "^Test(ValidateExposureProvidersScript|ExposureGateLibrary.*|OfflineGates.*|LiveGate.*)$" -count=1
    echo "运行离线 provider/collector race 测试（显式禁用 TestLive）"
    AI_GDM_LIVE_EXPOSURE=0 go test -race ./internal/adapters/provider/httpclient \
      ./internal/adapters/provider/worldpop ./internal/adapters/provider/overpass \
      ./internal/adapters/provider/geoboundaries ./internal/application/exposurecollection -count=1
    run_postgres_gate
    run_cmd_gate
    if [ "$AI_GDM_LIVE_EXPOSURE" = "1" ]; then
      echo "运行一次真实 WorldPop/Overpass/geoBoundaries TestLive 专项"
      run_live_gate
    fi
  '

docker exec "$POSTGRES_NAME" psql -U ai_gdm -d ai_gdm -Atc 'SELECT postgis_full_version();'
