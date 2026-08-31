#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-docker.XXXXXX")
SNAPSHOT_DIR="$WORK_DIR/source"
RUNTIME_DIR="$WORK_DIR/runtime"
ENV_FILE="$RUNTIME_DIR/runtime.env"
IMAGE_IIDFILE="$RUNTIME_DIR/app.iid"
COMPOSE_FILE=
RUN_ID=
PROJECT=
APP_IMAGE=
APP_IMAGE_ID=
ACTIVE_CHILD_PID=

stop_active_child() {
  [ -n "$ACTIVE_CHILD_PID" ] || return 0
  kill -TERM "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  kill -KILL "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  wait "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  ACTIVE_CHILD_PID=
}

run_active_command() {
  "$@" &
  ACTIVE_CHILD_PID=$!
  status=0
  wait "$ACTIVE_CHILD_PID" || status=$?
  ACTIVE_CHILD_PID=
  return "$status"
}

compose() {
  AI_GDM_RUNTIME_ENV_FILE="$ENV_FILE" docker compose \
    -f "$COMPOSE_FILE" --env-file "$ENV_FILE" -p "$PROJECT" "$@"
}

valid_object_id() {
  case "$1" in sha256:*) value=${1#sha256:} ;; *) value=$1 ;; esac
  case "$value" in ''|*[!0-9a-f]*) return 1 ;; esac
  [ "${#value}" -eq 64 ]
}

image_id_from_file() {
  [ -s "$IMAGE_IIDFILE" ] || return 1
  value=$(tr -d '\r\n' <"$IMAGE_IIDFILE" 2>/dev/null || true)
  valid_object_id "$value" || return 1
  printf '%s\n' "$value"
}

remove_owned_image() {
  [ -n "$APP_IMAGE" ] || return 0
  image_id=$(docker image inspect -f '{{.Id}}' "$APP_IMAGE" 2>/dev/null || true)
  valid_object_id "$image_id" || return 0
  [ "$(image_label ai.gdm.docker.run)" = "$RUN_ID" ] || return 0
  [ "$(image_label ai.gdm.docker.tree)" = "$SECURITY_TREE_SHA" ] || return 0
  [ "$(image_label ai.gdm.docker.source)" = "$SECURITY_SOURCE_SHA256" ] || return 0
  docker image rm "$APP_IMAGE" >/dev/null 2>&1 || true
}

image_label() {
  docker image inspect -f "{{index .Config.Labels \"$1\"}}" "$APP_IMAGE" 2>/dev/null || true
}

cleanup() {
  stop_active_child
  if [ -n "$PROJECT" ] && [ -n "$COMPOSE_FILE" ] && [ -s "$ENV_FILE" ]; then
    compose down -v --remove-orphans --timeout 15 >/dev/null 2>&1 || true
  fi
  remove_owned_image
  chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  rm -rf "$WORK_DIR"
}

terminate() {
  status=$1
  trap - EXIT HUP INT TERM
  cleanup
  exit "$status"
}

trap cleanup EXIT
trap 'terminate 129' HUP
trap 'terminate 130' INT
trap 'terminate 143' TERM

choose_port() {
  value=${AI_GDM_DOCKER_PORT:-$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')}
  case "$value" in ''|*[!0-9]*) security_fail 'AI_GDM_DOCKER_PORT 必须是有效端口' ;; esac
  [ "$value" -ge 1 ] && [ "$value" -le 65535 ] || security_fail 'AI_GDM_DOCKER_PORT 超出有效范围'
  printf '%s\n' "$value"
}

write_runtime_env() {
  umask 077
  cat >"$ENV_FILE" <<EOF
AI_GDM_IMAGE=$APP_IMAGE
AI_GDM_BIND_ADDRESS=127.0.0.1
AI_GDM_HTTP_PORT=$PORT
POSTGRES_PASSWORD=$DATABASE_SECRET
REDIS_PASSWORD=$CACHE_SECRET
DATABASE_URL=postgresql://ai_gdm:$DATABASE_SECRET@postgres:5432/ai_gdm?sslmode=disable
APP_ENV=production
APP_LOG_LEVEL=info
APP_ADMIN_TOKEN=$ADMIN_SECRET
APP_RATE_LIMIT_PER_MINUTE=120
APP_RATE_LIMIT_BURST=30
REFRESH_ENABLED=false
AMAP_ENABLED=false
BOCHA_ENABLED=false
LLM_ENABLED=false
EOF
}

build_app_image() {
  run_active_command docker build --pull=false --iidfile "$IMAGE_IIDFILE" \
    --build-arg "GOPROXY=$GO_PROXY" \
    --build-arg "VERSION=p10.1-validation" --build-arg "VCS_REF=$SECURITY_TREE_SHA" \
    --build-arg "BUILD_DATE=1970-01-01T00:00:00Z" \
    --label "ai.gdm.docker.run=$RUN_ID" --label "ai.gdm.docker.tree=$SECURITY_TREE_SHA" \
    --label "ai.gdm.docker.source=$SECURITY_SOURCE_SHA256" -t "$APP_IMAGE" "$SNAPSHOT_DIR"
  APP_IMAGE_ID=$(image_id_from_file) || security_fail 'P10.1 应用镜像 IID 无效'
}

assert_equal() {
  actual=$1
  expected=$2
  label=$3
  [ "$actual" = "$expected" ] || security_fail "$label 无效: actual=$actual expected=$expected"
}

service_id() {
  value=$(compose ps -q "$1")
  valid_object_id "$value" || security_fail "P10.1 $1 容器 ID 无效"
  printf '%s\n' "$value"
}

verify_runtime_contracts() {
  app_id=$(service_id app)
  postgres_id=$(service_id postgres)
  redis_id=$(service_id redis)
  assert_equal "$(docker exec "$app_id" id -u)" 10001 '应用 UID'
  assert_equal "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$app_id")" true '应用只读根文件系统'
  docker exec "$app_id" sh -c 'touch /read-only-probe' >/dev/null 2>&1 && security_fail '应用只读根文件系统可写'
  docker exec "$app_id" /usr/bin/gdal --version | grep -F 'GDAL 3.13.3' >/dev/null || security_fail 'GDAL 版本无效'
  [ -z "$(docker port "$postgres_id")" ] || security_fail 'PostgreSQL 不得映射宿主机端口'
  [ -z "$(docker port "$redis_id")" ] || security_fail 'Redis 不得映射宿主机端口'
  compose exec -T postgres psql -U ai_gdm -d ai_gdm -Atc \
    "SELECT extversion FROM pg_extension WHERE extname='postgis'" | grep -E '^3\.5([.]|$)' >/dev/null || \
    security_fail 'PostGIS 版本无效'
  assert_equal "$(compose exec -T postgres psql -U ai_gdm -d ai_gdm -Atc 'SELECT count(*) FROM schema_migrations')" 11 '数据库迁移数量'
  assert_equal "$(compose exec -T redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping')" PONG 'Redis 探针'
}

write_persistence_markers() {
  compose exec -T postgres psql -U ai_gdm -d ai_gdm -v ON_ERROR_STOP=1 -c \
    "CREATE TABLE p10_validation(value text PRIMARY KEY); INSERT INTO p10_validation(value) VALUES ('$RUN_ID');" >/dev/null
  compose exec -T -e "P10_RUN_ID=$RUN_ID" redis \
    sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli SET p10_validation "$P10_RUN_ID"' >/dev/null
  compose exec -T -e "P10_RUN_ID=$RUN_ID" app \
    sh -c 'printf "%s" "$P10_RUN_ID" > /var/lib/ai-gdm/lhasa/p10-validation' \
    >/dev/null 2>&1 || security_fail 'LHASA 持久化目录不可写'
}

verify_persistence_markers() {
  assert_equal "$(compose exec -T postgres psql -U ai_gdm -d ai_gdm -Atc 'SELECT value FROM p10_validation')" "$RUN_ID" 'PostgreSQL 持久化'
  assert_equal "$(compose exec -T redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli GET p10_validation')" "$RUN_ID" 'Redis 持久化'
  assert_equal "$(compose exec -T app cat /var/lib/ai-gdm/lhasa/p10-validation)" "$RUN_ID" 'LHASA 制品持久化'
}

verify_http() {
  curl -fsS --max-time 5 "http://127.0.0.1:$PORT/healthz" | grep -F '"status":"ok"' >/dev/null || security_fail '存活探针失败'
  curl -fsS --max-time 5 "http://127.0.0.1:$PORT/readyz" | grep -F '"status":"ready"' >/dev/null || security_fail '就绪探针失败'
  curl -fsS --max-time 5 "http://127.0.0.1:$PORT/" | grep -F 'AI-GDM 地质灾害辅助研判控制台' >/dev/null || security_fail 'Web 控制台失败'
}

verify_secret_logs() {
  logs=$(compose logs --no-color 2>&1)
  for secret in "$ADMIN_SECRET" "$DATABASE_SECRET" "$CACHE_SECRET"; do
    printf '%s' "$logs" | grep -F "$secret" >/dev/null && security_fail '容器日志泄露运行密钥'
  done
  return 0
}

verify_no_owned_resources() {
  [ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT")" ] || security_fail 'P10.1 留下容器'
  [ -z "$(docker volume ls -q --filter "label=com.docker.compose.project=$PROJECT")" ] || security_fail 'P10.1 留下卷'
  [ -z "$(docker network ls -q --filter "label=com.docker.compose.project=$PROJECT")" ] || security_fail 'P10.1 留下网络'
  docker image inspect "$APP_IMAGE" >/dev/null 2>&1 && security_fail 'P10.1 留下应用镜像标签'
  return 0
}

security_prepare_snapshot "$ROOT" "$SNAPSHOT_DIR"
RUN_ID=$(security_random_token)
ADMIN_SECRET=$(security_random_token)$(security_random_token)
DATABASE_SECRET=$(security_random_token)$(security_random_token)
CACHE_SECRET=$(security_random_token)$(security_random_token)
PORT=$(choose_port)
PROJECT="ai-gdm-p101-$RUN_ID"
APP_IMAGE="ai-gdm/server:p101-$SECURITY_TREE_SHA-$RUN_ID"
COMPOSE_FILE="$SNAPSHOT_DIR/compose.yaml"
mkdir -p "$RUNTIME_DIR"
write_runtime_env

build_app_image
compose config --quiet
run_active_command compose up -d --wait --wait-timeout 240
verify_http
verify_runtime_contracts
write_persistence_markers
run_active_command compose restart
run_active_command compose up -d --wait --wait-timeout 180
verify_http
verify_persistence_markers
verify_secret_logs
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"
FINAL_IMAGE_ID=$APP_IMAGE_ID
cleanup
verify_no_owned_resources
trap - EXIT HUP INT TERM

printf '%s\n' \
  "P10.1 容器门禁通过：mode=$SECURITY_SOURCE_MODE tree=$SECURITY_TREE_SHA source_sha256=$SECURITY_SOURCE_SHA256 image=$FINAL_IMAGE_ID"
