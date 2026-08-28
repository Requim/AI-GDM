#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VALIDATION_ID="ai-gdm-p13-$$"
NETWORK="$VALIDATION_ID"
POSTGRES_NAME="$VALIDATION_ID-postgis"
REDIS_NAME="$VALIDATION_ID-redis"
POSTGRES_PASSWORD="validation-$VALIDATION_ID"
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"

cleanup() {
  docker rm -f "$POSTGRES_NAME" "$REDIS_NAME" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker network create "$NETWORK" >/dev/null
docker run -d --name "$POSTGRES_NAME" --network "$NETWORK" \
  -e POSTGRES_DB=ai_gdm -e POSTGRES_USER=ai_gdm \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  postgis/postgis:17-3.5 >/dev/null
docker run -d --name "$REDIS_NAME" --network "$NETWORK" \
  redis:7.4.10-bookworm redis-server --save '' --appendonly no >/dev/null

attempt=0
until docker exec "$POSTGRES_NAME" pg_isready -U ai_gdm -d ai_gdm >/dev/null 2>&1 && \
      docker exec "$REDIS_NAME" redis-cli ping >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "PostGIS 或 Redis 未在期限内就绪" >&2
    exit 1
  fi
  sleep 2
done

sh "$ROOT/scripts/run-validation-container.sh" --network "$NETWORK" -v "$ROOT:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" \
  -e "TEST_DATABASE_URL=postgres://ai_gdm:$POSTGRES_PASSWORD@$POSTGRES_NAME:5432/ai_gdm?sslmode=disable" \
  -e "TEST_REDIS_ADDR=$REDIS_NAME:6379" golang:1.26.7-bookworm \
  sh -c 'mkdir -p /tmp/build && go mod verify && go test ./... -count=1 && go vet ./... && go build -o /tmp/build/ ./...'

docker exec "$POSTGRES_NAME" psql -U ai_gdm -d ai_gdm -Atc 'SELECT postgis_full_version();'
docker exec "$REDIS_NAME" redis-cli ping
