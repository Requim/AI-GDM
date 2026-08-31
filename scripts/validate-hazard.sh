#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VALIDATION_ID="ai-gdm-p33-$$"
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
    echo "P3.3 PostGIS 未在期限内就绪" >&2
    exit 1
  fi
  sleep 2
done

sh "$ROOT/scripts/run-validation-container.sh" --network "$NETWORK" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" \
  -e "TEST_DATABASE_URL=postgres://ai_gdm:$POSTGRES_PASSWORD@$POSTGRES_NAME:5432/ai_gdm?sslmode=disable" \
  golang:1.26.7-bookworm sh -c '
    go mod verify &&
    go test -race ./internal/application/hazard ./internal/adapters/http/hazardapi ./internal/platform/httpserver ./cmd/server -count=20 &&
    go test ./internal/adapters/storage/postgres -count=5 &&
    go vet ./... &&
    go build ./...
  '

printf '%s\n' 'P3.3 风险预警接口、注册表和 PostGIS 一致性验证通过'
