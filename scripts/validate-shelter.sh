#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VALIDATION_ID="ai-gdm-p42-$$"
NETWORK="$VALIDATION_ID"
POSTGRES_NAME="$VALIDATION_ID-postgis"
POSTGRES_PASSWORD="validation-$VALIDATION_ID"
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

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
    echo "P4.2 PostGIS 未在期限内就绪" >&2
    exit 1
  fi
  sleep 2
done

sh "$ROOT/scripts/run-validation-container.sh" --network "$NETWORK" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" \
  -e "TEST_DATABASE_URL=postgres://ai_gdm:$POSTGRES_PASSWORD@$POSTGRES_NAME:5432/ai_gdm?sslmode=disable" \
  golang:1.26.7-bookworm sh -c '
    go mod verify &&
    go test -race ./internal/domain/spatial ./internal/application/evacuation ./internal/adapters/provider/amap ./internal/adapters/http/mapapi ./cmd/server -count=20 &&
    go test ./internal/adapters/storage/postgres -count=3 &&
    go vet ./... &&
    go build ./...
  '

printf '%s\n' 'P4.2 真实避险设施搜索、风险区过滤和组合根验证通过'
